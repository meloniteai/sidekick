package verifier

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestVerifyEchoScript(t *testing.T) {
	v := Verifier{
		Name:      "Echo",
		Direction: "N",
		Command:   []string{"sh", "-c", `echo '{"distance": 0.42, "reason": "ok"}'`},
		Timeout:   5 * time.Second,
	}
	r, err := v.Verify(context.Background(), Session{Goal: "x"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Distance != 0.42 || r.Reason != "ok" {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyClampsDistance(t *testing.T) {
	v := Verifier{Name: "X", Command: []string{"sh", "-c", `echo '{"distance": 9.0, "reason": "high"}'`}}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 1.0 {
		t.Fatalf("expected clamp to 1.0, got %v", r.Distance)
	}
}

func TestVerifyTakesLastLine(t *testing.T) {
	v := Verifier{
		Name:    "Y",
		Command: []string{"sh", "-c", `echo "logging line"; echo '{"distance": 0.1, "reason": "tail"}'`},
	}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Reason != "tail" {
		t.Fatalf("expected reason=tail, got %q", r.Reason)
	}
}

func TestVerifyBadJSON(t *testing.T) {
	v := Verifier{Name: "Z", Command: []string{"sh", "-c", "echo not-json"}}
	_, err := v.Verify(context.Background(), Session{})
	if err == nil || !strings.Contains(err.Error(), "bad json") {
		t.Fatalf("expected bad json error, got %v", err)
	}
}

func TestVerifyTimeout(t *testing.T) {
	v := Verifier{Name: "Slow", Command: []string{"sh", "-c", "sleep 5"}, Timeout: 100 * time.Millisecond}
	_, err := v.Verify(context.Background(), Session{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
