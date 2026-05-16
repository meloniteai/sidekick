package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
)

const (
	defaultSessionKey = "__default__"
	// DefaultSessionIdleTimeout is the daemon-wide fallback for non-default
	// sessions. A configured timeout of 0 disables idle collection.
	DefaultSessionIdleTimeout = 30 * time.Minute
)

// SessionAnchor is the git context captured when a per-worktree session is
// created or re-anchored.
type SessionAnchor struct {
	Worktree string
	BaseRef  string
}

// SessionFactory creates a new session for a worktree that has touched the
// daemon for the first time.
type SessionFactory func(anchor SessionAnchor) (*State, error)

// SessionCleanup is called when idle GC removes a session so owners can stop
// its verifier runner.
type SessionCleanup func(*State)

// Registry owns the daemon's per-worktree sessions and the explicit UI
// selection. Socket/MCP traffic routes by cwd; the TUI reads the displayed
// session selected here.
type Registry struct {
	mu sync.RWMutex

	sessions     map[string]*State
	lastActivity map[string]time.Time
	defaultKey   string
	displayedKey string
	displayFixed bool
	idleTimeout  time.Duration
	factory      SessionFactory
	cleanup      SessionCleanup
}

// NewRegistry returns a registry seeded with the startup/default session.
func NewRegistry(defaultState *State, factory SessionFactory) *Registry {
	key := normalizeSessionKey(defaultState.SessionWorktree())
	now := time.Now()
	return &Registry{
		sessions:     map[string]*State{key: defaultState},
		lastActivity: map[string]time.Time{key: now},
		defaultKey:   key,
		displayedKey: key,
		idleTimeout:  DefaultSessionIdleTimeout,
		factory:      factory,
	}
}

func normalizeSessionKey(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return defaultSessionKey
	}
	if abs, err := filepath.Abs(worktree); err == nil {
		worktree = abs
	}
	return filepath.Clean(worktree)
}

// SetCleanup installs the callback invoked when idle GC removes a session.
func (r *Registry) SetCleanup(fn SessionCleanup) { r.cleanup = fn }

// SetIdleTimeout updates the daemon-wide idle timeout. Zero disables GC.
func (r *Registry) SetIdleTimeout(d time.Duration) {
	r.mu.Lock()
	r.idleTimeout = d
	r.mu.Unlock()
}

// SessionForCWD resolves cwd to a worktree session, lazily creating it when
// it belongs to the repo served by this daemon. Empty/invalid cwd falls back
// to the startup session.
func (r *Registry) SessionForCWD(cwd string) (*State, error) {
	return r.sessionForCWD(cwd, false)
}

// GoalSessionForCWD is like SessionForCWD, but re-anchors the selected
// session against its current HEAD for a new/changed goal.
func (r *Registry) GoalSessionForCWD(cwd string) (*State, error) {
	s, err := r.sessionForCWD(cwd, true)
	if err != nil {
		return nil, err
	}
	r.selectFirstGoal(s)
	return s, nil
}

func (r *Registry) sessionForCWD(cwd string, reanchor bool) (*State, error) {
	anchor, ok := ResolveAnchor(cwd)
	if !ok {
		return r.defaultSession(), nil
	}
	key := normalizeSessionKey(anchor.Worktree)

	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[key]; ok {
		if reanchor {
			if anchor.BaseRef != "" {
				s.SetSessionBaseRef(anchor.BaseRef)
			}
			s.SetSessionWorktree(anchor.Worktree)
		}
		r.lastActivity[key] = time.Now()
		return s, nil
	}
	if r.factory == nil {
		return nil, fmt.Errorf("no session factory configured for %s", anchor.Worktree)
	}
	s, err := r.factory(anchor)
	if err != nil {
		return nil, err
	}
	if s.SessionWorktree() == "" {
		s.SetSessionWorktree(anchor.Worktree)
	}
	if s.SessionBaseRef() == "" && anchor.BaseRef != "" {
		s.SetSessionBaseRef(anchor.BaseRef)
	}
	r.sessions[key] = s
	r.lastActivity[key] = time.Now()
	return s, nil
}

func (r *Registry) defaultSession() *State {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastActivity[r.defaultKey] = time.Now()
	return r.sessions[r.defaultKey]
}

func (r *Registry) selectFirstGoal(s *State) {
	key := normalizeSessionKey(s.SessionWorktree())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.displayFixed {
		return
	}
	if _, ok := r.sessions[key]; ok {
		r.displayedKey = key
		r.displayFixed = true
	}
}

