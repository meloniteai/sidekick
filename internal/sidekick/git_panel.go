package sidekick

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/meloniteai/sidekick/internal/gitstats"
)

// GitPanel is the ctrl+g modal that lists the per-file workspace diff.
// Shares chrome with the ctrl+P palette and ctrl+W session switcher.
type GitPanel struct {
	workspace     gitstats.Workspace
	width, height int
}

func NewGitPanel(ws gitstats.Workspace) GitPanel {
	return GitPanel{workspace: ws}
}

func (p GitPanel) Update(msg tea.Msg) (GitPanel, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil, false
	}
	switch key.String() {
	case "esc", "ctrl+c", "g", "ctrl+g":
		return p, nil, true
	}
	return p, nil, false
}

func (p GitPanel) View() string {
	innerW := paletteInnerWidth(p.width)
	body := p.renderBody(innerW)
	box := stylePaletteBorder.Width(innerW + stylePaletteBorder.GetHorizontalPadding()).Render(reanchorBrandBg(body))
	if p.width == 0 || p.height == 0 {
		return box
	}
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

func (p GitPanel) renderBody(innerW int) string {
	var b strings.Builder
	title := "Changes "
	b.WriteString(stylePaletteTitle.Render(title))
	b.WriteString(stylePaletteSlash.Render(strings.Repeat("/", max(innerW-lipgloss.Width(title), 0))))
	b.WriteString("\n\n")

	ws := p.workspace
	meta := fmt.Sprintf("worktree=%s  branch=%s",
		defaultString(ws.WorktreeName, "?"),
		defaultString(ws.Branch, "?"),
	)
	b.WriteString(truncate(styleHeaderLabel.Render(meta), innerW))
	b.WriteString("\n")
	b.WriteString(truncate(renderDiffSummary(ws.TotalAdded, ws.TotalRemoved, len(ws.Files)), innerW))
	b.WriteString("\n\n")

	switch {
	case ws.BaseRefUnset:
		b.WriteString(styleReason.Render(truncate("(session_base_ref not set — diffs not calculated; set a goal to anchor)", innerW)))
		b.WriteString("\n")
	case len(ws.Files) == 0:
		b.WriteString(styleReason.Render(truncate("(no files edited yet this session)", innerW)))
		b.WriteString("\n")
	default:
		const countW = 6
		pathW := max(innerW-2*countW-2, 8)
		for _, f := range ws.Files {
			b.WriteString(renderGitFileRow(f, pathW, countW))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(stylePaletteHelp.Render("esc close · ctrl+g close"))
	return b.String()
}
