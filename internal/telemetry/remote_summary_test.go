package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteFetchSummaryMapsListElement(t *testing.T) {
	dist := 0.42
	sessions := []map[string]any{
		{"session_id": "other", "goal_text": "x", "started_at": "2026-05-22T10:00:00Z"},
		{
			"session_id": "S1", "goal_text": "ship it",
			"base_ref": "abc", "worktree": "/repo",
			"started_at": "2026-05-22T10:00:00Z",
			"edit_count": 3, "batch_count": 2, "verifier_run_count": 5,
			"finding_count": 1, "total_cost_usd": 0.12, "current_distance": dist,
			"last_activity_at": "2026-05-22T10:05:00Z",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sessions") {
			writeJSON(w, 200, sessions)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := &RemoteEmitter{client: srv.Client(), base: srv.URL + "/api/projects/PID"}
	got, err := e.FetchSummary("S1")
	if err != nil {
		t.Fatalf("FetchSummary: %v", err)
	}
	if !got.SessionFound {
		t.Fatalf("SessionFound = false, want true")
	}
	if got.GoalText != "ship it" || got.BaseRef != "abc" || got.Worktree != "/repo" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.EditCount != 3 || got.BatchCount != 2 || got.RunCount != 5 {
		t.Fatalf("counts = %d/%d/%d, want 3/2/5", got.EditCount, got.BatchCount, got.RunCount)
	}
	if got.TotalCostUSD != 0.12 {
		t.Fatalf("cost = %v, want 0.12", got.TotalCostUSD)
	}
	if !got.LastOverallDistance.Valid || got.LastOverallDistance.Float64 != dist {
		t.Fatalf("distance = %+v, want valid %v", got.LastOverallDistance, dist)
	}
	// The list summary carries no per-verifier series, heartbeat count or tokens.
	if len(got.Verifiers) != 0 || got.HeartbeatCount != 0 || got.TotalTokens != 0 {
		t.Fatalf("expected no series/beats/tokens from list, got %+v", got)
	}
}

func TestRemoteFetchSummaryMissingSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []map[string]any{}) // project exists, session not posted yet
	}))
	defer srv.Close()

	e := &RemoteEmitter{client: srv.Client(), base: srv.URL + "/api/projects/PID"}
	got, err := e.FetchSummary("S1")
	if err != nil {
		t.Fatalf("FetchSummary: %v", err)
	}
	if got.SessionFound {
		t.Fatalf("SessionFound = true, want false for an absent session")
	}
	if got.SessionID != "S1" {
		t.Fatalf("SessionID = %q, want S1", got.SessionID)
	}
}

func TestRemoteFetchSummaryProjectNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // project not created yet
	}))
	defer srv.Close()

	e := &RemoteEmitter{client: srv.Client(), base: srv.URL + "/api/projects/PID"}
	got, err := e.FetchSummary("S1")
	if err != nil {
		t.Fatalf("FetchSummary on 404 should not error: %v", err)
	}
	if got.SessionFound {
		t.Fatalf("a 404 should map to SessionFound=false, not an error")
	}
}

func TestRemoteFetchSummaryEmptySessionID(t *testing.T) {
	e := &RemoteEmitter{client: http.DefaultClient, base: "http://127.0.0.1:1/api/projects/PID"}
	got, err := e.FetchSummary("")
	if err != nil {
		t.Fatalf("empty sessionID must not error or hit the network: %v", err)
	}
	if got.SessionFound || got.SessionID != "" {
		t.Fatalf("empty sessionID should return a zero Summary, got %+v", got)
	}
}