// DisplayedSession returns the session currently selected by the user/TUI.
func (r *Registry) DisplayedSession() *State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s := r.sessions[r.displayedKey]; s != nil {
		return s
	}
	return r.sessions[r.defaultKey]
}

// SwitchDisplayed selects a known session for TUI/menubar rendering.
func (r *Registry) SwitchDisplayed(worktree string) bool {
	key := normalizeSessionKey(worktree)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[key]; !ok {
		return false
	}
	r.displayedKey = key
	r.displayFixed = true
	r.lastActivity[key] = time.Now()
	return true
}

// DisplayedSnapshot returns the selected session snapshot enriched with
// registry-level session metadata.
func (r *Registry) DisplayedSnapshot() ipc.StatusReply {
	return r.EnrichSnapshot(r.DisplayedSession().Snapshot())
}

// EnrichSnapshot adds session-list metadata to a StatusReply.
func (r *Registry) EnrichSnapshot(s ipc.StatusReply) ipc.StatusReply {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s.SessionCount = len(r.sessions)
	s.DisplayedWorktree = r.sessionWorktreeLocked(r.displayedKey)
	s.Sessions = r.summariesLocked()
	return s
}

// Sessions returns compact rows for all known sessions.
func (r *Registry) Sessions() []ipc.SessionSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.summariesLocked()
}

func (r *Registry) summariesLocked() []ipc.SessionSummary {
	out := make([]ipc.SessionSummary, 0, len(r.sessions))
	for key, s := range r.sessions {
		snap := s.Snapshot()
		out = append(out, ipc.SessionSummary{
			Worktree:     snap.Worktree,
			Label:        WorktreeLabel(snap.Worktree),
			Goal:         snap.Goal,
			AnyRunning:   snap.AnyRunning,
			LastActivity: r.lastActivity[key],
			Displayed:    key == r.displayedKey,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Displayed != out[j].Displayed {
			return out[i].Displayed
		}
		return out[i].LastActivity.After(out[j].LastActivity)
	})
	return out
}

func (r *Registry) sessionWorktreeLocked(key string) string {
	if s := r.sessions[key]; s != nil {
		return s.SessionWorktree()
	}
	return ""
}

// CollectIdle removes inactive non-default sessions and returns the removed
// worktree keys. The default startup session is never collected.
func (r *Registry) CollectIdle(now time.Time) []string {
	r.mu.Lock()
	timeout := r.idleTimeout
	if timeout <= 0 {
		r.mu.Unlock()
		return nil
	}
	var removed []string
	var cleanup []*State
	for key, last := range r.lastActivity {
		if key == r.defaultKey || now.Sub(last) < timeout {
			continue
		}
		if s := r.sessions[key]; s != nil {
			cleanup = append(cleanup, s)
		}
		delete(r.sessions, key)
		delete(r.lastActivity, key)
		removed = append(removed, key)
	}
	if r.sessions[r.displayedKey] == nil {
		r.displayedKey = r.defaultKey
	}
	fn := r.cleanup
	r.mu.Unlock()

	if fn != nil {
		for _, s := range cleanup {
			fn(s)
		}
	}
	return removed
}

// StartGC runs periodic idle collection until ctx is canceled.
func (r *Registry) StartGC(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.CollectIdle(now)
		}
	}
}

// SetLastActivityForTest adjusts a session timestamp for registry tests.
func (r *Registry) SetLastActivityForTest(worktree string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastActivity[normalizeSessionKey(worktree)] = at
}

// ResolveAnchor returns the git worktree and HEAD for cwd. Empty or non-git
// cwd returns ok=false so callers can route to the default session.
func ResolveAnchor(cwd string) (SessionAnchor, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return SessionAnchor{}, false
	}
	top, err := gitOutput(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return SessionAnchor{}, false
	}
	anchor := SessionAnchor{Worktree: strings.TrimSpace(top)}
	head, err := gitOutput(cwd, "rev-parse", "HEAD")
	if err == nil {
		anchor.BaseRef = strings.TrimSpace(head)
	}
	return anchor, anchor.Worktree != ""
}

func gitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// WorktreeLabel returns a compact display label for a worktree path.
func WorktreeLabel(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "default"
	}
	base := filepath.Base(worktree)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return worktree
	}
	return base
}
