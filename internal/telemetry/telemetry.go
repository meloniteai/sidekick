// Package telemetry is the durable observability layer for Sidekick sessions.
//
// Every goal episode, file edit, verifier batch, and verifier run is recorded
// as a structured row so a single repo's friction can be analysed by hand.
// The package deliberately does nothing with the data beyond storing and
// dumping it: there is no scoring, no actor, no remote sink (Phase 1 MVP).
//
// Emitter is the single seam producers write through. The daemon wires a
// SQLite-backed [Store] today; a future control-plane sink can implement the
// same interface without touching the call sites that produce events.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Emitter is the write seam every telemetry producer uses. Implementations
// must be safe for concurrent use: a single batch fans out to parallel
// verifier goroutines that each emit a run. All methods return an error for
// the caller to log; producers never block on or fail because of telemetry.
type Emitter interface {
	RecordSession(SessionRecord) error
	RecordEdit(EditRecord) error
	RecordBatch(BatchRecord) error
	// RecordVerifierRun returns the new run's row id so its findings can
	// reference it (the finding table is a child of verifier_run).
	RecordVerifierRun(VerifierRunRecord) (int64, error)
	RecordFindings(runID int64, findings []FindingRecord) error
	RecordHeartbeat(HeartbeatRecord) error
	Close() error
}

// SessionRecord is one goal episode: opened when a goal is set and bounded by
// the next goal set (or heartbeat-derived idle). GoalClass and AgentKind are
// nullable for now — taxonomy and agent detection are deferred.
type SessionRecord struct {
	SessionID string
	GoalText  string
	GoalClass string
	BaseRef   string
	Worktree  string
	AgentKind string
	StartedAt time.Time
}

// EditRecord is one file touch, captured before the daemon's in-memory dedup
// so repeated touches of the same file are countable. Seq is monotonic within
// a session.
type EditRecord struct {
	SessionID string
	FilePath  string
	Seq       int
	TS        time.Time
}

// BatchRecord is one verifier batch (a runBatch). It is the join point that
// attributes a verifier's distance to the files that triggered it. BaseRef is
// inherited from the session — the working tree is dirty mid-session, so the
// goal's anchor HEAD is the stable reference.
type BatchRecord struct {
	BatchID   string
	SessionID string
	TS        time.Time
	FileSet   []string
	FileCount int
	BaseRef   string
}

// VerifierRunRecord is one verifier execution within a batch. BatchID is empty
// for single ("r"-key) runs that carry no batch/file context. Reason is
// first-class data: the natural-language record of why a distance was high.
type VerifierRunRecord struct {
	BatchID         string
	SessionID       string
	VerifierName    string
	VerifierVersion string
	Distance        float64
	Reason          string
	Status          string
	DurationMS      int64
	InputTokens     int
	OutputTokens    int
	CacheReads      int
	CacheWrites     int
	CostUSD         float64
	TS              time.Time
}

// FindingRecord is one attributed unit of distance, a child of a verifier_run.
// FilePath is "" for a tree-global finding (stored as NULL so "global friction"
// stays distinguishable from "no friction"); Symbol and Line are optional.
type FindingRecord struct {
	SessionID    string
	BatchID      string
	VerifierName string
	FilePath     string // "" -> stored as NULL
	Symbol       string
	Line         int
	Distance     float64
	Reason       string
	TS           time.Time
}

// HeartbeatRecord is a periodic liveness sample per live session. The end of a
// session is derived from the last heartbeat (now − last > grace), so a daemon
// crash loses nothing. OverallDistance carries the convergence trajectory.
type HeartbeatRecord struct {
	SessionID       string
	TS              time.Time
	OverallDistance float64
	BatchCount      int
	EditCount       int
}

// NewID returns a random 128-bit identifier as 32 hex chars. Used for session
// and batch ids so producers can mint ids without a round-trip to the store
// (keeping the Emitter seam free of autoincrement semantics).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read never returns an error on supported platforms; fall back
		// to a timestamp-derived id rather than panicking in a producer path.
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
