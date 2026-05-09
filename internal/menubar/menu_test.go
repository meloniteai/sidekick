package menubar

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uriahlevy/hud/internal/ipc"
)

func TestRenderJSONIncludesCompactStatusAndActions(t *testing.T) {
	b, err := RenderJSON(ipc.StatusReply{
		Goal:            "ship the menu bar HUD",
		Version:         "test",
		OverallDistance: 0.42,
		Verifiers: []ipc.VerifierStatus{
			{Name: "Architect", Direction: "N", Distance: 0.2, Reason: "shape is coherent"},
			{Name: "Test", Direction: "E", Distance: 0.8, Reason: "tests still running", Running: true},
		},
	})
	if err != nil {
		t.Fatalf("RenderJSON returned error: %v", err)
	}

	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload is not json: %v", err)
	}
	if p.Title != "HUD running" {
		t.Fatalf("title = %q, want running status", p.Title)
	}

	var sawGoal, sawVerifier, sawTrigger, sawStop, sawQuit bool
	for _, item := range p.Items {
		switch {
		case strings.Contains(item.Title, "ship the menu bar HUD"):
			sawGoal = true
		case strings.Contains(item.Title, "Architect | N | d=0.20"):
			sawVerifier = true
		case item.Action == actionTrigger && item.Enabled:
			sawTrigger = true
		case item.Action == actionStop && item.Enabled:
			sawStop = true
		case item.Action == actionQuit && item.Enabled:
			sawQuit = true
		}
	}
	if !sawGoal || !sawVerifier || !sawTrigger || !sawStop || !sawQuit {
		t.Fatalf("payload missing expected rows/actions: goal=%v verifier=%v trigger=%v stop=%v quit=%v",
			sawGoal, sawVerifier, sawTrigger, sawStop, sawQuit)
	}
}
