package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedReq is one request the fake backend received, kept so a test can
// assert the emitter hit the right endpoint with the right body.
type capturedReq struct {
	method string
	path   string
	body   map[string]any
	list   []map[string]any // populated when the body is a JSON array (findings)
}

// fakeBackend is a minimal stand-in for the sidekick-api: it resolves/creates a
// project and accepts every live-emit POST, recording each request.
type fakeBackend struct {
	mu       sync.Mutex
	reqs     []capturedReq
	projects []map[string]any // seeded matches for GET /projects
	runID    int64
}

func (f *fakeBackend) record(r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	cr := capturedReq{method: r.Method, path: r.URL.Path}
	if len(raw) > 0 {
		if raw[0] == '[' {
			_ = json.Unmarshal(raw, &cr.list)
		} else {
			_ = json.Unmarshal(raw, &cr.body)
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, cr)
	f.mu.Unlock()
}

func (f *fakeBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Method == http.MethodGet {
			writeJSON(w, 200, f.projects)
			return
		}
		writeJSON(w, 201, map[string]any{"project_id": "created-pid"})
	})
	// Everything under a project: sessions, edits, batches, verifier-runs,
	// findings, heartbeats. The verifier-run POST returns a server-assigned id.
	mux.HandleFunc("/api/projects/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if strings.HasSuffix(r.URL.Path, "/verifier-runs") && r.Method == http.MethodPost {
			f.mu.Lock()
			f.runID++
			id := f.runID
			f.mu.Unlock()
			writeJSON(w, 201, map[string]any{"id": id})
			return
		}
		writeJSON(w, 201, map[string]any{})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeBackend) snapshot() []capturedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedReq, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// find returns the first captured request matching method and a path suffix.
func (f *fakeBackend) find(method, suffix string) (capturedReq, bool) {
	for _, r := range f.snapshot() {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			return r, true
		}
	}
	return capturedReq{}, false
}

func TestRemoteEmitterFailsClosedOnUnhealthyBackend(t *testing.T) {
	// A reachable URL that is not a live sidekick-api (health 503) must fail at
	// open so the caller can fall back to local instead of silently dropping
	// every event.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/health") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		t.Fatalf("no request should be made past a failed healthcheck: %s", r.URL.Path)
	}))
	defer srv.Close()

	if _, err := OpenRemote(srv.URL+"/api", "fp", "repo", "/repos/repo"); err == nil {
		t.Fatalf("OpenRemote should error when the backend healthcheck fails")
	}
}

func TestRemoteEmitterReusesProjectByFingerprint(t *testing.T) {
	fb := &fakeBackend{projects: []map[string]any{
		{"project_id": "existing-pid", "repo_fingerprint": "fp-123"},
	}}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	e, err := OpenRemote(srv.URL+"/api", "fp-123", "myrepo", "/repos/myrepo")
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer e.Close()

	if e.ProjectID() != "existing-pid" {
		t.Fatalf("ProjectID = %q, want existing-pid", e.ProjectID())
	}
	if _, ok := fb.find(http.MethodPost, "/projects"); ok {
		t.Fatalf("matched fingerprint should not POST a new project")
	}
}

func TestRemoteEmitterCreatesProjectWhenAbsent(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	e, err := OpenRemote(srv.URL+"/api", "fp-new", "myrepo", "/repos/myrepo")
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer e.Close()

	if e.ProjectID() != "created-pid" {
		t.Fatalf("ProjectID = %q, want created-pid", e.ProjectID())
	}
	created, ok := fb.find(http.MethodPost, "/projects")
	if !ok {
		t.Fatalf("absent fingerprint should POST a new project")
	}
	if created.body["repo_fingerprint"] != "fp-new" || created.body["name"] != "myrepo" {
		t.Fatalf("create body = %+v, want fingerprint+name carried", created.body)
	}
}

func TestRemoteEmitterPostsEachEvent(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	e, err := OpenRemote(srv.URL+"/api", "fp", "repo", "/repos/repo")
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	pid := e.ProjectID()
	now := time.Now()

	if err := e.RecordSession(SessionRecord{SessionID: "S1", GoalText: "do x", StartedAt: now}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	if err := e.RecordEdit(EditRecord{SessionID: "S1", FilePath: "a.go", Seq: 1, TS: now}); err != nil {
		t.Fatalf("RecordEdit: %v", err)
	}
	if err := e.RecordBatch(BatchRecord{BatchID: "B1", SessionID: "S1", FileSet: []string{"a.go"}, FileCount: 1, TS: now}); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	runID, err := e.RecordVerifierRun(VerifierRunRecord{BatchID: "B1", SessionID: "S1", VerifierName: "architect", Distance: 0.5, TS: now})
	if err != nil {
		t.Fatalf("RecordVerifierRun: %v", err)
	}
	if runID != 1 {
		t.Fatalf("RecordVerifierRun id = %d, want 1 (server-assigned)", runID)
	}
	if err := e.RecordFindings(runID, []FindingRecord{{SessionID: "S1", VerifierName: "architect", FilePath: "a.go", Distance: 0.5, TS: now}}); err != nil {
		t.Fatalf("RecordFindings: %v", err)
	}
	if err := e.RecordHeartbeat(HeartbeatRecord{SessionID: "S1", OverallDistance: 0.5, TS: now}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	// Close drains the async edit worker before we assert.
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, tc := range []struct{ method, suffix string }{
		{http.MethodPost, "/projects/" + pid + "/sessions"},
		{http.MethodPost, "/projects/" + pid + "/sessions/S1/edits"},
		{http.MethodPost, "/projects/" + pid + "/batches"},
		{http.MethodPost, "/projects/" + pid + "/verifier-runs"},
		{http.MethodPost, "/projects/" + pid + "/verifier-runs/1/findings"},
		{http.MethodPost, "/projects/" + pid + "/sessions/S1/heartbeats"},
	} {
		if _, ok := fb.find(tc.method, tc.suffix); !ok {
			t.Fatalf("missing %s %s; got %+v", tc.method, tc.suffix, fb.snapshot())
		}
	}

	session, _ := fb.find(http.MethodPost, "/sessions")
	if session.body["session_id"] != "S1" || session.body["goal_text"] != "do x" {
		t.Fatalf("session body = %+v", session.body)
	}
	findings, _ := fb.find(http.MethodPost, "/findings")
	if len(findings.list) != 1 || findings.list[0]["file_path"] != "a.go" {
		t.Fatalf("findings body = %+v", findings.list)
	}
}

func TestRemoteEmitterDrainsBufferedEditsOnClose(t *testing.T) {
	fb := &fakeBackend{}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	e, err := OpenRemote(srv.URL+"/api", "fp", "repo", "/repos/repo")
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	for i := range 10 {
		if err := e.RecordEdit(EditRecord{SessionID: "S1", FilePath: "f.go", Seq: i, TS: time.Now()}); err != nil {
			t.Fatalf("RecordEdit: %v", err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	edits := 0
	for _, r := range fb.snapshot() {
		if strings.HasSuffix(r.path, "/edits") {
			edits++
		}
	}
	if edits != 10 {
		t.Fatalf("posted %d edits, want 10 (Close must drain the worker)", edits)
	}
}
