package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// RemoteEmitter is an [Emitter] that posts each event to the Sidekick HTTP API
// (the "backend" sink) instead of the local SQLite [Store], one POST per call.
//
// Safe for concurrent use. Per the package contract a producer never fails on
// telemetry: a failed POST is logged, not returned. The edit write is async (a
// background worker keeps the daemon's file-write hook off the network); the
// rest are synchronous, so the session row lands before the children whose
// foreign keys the API enforces.
type RemoteEmitter struct {
	client    *http.Client
	apiBase   string
	base      string // "<api>/projects/<projectID>"
	tokenFunc func() string
	projectID string
	fp        string
	name      string
	rootPath  string

	edits   chan EditRecord
	closing chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	project sync.Mutex

	logf func(format string, args ...any)
}

// editQueueCap bounds the in-flight edit buffer. Sized generously: edits are
// small and the worker drains them fast against a local backend. If a slow or
// unreachable backend ever fills it, RecordEdit drops with a log rather than
// blocking the daemon's write path.
const editQueueCap = 256

// RemoteOption customizes the remote telemetry client.
type RemoteOption func(*remoteOptions)

type remoteOptions struct {
	tokenFunc func() string
}

// WithAuthToken attaches a bearer token to each backend request.
func WithAuthToken(token string) RemoteOption {
	return func(o *remoteOptions) {
		token = strings.TrimSpace(token)
		o.tokenFunc = func() string { return token }
	}
}

// WithAuthTokenProvider attaches the current bearer token to each backend
// request. The provider is called for every request so long-running daemons can
// pick up a refreshed CLI login without restarting.
func WithAuthTokenProvider(provider func() string) RemoteOption {
	return func(o *remoteOptions) {
		if provider == nil {
			return
		}
		o.tokenFunc = func() string { return strings.TrimSpace(provider()) }
	}
}

// OpenRemote resolves (or creates) the project keyed by repoFingerprint on the
// backend at apiBaseURL (e.g. "http://localhost:8000/api"), then returns an
// emitter bound to it. name and rootPath seed a freshly created project; they
// are ignored when a project with the same fingerprint already exists. An
// unreachable backend surfaces as an error so the caller can fall back to local.
func OpenRemote(apiBaseURL, repoFingerprint, name, rootPath string, opts ...RemoteOption) (*RemoteEmitter, error) {
	base := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("telemetry: empty backend url")
	}
	var options remoteOptions
	for _, opt := range opts {
		opt(&options)
	}
	tokenFunc := options.tokenFunc
	if tokenFunc == nil {
		tokenFunc = func() string { return "" }
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if err := pingHealth(client, base, tokenFunc()); err != nil {
		return nil, fmt.Errorf("telemetry: backend healthcheck %s: %w", base+"/health", err)
	}
	projectID, err := resolveProject(client, base, tokenFunc(), repoFingerprint, name, rootPath)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resolve project: %w", err)
	}
	e := &RemoteEmitter{
		client:    client,
		apiBase:   base,
		base:      fmt.Sprintf("%s/projects/%s", base, projectID),
		tokenFunc: tokenFunc,
		projectID: projectID,
		fp:        repoFingerprint,
		name:      name,
		rootPath:  rootPath,
		edits:     make(chan EditRecord, editQueueCap),
		closing:   make(chan struct{}),
		logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[sidekick] "+format+"\n", args...)
		},
	}
	e.wg.Add(1)
	go e.editWorker()
	return e, nil
}

// ProjectID returns the resolved backend project id this emitter writes to.
func (e *RemoteEmitter) ProjectID() string { return e.projectID }

func (e *RemoteEmitter) authToken() string {
	if e.tokenFunc == nil {
		return ""
	}
	return strings.TrimSpace(e.tokenFunc())
}

// pingHealth checks the backend is a reachable, live sidekick-api before we
// commit to it, so a wrong/down URL fails fast at start and the caller falls
// back to local instead of silently dropping every event into the void.
func pingHealth(client *http.Client, base, token string) error {
	return doJSON(client, token, http.MethodGet, base+"/health", nil, nil)
}

func resolveProject(client *http.Client, base, token, fingerprint, name, rootPath string) (string, error) {
	body := map[string]any{"name": name, "repo_fingerprint": fingerprint, "root_path": rootPath}
	if repoFQN := repoFQNFromName(name); repoFQN != "" {
		body["repo_fqn"] = repoFQN
	}
	var resolved struct {
		ProjectID string `json:"project_id"`
	}
	if err := doJSON(client, token, http.MethodPost, base+"/projects/resolve", body, &resolved); err != nil {
		return "", err
	}
	return resolved.ProjectID, nil
}

