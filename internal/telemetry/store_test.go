package telemetry

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreRecordsEveryEntity(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	sid := NewID()
	if err := s.RecordSession(SessionRecord{
		SessionID: sid, GoalText: "ship the thing", BaseRef: "abc123",
		Worktree: "/repo", StartedAt: now,
	}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	if err := s.RecordEdit(EditRecord{SessionID: sid, FilePath: "main.go", Seq: 1, TS: now}); err != nil {
		t.Fatalf("RecordEdit: %v", err)
	}
	bid := NewID()
	if err := s.RecordBatch(BatchRecord{
		BatchID: bid, SessionID: sid, TS: now,
		FileSet: []string{"main.go", "util.go"}, FileCount: 2, BaseRef: "abc123",
	}); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	runID, err := s.RecordVerifierRun(VerifierRunRecord{
		BatchID: bid, SessionID: sid, VerifierName: "Architect", VerifierVersion: "deadbeef",
		Distance: 0.25, Reason: "looks good", Status: "ok", DurationMS: 1200,
		InputTokens: 10, OutputTokens: 20, CostUSD: 0.01, TS: now,
	})
	if err != nil {
		t.Fatalf("RecordVerifierRun: %v", err)
	}
	if err := s.RecordFindings(runID, []FindingRecord{
		{SessionID: sid, BatchID: bid, VerifierName: "Architect", FilePath: "main.go",
			Distance: 0.25, Reason: "looks good", TS: now},
	}); err != nil {
		t.Fatalf("RecordFindings: %v", err)
	}
	if err := s.RecordHeartbeat(HeartbeatRecord{
		SessionID: sid, TS: now, OverallDistance: 0.3, BatchCount: 1, EditCount: 1,
	}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	var buf bytes.Buffer
	if err := DumpJSON(s.DB(), &buf); err != nil {
		t.Fatalf("DumpJSON: %v", err)
	}
	var dump map[string][]map[string]any
	if err := json.Unmarshal(buf.Bytes(), &dump); err != nil {
		t.Fatalf("unmarshal dump: %v", err)
	}
	for _, table := range Tables {
		if len(dump[table]) != 1 {
			t.Errorf("table %s: got %d rows, want 1", table, len(dump[table]))
		}
	}

	// file_set round-trips as a JSON array string; reason is preserved verbatim.
	if got := dump["batch"][0]["file_set"]; got != `["main.go","util.go"]` {
		t.Errorf("batch.file_set = %v, want JSON array", got)
	}
	if got := dump["verifier_run"][0]["reason"]; got != "looks good" {
		t.Errorf("verifier_run.reason = %v", got)
	}
}

func TestRecordEditCountsRepeatTouches(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	for i := 1; i <= 3; i++ {
		if err := s.RecordEdit(EditRecord{SessionID: sid, FilePath: "main.go", Seq: i, TS: time.Now()}); err != nil {
			t.Fatalf("RecordEdit %d: %v", i, err)
		}
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM edit WHERE file_path = 'main.go'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("repeat touches: got %d rows, want 3 (no dedup at the storage layer)", n)
	}
}

func TestNullableColumnsStoreNull(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	// goal_class, agent_kind left empty → should be SQL NULL, not "".
	if err := s.RecordSession(SessionRecord{SessionID: sid, GoalText: "g", StartedAt: time.Now()}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	var goalClass, agentKind *string
	if err := s.DB().QueryRow(
		`SELECT goal_class, agent_kind FROM session WHERE session_id = ?`, sid,
	).Scan(&goalClass, &agentKind); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if goalClass != nil || agentKind != nil {
		t.Fatalf("nullable cols not null: goal_class=%v agent_kind=%v", goalClass, agentKind)
	}
}

func TestBatchlessVerifierRun(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	// Single ("r"-key) runs carry no batch — empty BatchID must persist as NULL.
	if _, err := s.RecordVerifierRun(VerifierRunRecord{
		SessionID: sid, VerifierName: "Test", Distance: 0.5, Status: "ok", TS: time.Now(),
	}); err != nil {
		t.Fatalf("RecordVerifierRun: %v", err)
	}
	var batchID *string
	if err := s.DB().QueryRow(`SELECT batch_id FROM verifier_run WHERE session_id = ?`, sid).Scan(&batchID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if batchID != nil {
		t.Fatalf("batch_id = %v, want NULL for a single run", *batchID)
	}
}

// The finding table + insert carry the two anchor columns; empty
// hashes (tree-global / no-hunk findings) persist as SQL NULL.
func TestRecordFindingsHashes(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	runID, err := s.RecordVerifierRun(VerifierRunRecord{
		SessionID: sid, VerifierName: "A", Distance: 0.5, Status: "ok", TS: time.Now(),
	})
	if err != nil {
		t.Fatalf("RecordVerifierRun: %v", err)
	}
	now := time.Now()
	if err := s.RecordFindings(runID, []FindingRecord{
		{SessionID: sid, VerifierName: "A", FilePath: "f.go", Line: 3, Distance: 0.5,
			HunkHash: "becae935df3b19d6", DirtyDiffHash: "becae935df3b19d6", TS: now},
		// tree-global finding: empty hashes -> NULL.
		{SessionID: sid, VerifierName: "A", Distance: 0.5, TS: now},
	}); err != nil {
		t.Fatalf("RecordFindings: %v", err)
	}

	var hunk, dirty *string
	if err := s.DB().QueryRow(
		`SELECT hunk_hash, dirty_diff_hash FROM finding WHERE file_path = 'f.go'`,
	).Scan(&hunk, &dirty); err != nil {
		t.Fatalf("scan line finding: %v", err)
	}
	if hunk == nil || *hunk != "becae935df3b19d6" {
		t.Fatalf("hunk_hash = %v, want becae935df3b19d6", hunk)
	}
	if dirty == nil || *dirty != "becae935df3b19d6" {
		t.Fatalf("dirty_diff_hash = %v", dirty)
	}

	var ghunk, gdirty *string
	if err := s.DB().QueryRow(
		`SELECT hunk_hash, dirty_diff_hash FROM finding WHERE file_path IS NULL`,
	).Scan(&ghunk, &gdirty); err != nil {
		t.Fatalf("scan tree-global finding: %v", err)
	}
	if ghunk != nil || gdirty != nil {
		t.Fatalf("tree-global finding should store NULL hashes: hunk=%v dirty=%v", ghunk, gdirty)
	}
}

func TestDumpCSV(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	if err := s.RecordSession(SessionRecord{SessionID: sid, GoalText: "ship", StartedAt: time.Now()}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	var buf bytes.Buffer
	if err := DumpCSV(s.DB(), "session", &buf); err != nil {
		t.Fatalf("DumpCSV: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "session_id,goal_text,goal_class,base_ref,worktree,agent_kind,started_at") {
		t.Fatalf("csv header missing/unexpected: %q", out)
	}
	if !strings.Contains(out, "ship") {
		t.Fatalf("csv missing data row: %q", out)
	}

	if err := DumpCSV(s.DB(), "no_such_table", &buf); err == nil {
		t.Fatal("DumpCSV accepted an unknown table name")
	}
}

func TestConcurrentVerifierRuns(t *testing.T) {
	s := newTestStore(t)
	sid := NewID()
	bid := NewID()
	// A batch fans out to parallel verifier goroutines that each emit a run;
	// the store must serialise these without error or data loss.
	const n = 20
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := s.RecordVerifierRun(VerifierRunRecord{
				BatchID: bid, SessionID: sid, VerifierName: "V", Distance: 0.5, Status: "ok", TS: time.Now(),
			}); err != nil {
				t.Errorf("concurrent RecordVerifierRun: %v", err)
			}
		})
	}
	wg.Wait()

	var got int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM verifier_run WHERE batch_id = ?`, bid).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("concurrent runs persisted = %d, want %d", got, n)
	}
}

func TestRecordFindings(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	sid := NewID()
	bid := NewID()

	runID, err := s.RecordVerifierRun(VerifierRunRecord{
		BatchID: bid, SessionID: sid, VerifierName: "architect", Distance: 0.75,
		Reason: "blocking", Status: "ok", TS: now,
	})
	if err != nil {
		t.Fatalf("RecordVerifierRun: %v", err)
	}
	if runID == 0 {
		t.Fatal("RecordVerifierRun returned id 0; findings can't reference it")
	}

	// One file-attributed finding and one tree-global finding (empty path).
	if err := s.RecordFindings(runID, []FindingRecord{
		{SessionID: sid, BatchID: bid, VerifierName: "architect", FilePath: "internal/a.go",
			Symbol: "Foo", Line: 12, Distance: 0.75, Reason: "blocking", TS: now},
		{SessionID: sid, BatchID: bid, VerifierName: "architect", FilePath: "",
			Distance: 0.5, Reason: "suite is shaky", TS: now},
	}); err != nil {
		t.Fatalf("RecordFindings: %v", err)
	}

	// Both rows reference the run; the global finding stores file_path/line as NULL.
	var fileFinding struct {
		runID    int64
		filePath *string
		symbol   *string
		line     *int
		distance float64
	}
	if err := s.DB().QueryRow(
		`SELECT verifier_run_id, file_path, symbol, line, distance FROM finding WHERE file_path = 'internal/a.go'`,
	).Scan(&fileFinding.runID, &fileFinding.filePath, &fileFinding.symbol, &fileFinding.line, &fileFinding.distance); err != nil {
		t.Fatalf("scan file finding: %v", err)
	}
	if fileFinding.runID != runID {
		t.Fatalf("finding.verifier_run_id = %d, want %d", fileFinding.runID, runID)
	}
	if fileFinding.filePath == nil || *fileFinding.filePath != "internal/a.go" || fileFinding.line == nil || *fileFinding.line != 12 {
		t.Fatalf("file finding columns wrong: %+v", fileFinding)
	}

	var globalPath *string
	var globalLine *int
	if err := s.DB().QueryRow(
		`SELECT file_path, line FROM finding WHERE distance = 0.5`,
	).Scan(&globalPath, &globalLine); err != nil {
		t.Fatalf("scan global finding: %v", err)
	}
	if globalPath != nil {
		t.Fatalf("tree-global finding file_path = %q, want NULL", *globalPath)
	}
	if globalLine != nil {
		t.Fatalf("zero line = %d, want NULL", *globalLine)
	}

	// Empty findings is a no-op, not an error.
	if err := s.RecordFindings(runID, nil); err != nil {
		t.Fatalf("RecordFindings(nil): %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM finding`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("finding rows = %d, want 2", count)
	}
}

func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.db")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sid := NewID()
	if err := w.RecordSession(SessionRecord{SessionID: sid, GoalText: "g", StartedAt: time.Now()}); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	w.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	var n int
	if err := ro.DB().QueryRow(`SELECT COUNT(*) FROM session`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("read-only count = %d, want 1", n)
	}
}
