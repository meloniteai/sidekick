package hud

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

// tickInterval drives a periodic re-render so the TUI reflects async verifier
// updates without explicit broadcast plumbing.
const tickInterval = 200 * time.Millisecond

// arrowAnimFrames is how many ticks the post-computation arrow animation
// lasts on each verifier's compass plane. ~5 * 200ms ≈ 1s.
const arrowAnimFrames = 5

type tickMsg time.Time

// arrowAnim tracks a per-verifier compass-plane animation. Each time a
// verifier's ComputedAt advances (i.e. a new computation cycle lands because
// of a code change), we restart the animation so the user can see which
// verifier just got refreshed.
type arrowAnim struct {
	lastComputed time.Time
	startTick    int     // tick at which the animation began; 0 if never animated
	armed        bool    // true once we've observed the verifier at least once (suppresses startup flash)
	lastDistance float64 // distance at the time of the last observation
	inward       bool    // true if distance decreased (moving toward the goal)
}

// Model is the Bubble Tea model. It pulls snapshots directly from the daemon
// State (in-process), so there is no IPC overhead for the TUI itself.
type Model struct {
	state    *daemon.State
	snapshot ipc.StatusReply
	width    int
	height   int
	tick     int
	// anims is keyed by verifier name. We seed an entry on first observation
	// without scheduling an animation, so the TUI doesn't flash on startup
	// for verifiers that already have a ComputedAt from a previous batch.
	anims map[string]arrowAnim
}

// New returns an initialized Model.
func New(state *daemon.State) Model {
	return Model{state: state, snapshot: state.Snapshot(), anims: map[string]arrowAnim{}}
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	return tick()
}

// Update satisfies tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	case tickMsg:
		m.snapshot = m.state.Snapshot()
		m.tick++
		m.refreshAnims()
		return m, tick()
	}
	return m, nil
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refreshAnims walks the latest snapshot and starts an animation whenever a
// verifier's ComputedAt advances. The first observation of any verifier just
// arms the entry without scheduling an animation — that way reconnecting to
// an already-running daemon doesn't paint stale "just updated" arrows.
func (m *Model) refreshAnims() {
	if m.anims == nil {
		m.anims = make(map[string]arrowAnim, len(m.snapshot.Verifiers))
	}
	for _, v := range m.snapshot.Verifiers {
		a, seen := m.anims[v.Name]
		if !seen {
			m.anims[v.Name] = arrowAnim{lastComputed: v.ComputedAt, lastDistance: v.Distance, armed: true}
			continue
		}
		if v.ComputedAt.IsZero() || v.ComputedAt.Equal(a.lastComputed) {
			continue
		}
		m.anims[v.Name] = arrowAnim{
			lastComputed: v.ComputedAt,
			startTick:    m.tick,
			armed:        true,
			lastDistance: v.Distance,
			inward:       v.Distance < a.lastDistance,
		}
	}
}

// animInfo returns the 0-indexed frame, whether the animation is active, and
// whether the motion is inward (distance shrank) for a verifier.
func (m Model) animInfo(name string) (frame int, active bool, inward bool) {
	a, ok := m.anims[name]
	if !ok || !a.armed || a.startTick == 0 {
		return -1, false, false
	}
	frame = m.tick - a.startTick
	if frame < 0 || frame >= arrowAnimFrames {
		return -1, false, false
	}
	return frame, true, a.inward
}

// animFrame wraps animInfo for callers that only need frame and active state.
func (m Model) animFrame(name string) (int, bool) {
	frame, active, _ := m.animInfo(name)
	return frame, active
}
