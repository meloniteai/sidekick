package hud

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

// tickInterval drives a periodic re-render so the TUI reflects async verifier
// updates without explicit broadcast plumbing.
const tickInterval = 133 * time.Millisecond

// arrowAnimFrames is how many ticks the post-computation arrow animation
// lasts on each verifier's compass plane. ~5 * 200ms ≈ 1s.
const arrowAnimFrames = 5

// footerNoticeTicks controls how long transient footer feedback replaces the
// key legend. At 133ms per tick this is roughly 1.6s.
const footerNoticeTicks = 12

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
	state             *daemon.State
	snapshot          ipc.StatusReply
	width             int
	height            int
	tick              int
	selectedVerifier  int
	footerNotice      string
	footerNoticeUntil int
	onManualTrigger   func()
	onTriggerVerifier func(name string)
	onToggleVerifier  func(name string)
	onStopAll         func()
	onConfigSaved     func() error
	configPath        string
	editor            *EditWizard
	status            *StatusWizard
	// anims is keyed by verifier name. We seed an entry on first observation
	// without scheduling an animation, so the TUI doesn't flash on startup
	// for verifiers that already have a ComputedAt from a previous batch.
	anims map[string]arrowAnim
}

// New returns an initialized Model.
func New(state *daemon.State) Model {
	return Model{state: state, snapshot: state.Snapshot(), anims: map[string]arrowAnim{}}
}

// WithManualTrigger sets a callback invoked when the user presses 't' on the
// main screen. It is called in the Bubble Tea Update goroutine, so it must be
// safe to call concurrently with background verifier runs.
func (m Model) WithManualTrigger(fn func()) Model {
	m.onManualTrigger = fn
	return m
}

// WithTriggerVerifier sets a callback invoked when the user runs the selected
// verifier from the footer browser.
func (m Model) WithTriggerVerifier(fn func(name string)) Model {
	m.onTriggerVerifier = fn
	return m
}

// WithToggleVerifier sets a callback invoked when the user presses a footer
// verifier key on the main screen.
func (m Model) WithToggleVerifier(fn func(name string)) Model {
	m.onToggleVerifier = fn
	return m
}

// WithStopAll sets a callback invoked when the user presses ESC on the main
// screen, asking the runner to terminate any in-flight verifier subprocesses
// and discard pending work. Same concurrency contract as WithManualTrigger.
func (m Model) WithStopAll(fn func()) Model {
	m.onStopAll = fn
	return m
}

// WithConfigEditor enables the in-TUI verifier edit wizard. configPath should
// be the hud.yaml file that produced the current verifier configuration.
func (m Model) WithConfigEditor(configPath string) Model {
	m.configPath = configPath
	return m
}

// WithConfigSaved sets a callback invoked after the edit wizard saves
// hud.yaml metadata.
func (m Model) WithConfigSaved(fn func() error) Model {
	m.onConfigSaved = fn
	return m
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
		if m.editor != nil {
			m.editor.width = msg.Width
			m.editor.height = msg.Height
		}
		if m.status != nil {
			m.status.width = msg.Width
			m.status.height = msg.Height
		}
	case tickMsg:
		m.snapshot = m.state.Snapshot()
		m.clampSelectedVerifier()
		m.refreshStatusWizard()
		m.tick++
		if m.footerNotice != "" && m.tick >= m.footerNoticeUntil {
			m.footerNotice = ""
			m.footerNoticeUntil = 0
		}
		m.refreshAnims()
		return m, tick()
	}
	if m.status != nil {
		next, cmd, done := m.status.Update(msg)
		if done {
			m.status = nil
			return m, cmd
		}
		m.status = &next
		return m, cmd
	}
	if m.editor != nil {
		next, cmd, done := m.editor.Update(msg)
		if next.saved {
			if m.onConfigSaved != nil {
				if err := m.onConfigSaved(); err != nil {
					next.errMsg = "reload config: " + err.Error()
				}
			}
			next.saved = false
			if m.state != nil {
				m.snapshot = m.state.Snapshot()
			}
		}
		if done {
			m.editor = nil
			if m.state != nil {
				m.snapshot = m.state.Snapshot()
			}
			return m, cmd
		}
		m.editor = &next
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "t":
			if m.onManualTrigger != nil {
				m.onManualTrigger()
			}
		case "up", "k":
			if m.selectedVerifier > 0 {
				m.selectedVerifier--
			}
		case "down", "j":
			if m.selectedVerifier < len(m.snapshot.Verifiers)-1 {
				m.selectedVerifier++
			}
		case " ", "x":
			m.toggleSelectedVerifier()
		case "r":
			if v, ok := m.selectedStatus(); ok && m.onTriggerVerifier != nil {
				m.onTriggerVerifier(v.Name)
			}
		case "esc":
			if m.onStopAll != nil {
				m.onStopAll()
			}
		case "e":
			editor := m.newEditWizard()
			editor.width = m.width
			editor.height = m.height
			m.editor = &editor
		case "n":
			editor := NewCreateWizard(m.configPath)
			editor.width = m.width
			editor.height = m.height
			m.editor = &editor
		case "enter":
			if v, ok := m.selectedStatus(); ok {
				status := NewStatusWizard(v)
				status.width = m.width
				status.height = m.height
				m.status = &status
			}
		default:
			if idx, ok := toggleKeyIndex(msg.String()); ok && idx < len(m.snapshot.Verifiers) {
				m.toggleVerifierByName(m.snapshot.Verifiers[idx].Name)
			}
		}
	}
	return m, nil
}

