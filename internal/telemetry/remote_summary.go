package telemetry

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errRemoteNotFound is returned by the read client for an HTTP 404 so callers
// can treat a missing resource (e.g. a project/session not posted yet) as an
// empty result rather than a hard error.
var errRemoteNotFound = errors.New("telemetry: remote resource not found")

// FetchSummary reads one episode's summary from the backend for the panel to
// render in backend mode, mapping the project's session-list element to a
// [Summary]. The list carries the executive numbers but no per-verifier series,
// so Verifiers is empty. A missing session or a 404 yields SessionFound=false
// (mirroring the local LoadSummary) rather than an error; an empty sessionID
// returns a zero Summary without a request.
func (e *RemoteEmitter) FetchSummary(sessionID string) (Summary, error) {
	if sessionID == "" {
		return Summary{}, nil
	}
	var sessions []sessionSummaryPayload
	err := getJSON(e.client, e.token, e.base+"/sessions", &sessions)
	if errors.Is(err, errRemoteNotFound) {
		return Summary{SessionID: sessionID}, nil
	}
	if err != nil {
		return Summary{SessionID: sessionID}, err
	}
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return s.toSummary(), nil
		}
	}
	return Summary{SessionID: sessionID}, nil
}

// sessionSummaryPayload mirrors the API's SessionSummary list element. Only the
// fields the panel renders are decoded; the rest of the contract is ignored.
type sessionSummaryPayload struct {
	SessionID       string    `json:"session_id"`
	GoalText        string    `json:"goal_text"`
	BaseRef         string    `json:"base_ref"`
	Worktree        string    `json:"worktree"`
	StartedAt       time.Time `json:"started_at"`
	EditCount       int       `json:"edit_count"`
	BatchCount      int       `json:"batch_count"`
	RunCount        int       `json:"verifier_run_count"`
	TotalCostUSD    float64   `json:"total_cost_usd"`
	CurrentDistance *float64  `json:"current_distance"` // null until a heartbeat lands
}

// toSummary maps the list element to the panel's read model. HeartbeatCount,
// TotalTokens and Verifiers stay zero/empty: the list endpoint doesn't carry
// per-verifier series or the heartbeat/token totals.
func (p sessionSummaryPayload) toSummary() Summary {
	s := Summary{
		SessionID:    p.SessionID,
		SessionFound: true,
		Partial:      true, // the list summary carries no series/heartbeat/token detail
		GoalText:     p.GoalText,
		BaseRef:      p.BaseRef,
		Worktree:     p.Worktree,
		StartedAt:    p.StartedAt,
		EditCount:    p.EditCount,
		BatchCount:   p.BatchCount,
		RunCount:     p.RunCount,
		TotalCostUSD: p.TotalCostUSD,
	}
	if p.CurrentDistance != nil {
		s.LastOverallDistance = sql.NullFloat64{Float64: *p.CurrentDistance, Valid: true}
	}
	return s
}

// getJSON performs a GET and decodes a 2xx JSON body into out. A 404 maps to
// errRemoteNotFound so callers can treat a missing resource as empty; any other
// non-2xx is an error carrying a body snippet for diagnosis. It mirrors doJSON's
// read path without the request-body handling the POST helpers need.
func getJSON(client *http.Client, token, url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errRemoteNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