func repoFQNFromName(name string) string {
	name = strings.TrimSpace(name)
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return ""
	}
	return name
}

func (e *RemoteEmitter) RecordSession(r SessionRecord) error {
	return e.post("/sessions", sessionBody{
		SessionID: r.SessionID,
		GoalText:  r.GoalText,
		GoalClass: r.GoalClass,
		BaseRef:   r.BaseRef,
		Worktree:  r.Worktree,
		AgentKind: r.AgentKind,
		StartedAt: r.StartedAt.UTC(),
	}, nil)
}

// RecordEdit hands the row to the background worker and returns immediately so
// the daemon's write hook never blocks on the network. A full queue drops the
// edit (logged) rather than stalling the producer.
func (e *RemoteEmitter) RecordEdit(r EditRecord) error {
	select {
	case <-e.closing:
		return nil
	default:
	}
	select {
	case e.edits <- r:
	default:
		e.logf("telemetry: edit queue full, dropping %s", r.FilePath)
	}
	return nil
}

func (e *RemoteEmitter) RecordBatch(r BatchRecord) error {
	var fileSet string
	if len(r.FileSet) > 0 {
		if b, err := json.Marshal(r.FileSet); err == nil {
			fileSet = string(b)
		}
	}
	return e.post("/batches", batchBody{
		BatchID:   r.BatchID,
		SessionID: r.SessionID,
		TS:        r.TS.UTC(),
		FileSet:   fileSet,
		FileCount: r.FileCount,
		BaseRef:   r.BaseRef,
	}, nil)
}

