package hud

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/verifier"
)

func samplePickerVerifiers(n int) []verifier.Verifier {
	dirs := []string{"N", "E", "S", "W", "NE", "SE"}
	out := make([]verifier.Verifier, n)
	for i := 0; i < n; i++ {
		out[i] = verifier.Verifier{
			Name:      string(rune('A' + i)),
			Direction: dirs[i%len(dirs)],
			Command:   []string{"true"},
		}
	}
	return out
}

func sendKey(t *testing.T, m tea.Model, k string) tea.Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return next
}

func sendNamedKey(t *testing.T, m tea.Model, k tea.KeyType) tea.Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return next
}

func TestPickerDefaultsAllSelected(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(5))
	if p.SelectedCount() != 5 {
		t.Fatalf("default selected: got %d, want 5", p.SelectedCount())
	}
}

func TestPickerEnterRequiresMin(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(6))
	// turn off two so we still have 4 -> should confirm
	m := sendKey(t, p, " ")
	m = sendNamedKey(t, m, tea.KeyDown)
	m = sendKey(t, m, " ")
	m = sendNamedKey(t, m, tea.KeyEnter)
	pm := m.(PickerModel)
	if !pm.Confirmed() {
		t.Fatalf("expected confirmed at 4 selected, got count=%d", pm.SelectedCount())
	}
	if got := len(pm.Selection()); got != 4 {
		t.Fatalf("selection len: got %d, want 4", got)
	}
}

func TestPickerEnterBelowMinIsRejected(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(6))
	// toggle all off -> 0 selected, below min=1
	m := sendKey(t, p, "a")
	m = sendNamedKey(t, m, tea.KeyEnter)
	pm := m.(PickerModel)
	if pm.Confirmed() {
		t.Fatalf("should not confirm with %d selected", pm.SelectedCount())
	}
	if pm.errMsg == "" {
		t.Fatal("expected error message when submitting under min")
	}
}

func TestPickerToggleAll(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(5))
	m := sendKey(t, p, "a") // all on -> all off
	if m.(PickerModel).SelectedCount() != 0 {
		t.Fatalf("after first 'a': got %d, want 0", m.(PickerModel).SelectedCount())
	}
	m = sendKey(t, m, "a") // all off -> all on
	if m.(PickerModel).SelectedCount() != 5 {
		t.Fatalf("after second 'a': got %d, want 5", m.(PickerModel).SelectedCount())
	}
}

func TestPickerAbort(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(5))
	m := sendKey(t, p, "q")
	pm := m.(PickerModel)
	if !pm.Aborted() {
		t.Fatal("q should mark picker aborted")
	}
	if pm.Confirmed() {
		t.Fatal("aborted picker should not be confirmed")
	}
}

func TestPickerSelectionPreservesOrder(t *testing.T) {
	p := NewPicker(samplePickerVerifiers(6))
	// deselect index 1 and 3
	m := sendNamedKey(t, p, tea.KeyDown)
	m = sendKey(t, m, " ")
	m = sendNamedKey(t, m, tea.KeyDown)
	m = sendNamedKey(t, m, tea.KeyDown)
	m = sendKey(t, m, " ")
	m = sendNamedKey(t, m, tea.KeyEnter)
	got := m.(PickerModel).Selection()
	want := []string{"A", "C", "E", "F"}
	if len(got) != len(want) {
		t.Fatalf("selection len: got %d (%v), want %d", len(got), got, len(want))
	}
	for i, v := range got {
		if v.Name != want[i] {
			t.Errorf("selection[%d]: got %q, want %q", i, v.Name, want[i])
		}
	}
}
