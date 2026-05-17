package sidekick

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/meloniteai/sidekick/internal/registry"
)

// remoteBrowserMode is the two-pane state machine of the browser modal:
// a flat list of manifests, or a detail view of one manifest with
// install controls.
type remoteBrowserMode int

const (
	browserModeList remoteBrowserMode = iota
	browserModeDetail
)

// directionsCycle is the canonical compass order the "d" key walks
// through in detail view. Matches the validDirections set in
// internal/config and the order verifiers light up on the grid.
var directionsCycle = []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}

// RemoteBrowser is the ctrl+p → "Browse Verifiers" overlay. It lists
// manifests fetched from the GitHub-hosted verifier catalog and lets
// the user install one with a single keystroke into either the project
// sidekick.yaml or the global ~/.sidekick/sidekick.yaml.
type RemoteBrowser struct {
	client      *registry.Client
	mode        remoteBrowserMode
	entries     []registry.Manifest
	cursor      int
	detailIdx   int
	scope       registry.Scope
	direction   string
	loading     bool
	loadErr     error
	installMsg  string
	installing  bool
	width       int
	height      int
	projectPath string
}

// browserListMsg lands in the host Model.Update when the initial fetch
// finishes. The modal switches out of its loading state and renders the
// list (or the error row).
type browserListMsg struct {
	entries []registry.Manifest
	err     error
}

// browserInstalledMsg lands after an install completes. err non-nil
// means the install failed; FinalName carries the (possibly renamed)
// verifier name that was written so the success row can reflect what
// actually landed in sidekick.yaml.
type browserInstalledMsg struct {
	finalName string
	path      string
	err       error
}

// NewRemoteBrowser returns a browser pointed at the default catalog
// (meloniteai/sidekick-verifiers @ main). projectPath should be the
// sidekick.yaml of the currently displayed session — passed straight through
// to registry.Install when the user picks ScopeProject.
func NewRemoteBrowser(projectPath string) RemoteBrowser {
	return RemoteBrowser{
		client:      registry.New("", "", ""),
		mode:        browserModeList,
		scope:       ScopeForExistingProject(projectPath),
		loading:     true,
		projectPath: projectPath,
	}
}

// ScopeForExistingProject picks a sensible default install scope: if
// the displayed session has a project sidekick.yaml, default to project;
// otherwise default to global so first-time users with no sidekick.yaml
// don't accidentally write into cwd.
func ScopeForExistingProject(path string) registry.Scope {
	if path == "" {
		return registry.ScopeGlobal
	}
	return registry.ScopeProject
}

// Init kicks off the catalog fetch on a worker goroutine. Returns a
// tea.Cmd that resolves to browserListMsg once the network round-trips
// complete.
func (b RemoteBrowser) Init() tea.Cmd {
	c := b.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		entries, err := c.List(ctx)
		return browserListMsg{entries: entries, err: err}
	}
}

// Update advances the modal state. done=true means the host Model should
// close the browser.
func (b RemoteBrowser) Update(msg tea.Msg) (RemoteBrowser, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case browserListMsg:
		b.loading = false
		b.loadErr = msg.err
		b.entries = sortedEntries(msg.entries)
		if b.cursor >= len(b.entries) {
			b.cursor = 0
		}
		return b, nil, false
	case browserInstalledMsg:
		b.installing = false
		if msg.err != nil {
			b.installMsg = "install failed: " + msg.err.Error()
		} else {
			scope := "project"
			if b.scope == registry.ScopeGlobal {
				scope = "global"
			}
			b.installMsg = fmt.Sprintf("installed %s into %s sidekick.yaml (%s)", msg.finalName, scope, msg.path)
		}
		return b, nil, false
	case tea.KeyMsg:
		return b.handleKey(msg)
	}
	return b, nil, false
}