// RecordVerifierRun posts the run and returns the server-assigned id so the
// caller can parent the run's findings, mirroring the local store's
// LastInsertId. A failed post returns id 0 so the caller skips its findings.
func (e *RemoteEmitter) RecordVerifierRun(r VerifierRunRecord) (int64, error) {
	body := verifierRunBody{
		BatchID:         r.BatchID,
		SessionID:       r.SessionID,
		VerifierName:    r.VerifierName,
		VerifierVersion: r.VerifierVersion,
		Distance:        r.Distance,
		Reason:          r.Reason,
		Status:          r.Status,
		DurationMS:      r.DurationMS,
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		CacheReads:      r.CacheReads,
		CacheWrites:     r.CacheWrites,
		CostUSD:         r.CostUSD,
		TS:              r.TS.UTC(),
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := e.post("/verifier-runs", body, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (e *RemoteEmitter) RecordFindings(runID int64, findings []FindingRecord) error {
	if runID == 0 || len(findings) == 0 {
		return nil
	}
	body := make([]findingBody, 0, len(findings))
	for _, f := range findings {
		body = append(body, findingBody{
			SessionID:     f.SessionID,
			BatchID:       f.BatchID,
			VerifierName:  f.VerifierName,
			FilePath:      f.FilePath,
			Symbol:        f.Symbol,
			Line:          f.Line,
			Distance:      f.Distance,
			Reason:        f.Reason,
			HunkHash:      f.HunkHash,
			DirtyDiffHash: f.DirtyDiffHash,
			TS:            f.TS.UTC(),
		})
	}
	return e.post(fmt.Sprintf("/verifier-runs/%d/findings", runID), body, nil)
}

func (e *RemoteEmitter) RecordHeartbeat(r HeartbeatRecord) error {
	return e.post("/sessions/"+r.SessionID+"/heartbeats", heartbeatBody{
		TS:              r.TS.UTC(),
		OverallDistance: r.OverallDistance,
		BatchCount:      r.BatchCount,
		EditCount:       r.EditCount,
	}, nil)
}

// Close stops the edit worker after it drains whatever is buffered, so a final
// burst of edits still reaches the backend on a clean shutdown.
func (e *RemoteEmitter) Close() error {
	e.once.Do(func() { close(e.closing) })
	e.wg.Wait()
	return nil
}

// editWorker is the single consumer of the edit queue. It posts each edit in
// arrival order; on close it drains the buffer before exiting. The edits
// channel is never closed (producers may still race a Close), so sends never
// panic.
func (e *RemoteEmitter) editWorker() {
	defer e.wg.Done()
	post := func(r EditRecord) {
		if err := e.post("/sessions/"+r.SessionID+"/edits", editBody{
			FilePath: r.FilePath,
			Seq:      r.Seq,
			TS:       r.TS.UTC(),
		}, nil); err != nil {
			e.logf("telemetry: record edit (remote): %v", err)
		}
	}
	for {
		select {
		case r := <-e.edits:
			post(r)
		case <-e.closing:
			for {
				select {
				case r := <-e.edits:
					post(r)
				default:
					return
				}
			}
		}
	}
}

// post sends body as JSON to base+path. A nil out skips response decoding; a
// non-2xx status is an error carrying a snippet of the body for diagnosis.
func (e *RemoteEmitter) post(path string, body, out any) error {
	token := e.authToken()
	if err := doJSON(e.client, token, http.MethodPost, e.base+path, body, out); err != nil {
		if isStatus(err, http.StatusUnauthorized) {
			if fresh := e.authToken(); fresh != token {
				if retryErr := doJSON(e.client, fresh, http.MethodPost, e.base+path, body, out); retryErr == nil {
					return nil
				} else {
					err = retryErr
				}
			}
		}
		if !isStatus(err, http.StatusNotFound) {
			return err
		}
		if rerr := e.refreshProject(); rerr != nil {
			return err
		}
		return doJSON(e.client, e.authToken(), http.MethodPost, e.base+path, body, out)
	}
	return nil
}

func (e *RemoteEmitter) refreshProject() error {
	e.project.Lock()
	defer e.project.Unlock()
	projectID, err := resolveProject(e.client, e.apiBase, e.authToken(), e.fp, e.name, e.rootPath)
	if err != nil {
		return err
	}
	if projectID == "" {
		return fmt.Errorf("telemetry: resolve project returned empty id")
	}
	if projectID != e.projectID {
		e.logf("telemetry: refreshed backend project %s -> %s", e.projectID, projectID)
		e.projectID = projectID
		e.base = fmt.Sprintf("%s/projects/%s", e.apiBase, projectID)
	}
	return nil
}

func doJSON(client *http.Client, token, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return httpError{
			Method:     method,
			URL:        url,
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Snippet:    strings.TrimSpace(string(snippet)),
		}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type httpError struct {
	Method     string
	URL        string
	Status     string
	StatusCode int
	Snippet    string
}

func (e httpError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.URL, e.Status, e.Snippet)
}

func isStatus(err error, code int) bool {
	var he httpError
	if errors.As(err, &he) {
		return he.StatusCode == code
	}
	return false
}

// Request bodies mirror the API's *Create schemas: the server-assigned id and
// any parent id carried in the URL path are omitted. omitempty on a field means
// the local store writes it as SQL NULL when empty, so the backend stays
// byte-compatible with a `sidekick export` of the same session.

type sessionBody struct {
	SessionID string    `json:"session_id"`
	GoalText  string    `json:"goal_text"`
	GoalClass string    `json:"goal_class,omitempty"`
	BaseRef   string    `json:"base_ref,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	AgentKind string    `json:"agent_kind,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

type editBody struct {
	FilePath string    `json:"file_path"`
	Seq      int       `json:"seq"`
	TS       time.Time `json:"ts"`
}

type batchBody struct {
	BatchID   string    `json:"batch_id"`
	SessionID string    `json:"session_id"`
	TS        time.Time `json:"ts"`
	FileSet   string    `json:"file_set,omitempty"`
	FileCount int       `json:"file_count"`
	BaseRef   string    `json:"base_ref,omitempty"`
}

type verifierRunBody struct {
	BatchID         string    `json:"batch_id,omitempty"`
	SessionID       string    `json:"session_id"`
	VerifierName    string    `json:"verifier_name"`
	VerifierVersion string    `json:"verifier_version,omitempty"`
	Distance        float64   `json:"distance"`
	Reason          string    `json:"reason"`
	Status          string    `json:"status,omitempty"`
	DurationMS      int64     `json:"duration_ms"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CacheReads      int       `json:"cache_reads"`
	CacheWrites     int       `json:"cache_writes"`
	CostUSD         float64   `json:"cost_usd"`
	TS              time.Time `json:"ts"`
}

type findingBody struct {
	SessionID     string    `json:"session_id"`
	BatchID       string    `json:"batch_id,omitempty"`
	VerifierName  string    `json:"verifier_name"`
	FilePath      string    `json:"file_path,omitempty"`
	Symbol        string    `json:"symbol,omitempty"`
	Line          int       `json:"line,omitempty"`
	Distance      float64   `json:"distance"`
	Reason        string    `json:"reason,omitempty"`
	HunkHash      string    `json:"hunk_hash,omitempty"`
	DirtyDiffHash string    `json:"dirty_diff_hash,omitempty"`
	TS            time.Time `json:"ts"`
}

type heartbeatBody struct {
	TS              time.Time `json:"ts"`
	OverallDistance float64   `json:"overall_distance"`
	BatchCount      int       `json:"batch_count"`
	EditCount       int       `json:"edit_count"`
}
