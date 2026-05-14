package hud

import (
	"context"
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/gitstats"
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

// gitRefreshTicks controls how often we shell out to `git diff --numstat`
// behind the workspace header. At 133ms per tick this is ~4s — fast enough
// to feel live without spawning a git process every render frame.
const gitRefreshTicks = 30

type tickMsg time.Time

// orbSpring tracks a verifier orb's smoothed position in normalized
// compass-plane coordinates. We spring on (x, y) ∈ [-1, 1]² rather than on
// raw grid cells so the spring state is independent of window resizes, and
// only project to integer cells at render time. Snap-without-spring on the
// first observation of a verifier so reconnecting to a running daemon
// doesn't paint a glide-from-center on the first frame.
type orbSpring struct {
	x, y   float64
	vx, vy float64
	armed  bool
}

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
	events            []daemon.EventEntry
	showEventLog      bool
	showGitPanel      bool
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
	palette           *Palette
	// anims is keyed by verifier name. We seed an entry on first observation
	// without scheduling an animation, so the TUI doesn't flash on startup
	// for verifiers that already have a ComputedAt from a previous batch.
	anims map[string]arrowAnim
	// orbs holds the spring-smoothed position of each verifier orb in
	// normalized compass coordinates. Updated every tick by orbSpringCfg.
	orbs         map[string]orbSpring
	orbSpringCfg harmonica.Spring
	// workspace caches the last gitstats fetch and the tick at which we
	// performed it. gitstats.Fetch shells out to git and we don't want to
	// do that on every render frame.
	workspace        gitstats.Workspace
	workspaceFetchAt int
}

