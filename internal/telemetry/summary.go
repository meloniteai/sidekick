package telemetry

import (
	"database/sql"
	"time"
)

// Summary is the executive-level rollup of one goal episode, read straight from
// the SQLite store. It is the single read model the TUI's session-telemetry
// panel renders — every number it shows comes from one of these fields, so the
// panel never has to touch in-memory daemon state (and can honestly flag what
// the database is missing).
//
// SessionFound is false when no session row exists for the requested id (the
// episode was never recorded, or telemetry was disabled when the goal was set).
// Callers should surface that explicitly rather than rendering a blank summary.
type Summary struct {
	SessionID    string
	SessionFound bool

	GoalText  string
	BaseRef   string
	Worktree  string
	StartedAt time.Time

	EditCount      int
	BatchCount     int
	RunCount       int
	HeartbeatCount int

	TotalCostUSD float64
	TotalTokens  int

	// LastOverallDistance is the convergence reading carried by the most recent
	// heartbeat. Valid is false when no heartbeat has landed yet.
	LastOverallDistance sql.NullFloat64

	// Verifiers holds one distance series per verifier that has produced at
	// least one scored run this episode, sorted by name for stable rendering.
	Verifiers []VerifierSeries
}

// VerifierSeries is the ordered distance trajectory for a single verifier
// within an episode — the x/y line the panel plots as a sparkline (run index on
// x, distance on y). Points are ordered oldest-first so the sparkline reads left
// (first run) to right (latest run).
type VerifierSeries struct {
	Name   string
	Points []DistancePoint
	Last   float64 // distance of the most recent run
	Min    float64
	Max    float64
}

// DistancePoint is one scored verifier run: when it ran and how far the work
// was from the goal (0 = achieved, 1 = far).
type DistancePoint struct {
	TS       time.Time
	Distance float64
}

// LoadSummary builds a Summary for one episode by querying the store read-only.
// It runs a handful of small aggregate queries plus one ordered scan of scored
// runs; nothing here writes, so it is safe against a live daemon writing the
// same WAL-mode database concurrently. An empty sessionID returns a zero-value
// Summary (SessionFound=false) without touching the database.
func LoadSummary(db *sql.DB, sessionID string) (Summary, error) {
	s := Summary{SessionID: sessionID}
	if sessionID == "" {
		return s, nil
	}

	// Session metadata. A missing row is not an error — it just means the
	// episode was never recorded — so ErrNoRows leaves SessionFound false.
	// base_ref/worktree are nullable columns; scan them through NullString.
	var startedAt sql.NullTime
	var baseRef, worktree sql.NullString
	err := db.QueryRow(
		`SELECT goal_text, base_ref, worktree, started_at FROM session WHERE session_id = ?`,
		sessionID,
	).Scan(&s.GoalText, &baseRef, &worktree, &startedAt)
	switch {
	case err == sql.ErrNoRows:
		// leave SessionFound = false
	case err != nil:
		return s, err
	default:
		s.SessionFound = true
		s.BaseRef = baseRef.String
		s.Worktree = worktree.String
		if startedAt.Valid {
			s.StartedAt = startedAt.Time
		}
	}

	if s.EditCount, err = countFor(db, "edit", sessionID); err != nil {
		return s, err
	}
	if s.BatchCount, err = countFor(db, "batch", sessionID); err != nil {
		return s, err
	}
	if s.RunCount, err = countFor(db, "verifier_run", sessionID); err != nil {
		return s, err
	}
	if s.HeartbeatCount, err = countFor(db, "session_heartbeat", sessionID); err != nil {
		return s, err
	}

	// Cumulative agent cost across every run in the episode.
	if err = db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0), COALESCE(SUM(input_tokens + output_tokens), 0)
		 FROM verifier_run WHERE session_id = ?`,
		sessionID,
	).Scan(&s.TotalCostUSD, &s.TotalTokens); err != nil {
		return s, err
	}

	// Latest heartbeat carries the freshest overall-distance trajectory point.
	if err = db.QueryRow(
		`SELECT overall_distance FROM session_heartbeat
		 WHERE session_id = ? ORDER BY ts DESC LIMIT 1`,
		sessionID,
	).Scan(&s.LastOverallDistance); err != nil && err != sql.ErrNoRows {
		return s, err
	}

	if s.Verifiers, err = loadVerifierSeries(db, sessionID); err != nil {
		return s, err
	}
	return s, nil
}

func countFor(db *sql.DB, table, sessionID string) (int, error) {
	// table is a package-internal constant string, never user input.
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE session_id = ?", sessionID).Scan(&n)
	return n, err
}

// loadVerifierSeries scans every scored run for the episode in (name, ts) order
// and folds them into one ordered series per verifier. Runs with a NULL distance
// (errored verifiers that never produced a score) are excluded so a sparkline
// only ever plots real readings. The result is already name-sorted because the
// query orders by verifier_name first.
func loadVerifierSeries(db *sql.DB, sessionID string) ([]VerifierSeries, error) {
	rows, err := db.Query(
		`SELECT verifier_name, ts, distance FROM verifier_run
		 WHERE session_id = ? AND distance IS NOT NULL
		 ORDER BY verifier_name, ts`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VerifierSeries
	var cur *VerifierSeries
	for rows.Next() {
		var name string
		var ts sql.NullTime
		var dist float64
		if err := rows.Scan(&name, &ts, &dist); err != nil {
			return nil, err
		}
		if cur == nil || cur.Name != name {
			out = append(out, VerifierSeries{Name: name, Min: dist, Max: dist})
			cur = &out[len(out)-1]
		}
		cur.Points = append(cur.Points, DistancePoint{TS: ts.Time, Distance: dist})
		cur.Last = dist
		if dist < cur.Min {
			cur.Min = dist
		}
		if dist > cur.Max {
			cur.Max = dist
		}
	}
	return out, rows.Err()
}
