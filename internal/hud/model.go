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

type tickMsg time.Time

// Model is the Bubble Tea model. It pulls snapshots directly from the daemon
// State (in-process), so there is no IPC overhead for the TUI itself.
type Model struct {
	state    *daemon.State
	snapshot ipc.StatusReply
	width    int
	height   int
}

// New returns an initialized Model.
func New(state *daemon.State) Model {
	return Model{state: state, snapshot: state.Snapshot()}
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
		return m, tick()
	}
	return m, nil
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}
