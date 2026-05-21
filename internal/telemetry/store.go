package telemetry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schema is the full DDL applied idempotently on Open. Five entities plus the
// indexes the by-hand analysis queries lean on (session, file, batch joins).
const schema = `
CREATE TABLE IF NOT EXISTS session (
	session_id TEXT PRIMARY KEY,
	goal_text  TEXT NOT NULL,
	goal_class TEXT,
	base_ref   TEXT,
	worktree   TEXT,
	agent_kind TEXT,
	started_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS edit (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL,
	file_path  TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	ts         TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS batch (
	batch_id   TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	ts         TIMESTAMP NOT NULL,
	file_set   TEXT,
	file_count INTEGER NOT NULL,
	base_ref   TEXT
);

CREATE TABLE IF NOT EXISTS verifier_run (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	batch_id         TEXT,
	session_id       TEXT NOT NULL,
	verifier_name    TEXT NOT NULL,
	verifier_version TEXT,
	distance         REAL,
	reason           TEXT,
	status           TEXT,
	duration_ms      INTEGER,
	input_tokens     INTEGER,
	output_tokens    INTEGER,
	cache_reads      INTEGER,
	cache_writes     INTEGER,
	cost_usd         REAL,
	ts               TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS session_heartbeat (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id       TEXT NOT NULL,
	ts               TIMESTAMP NOT NULL,
	overall_distance REAL,
	batch_count      INTEGER,
	edit_count       INTEGER
);

CREATE INDEX IF NOT EXISTS idx_edit_session  ON edit(session_id);
CREATE INDEX IF NOT EXISTS idx_batch_session ON batch(session_id);
CREATE INDEX IF NOT EXISTS idx_vrun_batch    ON verifier_run(batch_id);
CREATE INDEX IF NOT EXISTS idx_vrun_session  ON verifier_run(session_id);
CREATE INDEX IF NOT EXISTS idx_hb_session    ON session_heartbeat(session_id);
`

// Store is a SQLite-backed Emitter. The daemon is the single writer, so we
// keep one connection (MaxOpenConns(1)) which serialises the concurrent emits
// a fan-out batch produces without per-call locking.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the
// schema. The parent directory is created. WAL mode keeps a concurrent
// read-only `sidekick export` from blocking the daemon's writes.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("telemetry: mkdir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("telemetry: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("telemetry: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenReadOnly opens an existing database for querying (export). It does not
// create the file or apply the schema; a missing file surfaces as an error
// from the first query.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("telemetry: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

// DB exposes the underlying handle for read-only export queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) RecordSession(r SessionRecord) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO session
		 (session_id, goal_text, goal_class, base_ref, worktree, agent_kind, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.GoalText, nullStr(r.GoalClass), nullStr(r.BaseRef),
		nullStr(r.Worktree), nullStr(r.AgentKind), r.StartedAt.UTC(),
	)
	return err
}

func (s *Store) RecordEdit(r EditRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO edit (session_id, file_path, seq, ts) VALUES (?, ?, ?, ?)`,
		r.SessionID, r.FilePath, r.Seq, r.TS.UTC(),
	)
	return err
}

func (s *Store) RecordBatch(r BatchRecord) error {
	var fileSet any
	if len(r.FileSet) > 0 {
		if b, err := json.Marshal(r.FileSet); err == nil {
			fileSet = string(b)
		}
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO batch (batch_id, session_id, ts, file_set, file_count, base_ref)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.BatchID, r.SessionID, r.TS.UTC(), fileSet, r.FileCount, nullStr(r.BaseRef),
	)
	return err
}

func (s *Store) RecordVerifierRun(r VerifierRunRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO verifier_run
		 (batch_id, session_id, verifier_name, verifier_version, distance, reason,
		  status, duration_ms, input_tokens, output_tokens, cache_reads, cache_writes, cost_usd, ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullStr(r.BatchID), r.SessionID, r.VerifierName, nullStr(r.VerifierVersion),
		r.Distance, r.Reason, nullStr(r.Status), r.DurationMS,
		r.InputTokens, r.OutputTokens, r.CacheReads, r.CacheWrites, r.CostUSD, r.TS.UTC(),
	)
	return err
}

func (s *Store) RecordHeartbeat(r HeartbeatRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO session_heartbeat (session_id, ts, overall_distance, batch_count, edit_count)
		 VALUES (?, ?, ?, ?, ?)`,
		r.SessionID, r.TS.UTC(), r.OverallDistance, r.BatchCount, r.EditCount,
	)
	return err
}

// nullStr maps "" to a SQL NULL so nullable columns stay genuinely null rather
// than storing empty strings the analysis would have to special-case.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