func (b RemoteBrowser) handleKey(msg tea.KeyMsg) (RemoteBrowser, tea.Cmd, bool) {
	switch b.mode {
	case browserModeList:
		switch msg.String() {
		case "esc", "ctrl+c", "q":
			return b, nil, true
		case "up", "k":
			if b.cursor > 0 {
				b.cursor--
			}
		case "down", "j", "tab":
			if b.cursor < len(b.entries)-1 {
				b.cursor++
			}
		case "enter", "right", "l":
			if len(b.entries) == 0 {
				return b, nil, false
			}
			b.mode = browserModeDetail
			b.detailIdx = b.cursor
			b.direction = b.entries[b.detailIdx].Direction
			if b.direction == "" {
				b.direction = "NE"
			}
			b.installMsg = ""
		}
		return b, nil, false
	case browserModeDetail:
		switch msg.String() {
		case "esc", "left", "h":
			b.mode = browserModeList
			b.installMsg = ""
			return b, nil, false
		case "p":
			b.scope = registry.ScopeProject
			return b, nil, false
		case "g":
			b.scope = registry.ScopeGlobal
			return b, nil, false
		case "d":
			b.direction = cycleDirection(b.direction)
			return b, nil, false
		case "i", "enter":
			if b.installing || b.detailIdx >= len(b.entries) {
				return b, nil, false
			}
			b.installing = true
			b.installMsg = "installing…"
			m := b.entries[b.detailIdx]
			opts := registry.InstallOptions{
				Scope:       b.scope,
				Manifest:    m,
				ProjectPath: b.projectPath,
				Direction:   b.direction,
			}
			cmd := func() tea.Msg {
				res, err := registry.Install(opts)
				return browserInstalledMsg{finalName: res.FinalName, path: res.Path, err: err}
			}
			return b, cmd, false
		}
		return b, nil, false
	}
	return b, nil, false
}

