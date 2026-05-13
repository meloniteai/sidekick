// Package daemon owns the long-running session state behind `hud start`.
package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	charmlog "github.com/charmbracelet/log"
	"github.com/muesli/termenv"
	"github.com/uriahlevy/hud/internal/ipc"
)

// EventLevel categorises an entry in the in-memory event log. Renderers use
// it to colour rows; callers use it to label severity.
type EventLevel string

const (
	EventInfo  EventLevel = "info"
	EventError EventLevel = "error"
)

// EventEntry is one row in the event log surfaced by the TUI's toggleable
// log panel. Stored in-memory only; persistence is out of scope for now.
//
// Rendered holds the pre-styled single-line representation produced by the
// charmbracelet/log logger that owns this entry. The TUI consumes Rendered
// directly so it doesn't have to re-derive timestamps and level badges.
type EventEntry struct {
	At       time.Time
	Level    EventLevel
	Msg      string
	Rendered string
}

// eventBufferCap bounds the per-session log so a long-running daemon does
// not grow without bound. Oldest entries fall off the front.
const eventBufferCap = 500

// State is the in-memory snapshot of an active session. It is the single
// source of truth that the TUI renders, the MCP server reads, and the
// hook handlers mutate.
type State struct {
	mu              sync.RWMutex
	goal            string
	sessionBaseRef  string
	sessionWorktree string
	verifiers       map[string]ipc.VerifierStatus
	order          []string
	version        string
	lastSocketAt   time.Time
	lastMCPAt      time.Time
	events         []EventEntry
	// sessionEdits is the set of file paths reported via OnWrite this
	// session. Stored as insertion-ordered to keep the per-file panel
	// rendering stable across renders.
	sessionEdits      map[string]struct{}
	sessionEditsOrder []string

	logger     *charmlog.Logger
	logSink    *eventLogSink
	pending    EventEntry
	hasPending bool
}

// NewState returns a zeroed State.
func NewState() *State {
	s := &State{
		verifiers:    map[string]ipc.VerifierStatus{},
		sessionEdits: map[string]struct{}{},
	}
	s.logSink = &eventLogSink{state: s}
	s.logger = charmlog.NewWithOptions(s.logSink, charmlog.Options{
		ReportTimestamp: true,
		TimeFormat:      "15:04:05",
	})
	// charm/log probes the writer for tty support to decide whether to emit
	// ANSI escapes. Our sink is a plain io.Writer, so without this override
	// the rendered lines would arrive at the TUI as bare text.
	s.logger.SetColorProfile(termenv.TrueColor)
	s.logger.SetStyles(eventLogStyles())
	return s
}

// Logger exposes the charmbracelet/log instance backing the event log so
// callers can attach structured fields (logger.With(...)). LogEvent remains
// the convenient API for the common printf-style cases.
func (s *State) Logger() *charmlog.Logger {
	return s.logger
}

// RecordEdit registers a file path as touched in this session. Empty paths
// are dropped (Codex hook payloads occasionally fire without a path); repeat
// paths are de-duplicated.
func (s *State) RecordEdit(file string) {
	if file == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionEdits == nil {
		s.sessionEdits = map[string]struct{}{}
	}
	if _, seen := s.sessionEdits[file]; seen {
		return
	}
	s.sessionEdits[file] = struct{}{}
	s.sessionEditsOrder = append(s.sessionEditsOrder, file)
}

// SessionEdits returns the insertion-ordered list of files seen via
// RecordEdit. The slice is a copy; callers may mutate it freely.
func (s *State) SessionEdits() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.sessionEditsOrder))
	copy(out, s.sessionEditsOrder)
	return out
}

// SetGoal replaces the active goal.
func (s *State) SetGoal(goal string) {
	s.mu.Lock()
	s.goal = goal
	s.mu.Unlock()
}

// Goal returns a copy of the current goal.
func (s *State) Goal() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.goal
}

// SetSessionBaseRef records the git SHA HEAD pointed at when the session
// was anchored. Verifiers diff the working tree against this ref to
// evaluate cumulative session work, not just the latest write.
func (s *State) SetSessionBaseRef(ref string) {
	s.mu.Lock()
	s.sessionBaseRef = ref
	s.mu.Unlock()
}

// SessionBaseRef returns the captured session base ref, or "" if unset.
func (s *State) SessionBaseRef() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionBaseRef
}

