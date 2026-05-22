package telemetry

import (
	"testing"
	"time"
)

func TestLoadSummaryRollsUpAnEpisode(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	sid := NewID()

	if err := s.RecordSession(SessionRecord{
		SessionID: sid, GoalText: "ship telemetry panel", BaseRef: "abc123",
		Worktree: "/repo", StartedAt: base,
	}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := s.RecordEdit(EditRecord{SessionID: sid, FilePath: "main.go", Seq: i, TS: base}); err != nil {
			t.Fatalf("RecordEdit: %v", err)
		}
	}
	bid := NewID()
	if err := s.RecordBatch(BatchRecord{BatchID: bid, SessionID: sid, TS: base, FileCount: 1}); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	// Two verifiers, each with a descending (converging) distance series.
	clarity := []float64{0.8, 0.5, 0.2}
	tests := []float64{0.6, 0.3}
	for i, d := range clarity {
		if _, err := s.RecordVerifierRun(VerifierRunRecord{
			BatchID: bid, SessionID: sid, VerifierName: "clarity", Distance: d, Status: "ok",
			InputTokens: 100, OutputTokens: 50, CostUSD: 0.01, TS: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordVerifierRun clarity: %v", err)
		}
	}
	for i, d := range tests {
		if _, err := s.RecordVerifierRun(VerifierRunRecord{
			BatchID: bid, SessionID: sid, VerifierName: "tests", Distance: d, Status: "ok",
			InputTokens: 10, OutputTokens: 5, CostUSD: 0.002, TS: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordVerifierRun tests: %v", err)
		}
	}
	if err := s.RecordHeartbeat(HeartbeatRecord{SessionID: sid, TS: base, OverallDistance: 0.4, BatchCount: 1, EditCount: 3}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	if err := s.RecordHeartbeat(HeartbeatRecord{SessionID: sid, TS: base.Add(time.Minute), OverallDistance: 0.25, BatchCount: 1, EditCount: 3}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	sum, err := LoadSummary(s.DB(), sid)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if !sum.SessionFound {
		t.Fatal("SessionFound = false, want true")
	}
	if sum.GoalText != "ship telemetry panel" {
		t.Errorf("GoalText = %q", sum.GoalText)
	}
	if sum.EditCount != 3 || sum.BatchCount != 1 || sum.RunCount != 5 || sum.HeartbeatCount != 2 {
		t.Errorf("counts: edits=%d batches=%d runs=%d heartbeats=%d",
			sum.EditCount, sum.BatchCount, sum.RunCount, sum.HeartbeatCount)
	}
	wantCost := 3*0.01 + 2*0.002
	if sum.TotalCostUSD < wantCost-1e-9 || sum.TotalCostUSD > wantCost+1e-9 {
		t.Errorf("TotalCostUSD = %v, want %v", sum.TotalCostUSD, wantCost)
	}
	if sum.TotalTokens != 3*150+2*15 {
		t.Errorf("TotalTokens = %d, want %d", sum.TotalTokens, 3*150+2*15)
	}
	if !sum.LastOverallDistance.Valid || sum.LastOverallDistance.Float64 != 0.25 {
		t.Errorf("LastOverallDistance = %+v, want 0.25 (latest heartbeat)", sum.LastOverallDistance)
	}

	if len(sum.Verifiers) != 2 {
		t.Fatalf("Verifiers = %d series, want 2", len(sum.Verifiers))
	}
	// Sorted by name: clarity before tests.
	c := sum.Verifiers[0]
	if c.Name != "clarity" || len(c.Points) != 3 {
		t.Fatalf("series[0] = %s with %d points, want clarity with 3", c.Name, len(c.Points))
	}
	if c.Points[0].Distance != 0.8 || c.Points[2].Distance != 0.2 {
		t.Errorf("clarity points not in ts order: %+v", c.Points)
	}
	if c.Last != 0.2 || c.Min != 0.2 || c.Max != 0.8 {
		t.Errorf("clarity last/min/max = %v/%v/%v, want 0.2/0.2/0.8", c.Last, c.Min, c.Max)
	}
}

func TestLoadSummaryMissingSession(t *testing.T) {
	s := newTestStore(t)
	sum, err := LoadSummary(s.DB(), NewID())
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if sum.SessionFound {
		t.Error("SessionFound = true for an unrecorded session id")
	}
	if len(sum.Verifiers) != 0 {
		t.Errorf("Verifiers = %d, want 0 for missing session", len(sum.Verifiers))
	}
}

func TestLoadSummaryEmptyIDSkipsQuery(t *testing.T) {
	s := newTestStore(t)
	sum, err := LoadSummary(s.DB(), "")
	if err != nil {
		t.Fatalf("LoadSummary(\"\"): %v", err)
	}
	if sum.SessionFound || len(sum.Verifiers) != 0 {
		t.Errorf("empty session id should yield a zero summary, got %+v", sum)
	}
}

func TestLoadSummaryExcludesNullDistanceRuns(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	if err := s.RecordSession(SessionRecord{SessionID: sid, GoalText: "g", StartedAt: time.Now()}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	// An errored verifier that never produced a score: distance stays NULL via a
	// direct insert (RecordVerifierRun always writes a float). It must count in
	// RunCount but must not appear as a plottable series.
	if _, err := s.DB().Exec(
		`INSERT INTO verifier_run (session_id, verifier_name, distance, status, ts) VALUES (?, ?, NULL, 'error', ?)`,
		sid, "broken", time.Now().UTC(),
	); err != nil {
		t.Fatalf("insert null-distance run: %v", err)
	}
	sum, err := LoadSummary(s.DB(), sid)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if sum.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1 (null-distance run still counts)", sum.RunCount)
	}
	if len(sum.Verifiers) != 0 {
		t.Errorf("Verifiers = %d, want 0 (null-distance run is not plottable)", len(sum.Verifiers))
	}
}
