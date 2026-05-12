package hud

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

func TestToggleKeyIndex(t *testing.T) {
	for key, want := range map[string]int{
		"1": 0,
		"2": 1,
		"9": 8,
		"0": 9,
	} {
		got, ok := toggleKeyIndex(key)
		if !ok || got != want {
			t.Fatalf("toggleKeyIndex(%q) = %d, %v; want %d, true", key, got, ok, want)
		}
	}
	if _, ok := toggleKeyIndex("x"); ok {
		t.Fatal("non-toggle key should not match")
	}
}

func TestModelToggleGitPanelKey(t *testing.T) {
	state := daemon.NewState()
	m := New(state)
	if m.showGitPanel {
		t.Fatal("git panel should start hidden")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if !next.(Model).showGitPanel {
		t.Fatal("g should toggle git panel on")
	}
	after, _ := next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if after.(Model).showGitPanel {
		t.Fatal("second g should toggle git panel off")
	}
}

func TestModelToggleVerifierKey(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	m := New(state).WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	got := next.(Model).snapshot.Verifiers
	if len(got) != 2 {
		t.Fatalf("got %d verifiers, want 2", len(got))
	}
	if got[0].Disabled {
		t.Fatal("first verifier should remain enabled")
	}
	if !got[1].Disabled {
		t.Fatal("second verifier should be disabled")
	}
	if next.(Model).footerNotice != "Test disabled" {
		t.Fatalf("footerNotice = %q, want Test disabled", next.(Model).footerNotice)
	}
}

func TestModelFooterBrowserActionsUseSelectedVerifier(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	triggered := ""
	m := New(state).
		WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) }).
		WithTriggerVerifier(func(name string) { triggered = name })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.selectedVerifier != 1 {
		t.Fatalf("selectedVerifier = %d, want 1", m.selectedVerifier)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = next.(Model)
	if triggered != "Test" {
		t.Fatalf("triggered = %q, want Test", triggered)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	got := m.snapshot.Verifiers
	if !got[1].Disabled {
		t.Fatal("space should toggle selected verifier off")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.status == nil || m.status.verifier != "Test" {
		t.Fatalf("enter should open selected status wizard, got %#v", m.status)
	}
}

func TestRefreshOrbsSnapsThenSpringsToNewTarget(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Tests", Direction: "E", Distance: 0.2})
	m := New(state)

	// First tick must snap to the target — reconnecting to a running daemon
	// shouldn't paint a glide-from-center.
	next, _ := m.Update(tickMsg{})
	m = next.(Model)
	got := m.orbs["Tests"]
	if !got.armed {
		t.Fatal("first observation should arm the spring")
	}
	if got.x != 0.2 || got.y != 0 {
		t.Fatalf("first observation should snap to target; got (%v, %v) want (0.2, 0)", got.x, got.y)
	}

	// Now push the target outward; the spring should approach it over a few
	// ticks without overshooting past 1.0.
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Tests", Direction: "E", Distance: 0.9})
	prev := got.x
	for i := 0; i < 6; i++ {
		next, _ = m.Update(tickMsg{})
		m = next.(Model)
		cur := m.orbs["Tests"].x
		if cur < prev {
			t.Fatalf("spring should move outward on tick %d: prev=%v cur=%v", i, prev, cur)
		}
		prev = cur
	}
	if prev <= 0.5 {
		t.Fatalf("spring should have meaningfully approached the new target after 6 ticks; got %v", prev)
	}
}

func TestModelFooterNoticeExpires(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	m := New(state).WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = next.(Model)
	if m.footerNotice == "" {
		t.Fatal("toggle should set a transient footer notice")
	}
	for i := 0; i < footerNoticeTicks; i++ {
		next, _ = m.Update(tickMsg{})
		m = next.(Model)
	}
	if m.footerNotice != "" || m.footerNoticeUntil != 0 {
		t.Fatalf("footer notice should expire, notice=%q until=%d", m.footerNotice, m.footerNoticeUntil)
	}
}