// SetSessionWorktree records the absolute path to the git worktree the
// session is anchored against. Verifier subprocesses run with this as
// their working directory so `git diff $SESSION_BASE_REF` evaluates the
// right tree regardless of where `hud start` was launched.
func (s *State) SetSessionWorktree(path string) {
	s.mu.Lock()
	s.sessionWorktree = path
	s.mu.Unlock()
}

// SessionWorktree returns the anchored worktree path, or "" if unset.
func (s *State) SessionWorktree() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionWorktree
}

// SetVersion records the daemon binary version string for the header.
func (s *State) SetVersion(v string) {
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
}

// MarkSocketActivity timestamps the most recent socket request. If isMCP is
// true (i.e. Request.Source == ipc.SourceMCP) the MCP-specific timestamp is
// also bumped so the TUI header can distinguish hook/CLI traffic from agent
// MCP traffic.
func (s *State) MarkSocketActivity(isMCP bool) {
	now := time.Now()
	s.mu.Lock()
	s.lastSocketAt = now
	if isMCP {
		s.lastMCPAt = now
	}
	s.mu.Unlock()
}

// UpsertVerifier registers or updates a verifier's status entry.
func (s *State) UpsertVerifier(v ipc.VerifierStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.verifiers[v.Name]; !seen {
		s.order = append(s.order, v.Name)
	}
	s.verifiers[v.Name] = v
}

// ReplaceVerifiers swaps the configured verifier set while preserving runtime
// status for same-named verifiers. This is used when hud.yaml is edited from
// the TUI and reloaded without restarting HUD. Preserved scores are marked
// stale so clients can render them as not-yet-revalidated.
func (s *State) ReplaceVerifiers(verifiers []ipc.VerifierStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]ipc.VerifierStatus, len(verifiers))
	order := make([]string, 0, len(verifiers))
	for _, v := range verifiers {
		if prev, ok := s.verifiers[v.Name]; ok {
			v.Distance = prev.Distance
			v.Reason = prev.Reason
			v.ComputedAt = prev.ComputedAt
			v.Running = prev.Running
			v.Disabled = prev.Disabled
			v.History = prev.History
			v.LastUsage = prev.LastUsage
			// Preserved score predates the new config; mark stale unless the
			// verifier was explicitly disabled.
			if prev.Disabled {
				v.Status = ipc.StatusDisabled
			} else if prev.Status == ipc.StatusPending || prev.Status == "" {
				v.Status = ipc.StatusPending
			} else {
				v.Status = ipc.StatusStale
			}
		}
		next[v.Name] = v
		order = append(order, v.Name)
	}
	s.verifiers = next
	s.order = order
}

// MarkRunning toggles the per-verifier "running" flag, useful for TUI feedback.
func (s *State) MarkRunning(name string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verifiers[name]
	if !ok {
		return
	}
	if v.Disabled && running {
		return
	}
	v.Running = running
	if !running {
		v.ComputedAt = time.Now()
	}
	s.verifiers[name] = v
}

// SetVerifierDisabled controls whether a verifier participates in rendering
// and future runner batches. The row remains in State so users can re-enable
// it from the footer without restarting HUD.
func (s *State) SetVerifierDisabled(name string, disabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verifiers[name]
	if !ok {
		return false
	}
	applyDisable(&v, disabled)
	s.verifiers[name] = v
	return true
}

// ToggleVerifierDisabled flips a verifier's disabled state and returns the
// resulting value. ok is false when no verifier by that name exists.
func (s *State) ToggleVerifierDisabled(name string) (disabled bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verifiers[name]
	if !ok {
		return false, false
	}
	applyDisable(&v, !v.Disabled)
	s.verifiers[name] = v
	return v.Disabled, true
}

// applyDisable centralises the bookkeeping for the disabled flag: it must
// flip Disabled, refresh Status (so clients can disambiguate from a real
// score) and clean up the user-facing reason text.
func applyDisable(v *ipc.VerifierStatus, disabled bool) {
	v.Disabled = disabled
	if disabled {
		v.Running = false
		v.Reason = "disabled"
		v.Status = ipc.StatusDisabled
		return
	}
	if v.Reason == "disabled" {
		v.Reason = "awaiting next run"
	}
	if v.Status == ipc.StatusDisabled {
		v.Status = ipc.StatusPending
	}
}