// sortedEntries groups manifests by type (agent, command, binary) and
// alphabetises by name within each group so the list view is stable
// across refetches.
func sortedEntries(in []registry.Manifest) []registry.Manifest {
	out := append([]registry.Manifest(nil), in...)
	groupRank := map[string]int{"agent": 0, "command": 1, "binary": 2}
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := groupRank[out[i].Type], groupRank[out[j].Type]
		if gi != gj {
			return gi < gj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func cycleDirection(cur string) string {
	cur = strings.ToUpper(cur)
	for i, d := range directionsCycle {
		if d == cur {
			return directionsCycle[(i+1)%len(directionsCycle)]
		}
	}
	return directionsCycle[0]
}

// View renders either the list or detail page, framed in the same
// palette-style chrome the rest of the modals use so the browser reads
// as part of the same family.
func (b RemoteBrowser) View() string {
	innerW := paletteInnerWidth(b.width)
	var body string
	switch b.mode {
	case browserModeList:
		body = b.viewList(innerW)
	case browserModeDetail:
		body = b.viewDetail(innerW)
	}
	box := stylePaletteBorder.Width(innerW + stylePaletteBorder.GetHorizontalPadding()).Render(reanchorBrandBg(body))
	if b.width == 0 || b.height == 0 {
		return box
	}
	return lipgloss.Place(b.width, b.height, lipgloss.Center, lipgloss.Center, box)
}

func (b RemoteBrowser) viewList(innerW int) string {
	var bld strings.Builder
	bld.WriteString(renderBrowserTitle("Verifiers", innerW))
	bld.WriteString("\n\n")

	if b.loading {
		bld.WriteString(stylePalettePlaceholder.Render("Fetching meloniteai/sidekick-verifiers…"))
		bld.WriteString("\n")
		return bld.String()
	}
	if b.loadErr != nil {
		bld.WriteString(stylePalettePlaceholder.Render(formatLoadErr(b.loadErr)))
		bld.WriteString("\n\n")
		bld.WriteString(stylePaletteHelp.Render("esc cancel"))
		return bld.String()
	}
	if len(b.entries) == 0 {
		bld.WriteString(stylePalettePlaceholder.Render("(no verifiers in catalog)"))
		bld.WriteString("\n")
		return bld.String()
	}

	var lastGroup string
	for i, m := range b.entries {
		if m.Type != lastGroup {
			if i > 0 {
				bld.WriteString("\n")
			}
			bld.WriteString(stylePaletteTitle.Render(strings.ToUpper(m.Type)))
			bld.WriteString("\n")
			lastGroup = m.Type
		}
		bld.WriteString(renderBrowserRow(m, innerW, i == b.cursor))
		bld.WriteString("\n")
	}
	bld.WriteString("\n")
	bld.WriteString(renderBrowserHelpBar("↑/↓ choose · enter details · esc cancel", innerW))
	return bld.String()
}

func (b RemoteBrowser) viewDetail(innerW int) string {
	m := b.entries[b.detailIdx]
	var bld strings.Builder
	bld.WriteString(renderBrowserTitle(m.Name, innerW))
	bld.WriteString("\n\n")

	meta := fmt.Sprintf("type=%s · direction=%s", m.Type, b.direction)
	bld.WriteString(styleReason.Render(meta))
	bld.WriteString("\n\n")

	if m.Description != "" {
		bld.WriteString(wrapToWidth(strings.TrimSpace(m.Description), innerW))
		bld.WriteString("\n\n")
	}

	if m.Type == "agent" && (m.Agent.Agent != "" || m.Agent.Model != "") {
		agentLine := fmt.Sprintf("agent: %s", m.Agent.Agent)
		if m.Agent.Model != "" {
			agentLine += "  model: " + m.Agent.Model
		}
		if m.Agent.Thinking != "" {
			agentLine += "  thinking: " + m.Agent.Thinking
		}
		bld.WriteString(styleReason.Render(agentLine))
		bld.WriteString("\n")
	}

	if hasManifestPermissions(m.Permissions) {
		bld.WriteString("\n")
		bld.WriteString(stylePaletteTitle.Render("PERMISSIONS"))
		bld.WriteString("\n")
		bld.WriteString(styleReason.Render(formatManifestPermissions(m.Permissions)))
		bld.WriteString("\n")
		if len(m.Permissions.AllowedTools) > 0 {
			bld.WriteString(styleReason.Render("allowed_tools:"))
			bld.WriteString("\n")
			for _, t := range m.Permissions.AllowedTools {
				bld.WriteString(styleReason.Render("  • " + t))
				bld.WriteString("\n")
			}
		}
	}

	bld.WriteString("\n")
	bld.WriteString(stylePaletteTitle.Render("SOURCE"))
	bld.WriteString("\n")
	bld.WriteString(styleReason.Render(truncate(m.RawURL, innerW)))
	bld.WriteString("\n")
	bld.WriteString(styleReason.Render("sha256: " + shortSHA(m.SHA256)))
	bld.WriteString("\n\n")

	scope := "project"
	if b.scope == registry.ScopeGlobal {
		scope = "global"
	}
	// Render the scope value as a coral chip so a toggle is impossible
	// to miss — the surrounding label stays in dim grey so the eye
	// jumps to the changing bit.
	bld.WriteString(styleReason.Render("install scope: "))
	bld.WriteString(stylePaletteSelected.Render(" " + scope + " "))
	bld.WriteString("\n")

	if b.installMsg != "" {
		bld.WriteString("\n")
		bld.WriteString(styleReason.Render(b.installMsg))
		bld.WriteString("\n")
	}

	bld.WriteString("\n")
	bld.WriteString(renderBrowserHelpBar("p project · g global · d direction · i install · esc back", innerW))
	return bld.String()
}

// renderBrowserHelpBar paints the footer key menu as a full-width
// coral status bar so the available actions read as a single chunky
// chip — matches stylePaletteSelected so it sits in the same visual
// family as the cursor bar and the install-scope chip.
func renderBrowserHelpBar(help string, innerW int) string {
	return stylePaletteSelected.Width(innerW).Render(truncate(" "+help, innerW))
}

func renderBrowserTitle(title string, innerW int) string {
	title = title + " "
	slashCount := max(innerW-lipgloss.Width(title), 0)
	return stylePaletteTitle.Render(title) + stylePaletteSlash.Render(strings.Repeat("/", slashCount))
}

func renderBrowserRow(m registry.Manifest, innerW int, selected bool) string {
	left := "  " + m.Name
	right := truncate(strings.TrimSpace(strings.ReplaceAll(m.Description, "\n", " ")), max(innerW/2, 16))
	labelMax := max(innerW-lipgloss.Width(right)-2, 1)
	left = truncate(left, labelMax)
	pad := max(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	row := left + strings.Repeat(" ", pad) + right
	if selected {
		return stylePaletteSelected.Width(innerW).Render(truncate(row, innerW))
	}
	return padCell(row, innerW)
}

func hasManifestPermissions(p registry.ManifestPermSet) bool {
	return p.Network || p.Filesystem != "" || len(p.Env) > 0 || len(p.AllowedTools) > 0
}

func formatManifestPermissions(p registry.ManifestPermSet) string {
	parts := []string{}
	if p.Filesystem != "" {
		parts = append(parts, "fs="+p.Filesystem)
	}
	if p.Network {
		parts = append(parts, "network=true")
	} else {
		parts = append(parts, "network=false")
	}
	if len(p.Env) > 0 {
		parts = append(parts, "env="+strings.Join(p.Env, ":"))
	}
	return strings.Join(parts, "  ")
}

func formatLoadErr(err error) string {
	var rl *registry.RateLimitError
	if errors.As(err, &rl) {
		if rl.ResetAt.IsZero() {
			return "github rate limit exceeded — try again later"
		}
		return "github rate limit exceeded — resets " + rl.ResetAt.Format(time.RFC1123)
	}
	return "fetch failed: " + err.Error()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// wrapToWidth is a minimal word-wrapper for the description block.
// Existing helpers in this package operate on table cells (single
// lines); description is the only place we need real multi-line wrap.
func wrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if lipgloss.Width(cur)+1+lipgloss.Width(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	for i, ln := range lines {
		lines[i] = styleReason.Render(ln)
	}
	return strings.Join(lines, "\n")
}
