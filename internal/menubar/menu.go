package menubar

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
)

const (
	actionTrigger     = 1
	actionStop        = 2
	actionQuit        = 3
	actionSessionBase = 20
)

type payload struct {
	Title string     `json:"title"`
	Items []menuItem `json:"items"`
}

type menuItem struct {
	Title     string `json:"title,omitempty"`
	Enabled   bool   `json:"enabled,omitempty"`
	Action    int    `json:"action,omitempty"`
	Separator bool   `json:"separator,omitempty"`
	Tone      string `json:"tone,omitempty"`
}

// RenderJSON converts daemon state into the compact native status-menu model
// consumed by the macOS AppKit bridge.
func RenderJSON(s ipc.StatusReply) ([]byte, error) {
	p := payload{
		Title: statusTitle(s),
		Items: []menuItem{
			{Title: fmt.Sprintf("HUD active | version %s", fallback(s.Version, "dev")), Tone: toneForOverall(s)},
			{Title: "Session: " + sessionText(s)},
			{Title: "Goal: " + goalText(s.Goal)},
			{Title: fmt.Sprintf("Socket %s | MCP %s | %d verifiers",
				shortTime(s.LastSocketAt), shortTime(s.LastMCPAt), len(s.Verifiers))},
			{Separator: true},
		},
	}
	if len(s.Sessions) > 1 {
		p.Items = append(p.Items, menuItem{Title: "Switch session"})
		for i, session := range s.Sessions {
			title := session.Label
			if title == "" {
				title = session.Worktree
			}
			if session.Displayed {
				title = "✓ " + title
			} else {
				title = "  " + title
			}
			p.Items = append(p.Items, menuItem{
				Title:   title,
				Enabled: true,
				Action:  actionSessionBase + i,
				Tone:    boolTone(session.Displayed, "accent", "muted"),
			})
		}
		p.Items = append(p.Items, menuItem{Separator: true})
	}

	if len(s.Verifiers) == 0 {
		p.Items = append(p.Items, menuItem{Title: "No verifiers configured"})
	} else {
		for _, v := range s.Verifiers {
			p.Items = append(p.Items, menuItem{
				Title: verifierLine(v),
				Tone:  toneForVerifier(v),
			})
			if v.Reason != "" {
				p.Items = append(p.Items, menuItem{Title: "  " + trunc(v.Reason, 72)})
			}
		}
	}

	stopEnabled := anyRunning(s.Verifiers)
	p.Items = append(p.Items,
		menuItem{Separator: true},
		menuItem{Title: "Trigger now", Enabled: true, Action: actionTrigger, Tone: "accent"},
		menuItem{Title: "Stop current run", Enabled: stopEnabled, Action: actionStop, Tone: boolTone(stopEnabled, "warn", "muted")},
		menuItem{Separator: true},
		menuItem{Title: "Quit HUD", Enabled: true, Action: actionQuit},
	)
	return json.Marshal(p)
}

func sessionText(s ipc.StatusReply) string {
	label := ""
	for _, row := range s.Sessions {
		if row.Displayed {
			label = row.Label
			break
		}
	}
	if label == "" {
		label = s.Worktree
	}
	if label == "" {
		return "default"
	}
	if s.SessionCount > 1 {
		return fmt.Sprintf("%s (%d)", label, s.SessionCount)
	}
	return label
}

func statusTitle(s ipc.StatusReply) string {
	if anyRunning(s.Verifiers) {
		return "HUD running"
	}
	if len(s.Verifiers) == 0 {
		return "HUD"
	}
	return fmt.Sprintf("HUD %.2f", s.OverallDistance)
}

func verifierLine(v ipc.VerifierStatus) string {
	parts := []string{trunc(v.Name, 24)}
	if v.Direction != "" {
		parts = append(parts, v.Direction)
	}
	parts = append(parts, fmt.Sprintf("d=%.2f", v.Distance))
	if v.Running {
		parts = append(parts, "running")
	}
	return strings.Join(parts, " | ")
}

func toneForOverall(s ipc.StatusReply) string {
	if anyRunning(s.Verifiers) {
		return "running"
	}
	return toneForDistance(s.OverallDistance)
}

func toneForVerifier(v ipc.VerifierStatus) string {
	if v.Running {
		return "running"
	}
	return toneForDistance(v.Distance)
}

func toneForDistance(d float64) string {
	switch {
	case d <= 0.25:
		return "good"
	case d <= 0.50:
		return "ok"
	case d <= 0.75:
		return "warn"
	default:
		return "bad"
	}
}

func anyRunning(vs []ipc.VerifierStatus) bool {
	for _, v := range vs {
		if v.Running {
			return true
		}
	}
	return false
}

func boolTone(ok bool, ifTrue, ifFalse string) string {
	if ok {
		return ifTrue
	}
	return ifFalse
}

func fallback(s, fb string) string {
	if s == "" {
		return fb
	}
	return s
}

func goalText(goal string) string {
	if strings.TrimSpace(goal) == "" {
		return "(none, submit a prompt or run hud goal)"
	}
	return trunc(goal, 76)
}

func shortTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("15:04:05")
}

func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	if n <= 3 {
		return string(rs[:n])
	}
	return string(rs[:n-3]) + "..."
}