// New returns an initialized Model.
func New(state *daemon.State) Model {
	return Model{
		state:    state,
		snapshot: state.Snapshot(),
		events:   state.Events(),
		anims:    map[string]arrowAnim{},
		orbs:     map[string]orbSpring{},
		// Critically damped so the orb settles onto its target without
		// bouncing past it — overshoot would briefly paint a misleading
		// "further from goal than it actually is" distance reading.
		orbSpringCfg: harmonica.NewSpring(tickInterval.Seconds(), 7.0, 1.0),
	}
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
		if m.palette != nil {
			m.palette.SetSize(msg.Width, msg.Height)
		}
	case tickMsg:
		m.snapshot = m.state.Snapshot()
		m.events = m.state.Events()
		m.clampSelectedVerifier()
		m.refreshStatusWizard()
		m.tick++
		if m.footerNotice != "" && m.tick >= m.footerNoticeUntil {
			m.footerNotice = ""
			m.footerNoticeUntil = 0
		}
		m.refreshAnims()
		m.refreshOrbs()
		m.refreshWorkspace()
		return m, tick()
	}
	if m.palette != nil {
		next, cmd, done := m.palette.Update(msg)
		if done {
			action := next.Chosen()
			m.palette = nil
			m.dispatchPaletteAction(action)
			return m, cmd
		}
		m.palette = &next
		return m, cmd
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
		case "ctrl+p":
			p := NewPalette()
			p.SetSize(m.width, m.height)
			m.palette = &p
			return m, nil
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
		case "e", "ctrl+e":
			editor := m.newEditWizard()
			editor.width = m.width
			editor.height = m.height
			m.editor = &editor
		case "n", "ctrl+n":
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
		case "l", "ctrl+l":
			m.showEventLog = !m.showEventLog
		case "g", "ctrl+g":
			m.showGitPanel = !m.showGitPanel
			// Force a refresh on toggle so the user doesn't see stale data
			// the first time they open the panel.
			m.workspaceFetchAt = 0
			m.refreshWorkspace()
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

// dispatchPaletteAction runs the side-effect for a palette item once the user
// confirms with enter. Esc dismissal arrives as paletteActionNone and is a
// no-op; all real actions reuse the same code paths as the bare hotkeys (n,
// e, g, l) so behaviour stays consistent however the user invokes them.
func (m *Model) dispatchPaletteAction(action paletteAction) {
	switch action {
	case paletteActionNewVerifier:
		editor := NewCreateWizard(m.configPath)
		editor.width = m.width
		editor.height = m.height
		m.editor = &editor
	case paletteActionEditVerifier:
		editor := m.newEditWizard()
		editor.width = m.width
		editor.height = m.height
		m.editor = &editor
	case paletteActionToggleGitPanel:
		m.showGitPanel = !m.showGitPanel
		m.workspaceFetchAt = 0
		m.refreshWorkspace()
	case paletteActionToggleEventLog:
		m.showEventLog = !m.showEventLog
	}
}

// refreshWorkspace refetches git workspace metadata when enough ticks have
// passed since the last fetch. The first call after construction always
// fetches (workspaceFetchAt == 0).
func (m *Model) refreshWorkspace() {
	if m.state == nil {
		return
	}
	if m.workspaceFetchAt != 0 && m.tick-m.workspaceFetchAt < gitRefreshTicks {
		return
	}
	m.workspace = gitstats.Fetch(context.Background(), m.state.SessionBaseRef(), m.state.SessionEdits())
	m.workspaceFetchAt = m.tick
	if m.workspaceFetchAt == 0 {
		// Reserve 0 as the "never fetched" sentinel so the tick==0 case still
		// triggers a refresh next time.
		m.workspaceFetchAt = 1
	}
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

// refreshOrbs advances the per-verifier position springs toward each orb's
// projected target in normalized compass coordinates. Direction+distance are
// converted to (x, y) ∈ [-1, 1]²; the spring then eases the rendered position
// toward that target over a handful of ticks.
//
// On first observation we snap to the target without springing — that way a
// fresh TUI attached to an already-running daemon doesn't paint a glide-in
// from the center for every verifier.
//
// Disabled verifiers are skipped entirely (renderGrid won't draw them); we do
// not reset their spring state, so re-enabling them resumes from where they
// left off rather than jumping back to center.
func (m *Model) refreshOrbs() {
	if m.orbs == nil {
		m.orbs = make(map[string]orbSpring, len(m.snapshot.Verifiers))
	}
	for _, v := range m.snapshot.Verifiers {
		if v.Disabled {
			continue
		}
		tx, ty, ok := orbTargetXY(v.Direction, v.Distance)
		if !ok {
			continue
		}
		o, seen := m.orbs[v.Name]
		if !seen || !o.armed {
			m.orbs[v.Name] = orbSpring{x: tx, y: ty, armed: true}
			continue
		}
		o.x, o.vx = m.orbSpringCfg.Update(o.x, o.vx, tx)
		o.y, o.vy = m.orbSpringCfg.Update(o.y, o.vy, ty)
		m.orbs[v.Name] = o
	}
}

// orbTargetXY converts a (direction, distance) pair to the normalized
// compass-plane target used by the orb springs. Returns ok=false for an
// unknown direction string. The y axis is screen-down to match grid coords.
func orbTargetXY(direction string, distance float64) (x, y float64, ok bool) {
	θ, ok := directionAngle[direction]
	if !ok {
		return 0, 0, false
	}
	if distance < 0 {
		distance = 0
	}
	if distance > 1 {
		distance = 1
	}
	return math.Cos(θ) * distance, -math.Sin(θ) * distance, true
}

// orbPosition returns the smoothed (col, row) for a verifier on a grid of the
// given size. Falls back to the canonical project() call when no spring state
// exists yet so callers (e.g. tests) that never tick still render sensibly.
func (m Model) orbPosition(name, direction string, distance float64, w, h int) (col, row int, ok bool) {
	o, seen := m.orbs[name]
	if !seen || !o.armed {
		return project(direction, distance, w, h)
	}
	return projectXY(o.x, o.y, w, h)
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
