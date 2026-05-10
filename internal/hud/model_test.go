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
