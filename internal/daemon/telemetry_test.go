package daemon

import (
	"sync"
	"testing"

	"github.com/meloniteai/sidekick/internal/telemetry"
)

// fakeEmitter records calls in memory so State wiring can be asserted without
// touching SQLite.
type fakeEmitter struct {
	mu         sync.Mutex
	sessions   []telemetry.SessionRecord
	edits      []telemetry.EditRecord
	heartbeats []telemetry.HeartbeatRecord
	runID      int64
}

func (f *fakeEmitter) RecordSession(r telemetry.SessionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, r)
	return nil
}
func (f *fakeEmitter) RecordEdit(r telemetry.EditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, r)
	return nil
}
func (f *fakeEmitter) RecordBatch(telemetry.BatchRecord) error { return nil }
func (f *fakeEmitter) RecordVerifierRun(telemetry.VerifierRunRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runID++
	return f.runID, nil
}
func (f *fakeEmitter) RecordFindings(int64, []telemetry.FindingRecord) error {
	return nil
}
func (f *fakeEmitter) RecordHeartbeat(r telemetry.HeartbeatRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, r)
	return nil
}
func (f *fakeEmitter) Close() error { return nil }

func TestTelemetryNoopWhenEmitterNil(t *testing.T) {
	s := NewState()
	// No emitter set: every telemetry method must be a safe no-op.
	s.StartTelemetrySession("goal")
	s.RecordTelemetryEdit("main.go")
	s.IncTelemetryBatchCount()
	s.EmitHeartbeat()
	if id := s.TelemetrySessionID(); id != "" {
		t.Fatalf("TelemetrySessionID = %q, want empty with nil emitter", id)
	}
}

func TestStartTelemetrySessionMintsIDAndEmits(t *testing.T) {
	f := &fakeEmitter{}
	s := NewState()
	s.SetSessionBaseRef("abc123")
	s.SetSessionWorktree("/repo")
	s.SetEmitter(f)

	s.StartTelemetrySession("first goal")
	first := s.TelemetrySessionID()
	if first == "" {
		t.Fatal("session id not minted")
	}
	s.StartTelemetrySession("second goal")
	second := s.TelemetrySessionID()
	if second == first {
		t.Fatal("a new goal episode must mint a fresh session id")
	}
	if len(f.sessions) != 2 {
		t.Fatalf("emitted %d sessions, want 2", len(f.sessions))
	}
	if f.sessions[0].BaseRef != "abc123" || f.sessions[0].Worktree != "/repo" {
		t.Fatalf("session record missing anchor: %+v", f.sessions[0])
	}
	if len(f.heartbeats) != 2 {
		t.Fatalf("initial heartbeats = %d, want 2 (one per goal episode)", len(f.heartbeats))
	}
	if f.heartbeats[0].SessionID != first || f.heartbeats[1].SessionID != second {
		t.Fatalf("initial heartbeats not tied to their episodes: %+v", f.heartbeats)
	}
}

func TestRecordTelemetryEditCountsAndSequences(t *testing.T) {
	f := &fakeEmitter{}
	s := NewState()
	s.SetEmitter(f)

	// Edits before a goal episode belong to no session and are dropped.
	s.RecordTelemetryEdit("early.go")
	if len(f.edits) != 0 {
		t.Fatalf("edit recorded before goal set: %d", len(f.edits))
	}

	s.StartTelemetrySession("goal")
	s.RecordTelemetryEdit("main.go")
	s.RecordTelemetryEdit("main.go") // repeat touch must still count
	s.RecordTelemetryEdit("")        // empty path dropped

	if len(f.edits) != 2 {
		t.Fatalf("edits = %d, want 2 (repeat counted, empty dropped)", len(f.edits))
	}
	if f.edits[0].Seq != 1 || f.edits[1].Seq != 2 {
		t.Fatalf("seq not monotonic: %d, %d", f.edits[0].Seq, f.edits[1].Seq)
	}
}

func TestRecordTelemetryEditNormalizesPath(t *testing.T) {
	f := &fakeEmitter{}
	s := NewState()
	s.SetSessionWorktree("/repo/.claude/worktrees/wt")
	s.SetEmitter(f)
	s.StartTelemetrySession("goal")

	// Absolute worktree path -> repo-relative, so it shares one key with the
	// finding stream (which already normalizes this way).
	s.RecordTelemetryEdit("/repo/.claude/worktrees/wt/internal/sidekick/model.go")
	// Already-relative path is preserved.
	s.RecordTelemetryEdit("cmd/start.go")
	// Path outside the worktree can't be anchored: keep the raw path rather than
	// drop the edit (an edit is always to a real file).
	s.RecordTelemetryEdit("/elsewhere/util.go")

	if len(f.edits) != 3 {
		t.Fatalf("edits = %d, want 3", len(f.edits))
	}
	want := []string{"internal/sidekick/model.go", "cmd/start.go", "/elsewhere/util.go"}
	for i, w := range want {
		if f.edits[i].FilePath != w {
			t.Errorf("edit[%d].FilePath = %q, want %q", i, f.edits[i].FilePath, w)
		}
	}
}

func TestEmitHeartbeatCarriesCounts(t *testing.T) {
	f := &fakeEmitter{}
	s := NewState()
	s.SetEmitter(f)
	s.StartTelemetrySession("goal")
	s.RecordTelemetryEdit("a.go")
	s.IncTelemetryBatchCount()
	s.IncTelemetryBatchCount()

	s.EmitHeartbeat()
	if len(f.heartbeats) != 2 {
		t.Fatalf("heartbeats = %d, want 2 (initial + changed)", len(f.heartbeats))
	}
	hb := f.heartbeats[1]
	if hb.EditCount != 1 || hb.BatchCount != 2 {
		t.Fatalf("heartbeat counts: edits=%d batches=%d, want 1/2", hb.EditCount, hb.BatchCount)
	}

	// A fresh goal episode resets the counters.
	s.StartTelemetrySession("next goal")
	last := f.heartbeats[len(f.heartbeats)-1]
	if last.EditCount != 0 || last.BatchCount != 0 {
		t.Fatalf("counters not reset on new episode: edits=%d batches=%d", last.EditCount, last.BatchCount)
	}
}

func TestEmitHeartbeatSkipsUnchangedSamples(t *testing.T) {
	f := &fakeEmitter{}
	s := NewState()
	s.SetEmitter(f)
	s.StartTelemetrySession("goal")

	// StartTelemetrySession writes the first sample of an episode; idle ticks
	// with no new edits/batches/distance must not append duplicate rows.
	s.EmitHeartbeat()
	s.EmitHeartbeat()
	s.EmitHeartbeat()
	if len(f.heartbeats) != 1 {
		t.Fatalf("idle heartbeats not gated: got %d rows, want 1", len(f.heartbeats))
	}

	// A new edit changes the sample, so the next tick writes again; a further
	// idle tick is gated once more.
	s.RecordTelemetryEdit("a.go")
	s.EmitHeartbeat()
	s.EmitHeartbeat()
	if len(f.heartbeats) != 2 {
		t.Fatalf("changed sample not recorded: got %d rows, want 2", len(f.heartbeats))
	}
	if got := f.heartbeats[1].EditCount; got != 1 {
		t.Fatalf("second heartbeat edit count = %d, want 1", got)
	}

	// A fresh episode always writes its first sample, even though its counts
	// and distance match a previously written sample.
	s.StartTelemetrySession("next goal")
	s.EmitHeartbeat()
	if len(f.heartbeats) != 3 {
		t.Fatalf("new episode first heartbeat not recorded: got %d rows, want 3", len(f.heartbeats))
	}
}