// Snapshot returns a stable, ordered copy of the current state for read-only
// consumers (TUI, MCP `hud_status`).
func (s *State) Snapshot() ipc.StatusReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := ipc.StatusReply{
		Goal:         s.goal,
		Version:      s.version,
		LastSocketAt: s.lastSocketAt,
		LastMCPAt:    s.lastMCPAt,
	}
	var sum float64
	var enabled int
	for _, name := range s.order {
		v := s.verifiers[name]
		out.Verifiers = append(out.Verifiers, v)
		if !v.Disabled {
			sum += v.Distance
			enabled++
		}
		if v.Running && !v.Disabled {
			out.AnyRunning = true
			out.RunningVerifiers = append(out.RunningVerifiers, v.Name)
		}
	}
	if enabled > 0 {
		out.OverallDistance = sum / float64(enabled)
	}
	return out
}

// Verifier returns a single verifier's status by name.
func (s *State) Verifier(name string) (ipc.VerifierStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.verifiers[name]
	return v, ok
}

// LogEvent appends a timestamped entry to the in-memory event log. Callsites
// that previously wrote to os.Stderr during the TUI session use this so the
// alt-screen isn't corrupted by stray writes; the TUI's `l` panel renders
// the buffer instead. The line that lands in the ring buffer is the same one
// the charm/log formatter would emit to a terminal — colour escapes and all
// — so the renderer can drop it straight onto the screen.
func (s *State) LogEvent(level EventLevel, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.mu.Lock()
	s.pending = EventEntry{At: time.Now(), Level: level, Msg: msg}
	s.hasPending = true
	s.mu.Unlock()
	switch level {
	case EventError:
		s.logger.Error(msg)
	default:
		s.logger.Info(msg)
	}
}

// eventLogSink turns each charm/log line into an EventEntry in the ring
// buffer. The "raw" portion of the entry (At, Level, Msg) is captured by
// LogEvent just before invoking the logger; Write fills in Rendered and
// commits the entry. This keeps level/timestamp data consistent between the
// styled line shown in the side panel and any future structured consumers.
type eventLogSink struct {
	state *State
}

func (w *eventLogSink) Write(p []byte) (int, error) {
	line := string(p)
	// charm/log emits a trailing newline; strip it so the rendered text fits
	// on a single panel row before our wrapper splits it.
	line = strings.TrimRight(line, "\n")
	w.state.mu.Lock()
	entry := w.state.pending
	if !w.state.hasPending {
		// Defensive: a stray write outside LogEvent (e.g. logger.Print) still
		// gets stored so it isn't silently dropped.
		entry = EventEntry{At: time.Now(), Level: EventInfo, Msg: line}
	}
	entry.Rendered = line
	w.state.events = append(w.state.events, entry)
	if len(w.state.events) > eventBufferCap {
		w.state.events = w.state.events[len(w.state.events)-eventBufferCap:]
	}
	w.state.pending = EventEntry{}
	w.state.hasPending = false
	w.state.mu.Unlock()
	return len(p), nil
}

// eventLogStyles tints charm/log's level + timestamp output to match the
// rest of the HUD palette (dim grey timestamp, cyan INF, red ERR). The level
// labels are squashed to three characters so they line up with the historical
// INF/ERR badges already familiar to users.
func eventLogStyles() *charmlog.Styles {
	s := charmlog.DefaultStyles()
	s.Timestamp = s.Timestamp.Foreground(lipgloss.Color("245"))
	s.Levels[charmlog.InfoLevel] = s.Levels[charmlog.InfoLevel].
		SetString("INF").
		Foreground(lipgloss.Color("39")).
		Bold(false)
	s.Levels[charmlog.ErrorLevel] = s.Levels[charmlog.ErrorLevel].
		SetString("ERR").
		Foreground(lipgloss.Color("9")).
		Bold(true)
	s.Levels[charmlog.WarnLevel] = s.Levels[charmlog.WarnLevel].SetString("WRN")
	s.Levels[charmlog.DebugLevel] = s.Levels[charmlog.DebugLevel].SetString("DBG")
	s.Levels[charmlog.FatalLevel] = s.Levels[charmlog.FatalLevel].SetString("FAT")
	return s
}

// Events returns a copy of the event log buffer in arrival order.
func (s *State) Events() []EventEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]EventEntry, len(s.events))
	copy(out, s.events)
	return out
}