func (m *Model) clampSelectedVerifier() {
	if len(m.snapshot.Verifiers) == 0 {
		m.selectedVerifier = 0
		return
	}
	if m.selectedVerifier < 0 {
		m.selectedVerifier = 0
	}
	if m.selectedVerifier >= len(m.snapshot.Verifiers) {
		m.selectedVerifier = len(m.snapshot.Verifiers) - 1
	}
}

func (m Model) selectedStatus() (ipc.VerifierStatus, bool) {
	if m.selectedVerifier < 0 || m.selectedVerifier >= len(m.snapshot.Verifiers) {
		return ipc.VerifierStatus{}, false
	}
	return m.snapshot.Verifiers[m.selectedVerifier], true
}

func (m *Model) toggleSelectedVerifier() {
	v, ok := m.selectedStatus()
	if !ok {
		return
	}
	m.toggleVerifierByName(v.Name)
}

func (m *Model) toggleVerifierByName(name string) {
	if name == "" || m.onToggleVerifier == nil {
		return
	}
	m.onToggleVerifier(name)
	if m.state != nil {
		m.snapshot = m.state.Snapshot()
		m.clampSelectedVerifier()
	}
	if v, ok := m.verifierByName(name); ok {
		state := "enabled"
		if v.Disabled {
			state = "disabled"
		}
		m.setFooterNotice(v.Name + " " + state)
	}
}

func (m Model) verifierByName(name string) (ipc.VerifierStatus, bool) {
	for _, v := range m.snapshot.Verifiers {
		if v.Name == name {
			return v, true
		}
	}
	return ipc.VerifierStatus{}, false
}

func (m *Model) setFooterNotice(message string) {
	m.footerNotice = message
	m.footerNoticeUntil = m.tick + footerNoticeTicks
}

func (m Model) newEditWizard() EditWizard {
	if v, ok := m.selectedStatus(); ok {
		return NewEditWizardFor(m.configPath, v.Name)
	}
	return NewEditWizard(m.configPath)
}

func (m *Model) refreshStatusWizard() {
	if m.status == nil {
		return
	}
	for _, v := range m.snapshot.Verifiers {
		if v.Name == m.status.verifier {
			m.status.status = v
			return
		}
	}
	m.status.errMsg = "verifier no longer exists"
}

func toggleKeyIndex(key string) (int, bool) {
	if len(key) != 1 {
		return 0, false
	}
	ch := key[0]
	if ch >= '1' && ch <= '9' {
		return int(ch - '1'), true
	}
	if ch == '0' {
		return 9, true
	}
	return 0, false
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

// calibPeriod is the total ping-pong cycle length (out + back) for the
// calibrating animation. At 133ms per tick this gives ~1.33s per cycle.
const calibPeriod = arrowAnimFrames * 2

// animInfo returns animation state for a verifier.
//
// When a post-completion animation is active (ComputedAt just advanced),
// active=true and frame/inward describe the one-shot sweep.
//
// When the verifier is running (calibrating=true) and no post-completion
// animation is active, the arrows bounce in/out in a continuous ping-pong
// loop so the user can see which axis is being re-measured. The calibrating
// frame is also returned as frame/inward so callers can reuse the same
// rendering path.
func (m Model) animInfo(name string, running bool) (frame int, active bool, inward bool, calibrating bool) {
	a, ok := m.anims[name]
	// Post-completion one-shot animation: takes priority over calibration.
	if ok && a.armed && a.startTick != 0 {
		f := m.tick - a.startTick
		if f >= 0 && f < arrowAnimFrames {
			return f, true, a.inward, false
		}
	}
	// Calibrating ping-pong: shown while the verifier subprocess is running.
	if running {
		phase := m.tick % calibPeriod
		if phase < arrowAnimFrames {
			return phase, true, false, true
		}
		return calibPeriod - phase - 1, true, true, true
	}
	return -1, false, false, false
}

// animFrame wraps animInfo for callers that only need frame and active state.
func (m Model) animFrame(name string, running bool) (int, bool) {
	frame, active, _, _ := m.animInfo(name, running)
	return frame, active
}
