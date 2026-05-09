// Package daemon owns the long-running session state behind `hud start`.
package daemon

import (
	"sync"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
)

// State is the in-memory snapshot of an active session. It is the single
// source of truth that the TUI renders, the MCP server reads, and the
// hook handlers mutate.
type State struct {
	mu             sync.RWMutex
	goal           string
	sessionBaseRef string
	verifiers      map[string]ipc.VerifierStatus
	order          []string
	version        string
	lastSocketAt   time.Time
	lastMCPAt      time.Time
}

// NewState returns a zeroed State.
func NewState() *State {
	return &State{verifiers: map[string]ipc.VerifierStatus{}}
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

// SetSessionBaseRef records the git SHA HEAD pointed at when `hud start`
// began. Verifiers diff the working tree against this ref to evaluate
// cumulative session work, not just the latest write.
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

// MarkRunning toggles the per-verifier "running" flag, useful for TUI feedback.
func (s *State) MarkRunning(name string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verifiers[name]
	if !ok {
		return
	}
	v.Running = running
	if !running {
		v.ComputedAt = time.Now()
	}
	s.verifiers[name] = v
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
	for _, name := range s.order {
		v := s.verifiers[name]
		out.Verifiers = append(out.Verifiers, v)
		sum += v.Distance
	}
	if n := len(s.order); n > 0 {
		out.OverallDistance = sum / float64(n)
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
