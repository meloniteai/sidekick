package daemon

import (
	"encoding/json"
	"testing"

	"github.com/uriahlevy/hud/internal/ipc"
)

func TestSocketDispatchRoutesGoalStatusExplainByCWD(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.UpsertVerifier(ipc.VerifierStatus{Name: "Shared", Distance: 0.7, Reason: "trunk"})
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		s.SetSessionBaseRef(anchor.BaseRef)
		s.UpsertVerifier(ipc.VerifierStatus{Name: "Shared", Distance: 0.2, Reason: "wt"})
		return s, nil
	})
	srv := &Server{registry: reg, handler: routeCaptureHandler{}}

	goalData, _ := ipc.MarshalData(ipc.GoalData{Goal: "worktree goal"})
	resp := srv.dispatch(ipc.Request{Type: ipc.TypeGoal, Cwd: wt, Data: goalData})
	if !resp.OK {
		t.Fatalf("goal dispatch failed: %s", resp.Error)
	}
	status := srv.dispatch(ipc.Request{Type: ipc.TypeStatus, Cwd: wt})
	if !status.OK {
		t.Fatalf("status dispatch failed: %s", status.Error)
	}
	var reply ipc.StatusReply
	if err := json.Unmarshal(status.Data, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Goal != "worktree goal" || reply.Worktree != wt {
		t.Fatalf("routed status = goal %q worktree %q, want worktree session", reply.Goal, reply.Worktree)
	}

	explainData, _ := ipc.MarshalData(ipc.ExplainData{Verifier: "Shared"})
	explain := srv.dispatch(ipc.Request{Type: ipc.TypeExplain, Cwd: wt, Data: explainData})
	if !explain.OK {
		t.Fatalf("explain dispatch failed: %s", explain.Error)
	}
	var v ipc.VerifierStatus
	if err := json.Unmarshal(explain.Data, &v); err != nil {
		t.Fatal(err)
	}
	if v.Reason != "wt" {
		t.Fatalf("explain reason = %q, want wt", v.Reason)
	}
}

func TestSocketDispatchWriteTouchesOnlyTargetSession(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		return s, nil
	})
	h := &writeCaptureHandler{files: map[string][]string{}}
	srv := &Server{registry: reg, handler: h}

	data, _ := ipc.MarshalData(ipc.WriteData{File: "a.go"})
	resp := srv.dispatch(ipc.Request{Type: ipc.TypeWrite, Cwd: wt, Data: data})
	if !resp.OK {
		t.Fatalf("write dispatch failed: %s", resp.Error)
	}
	if got := h.files[wt]; len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("worktree writes = %v, want [a.go]", got)
	}
	if got := h.files[trunk]; len(got) != 0 {
		t.Fatalf("trunk should not receive worktree write: %v", got)
	}
}

type routeCaptureHandler struct{}

func (routeCaptureHandler) OnWrite(*State, string) {}
func (routeCaptureHandler) OnGoal(s *State, goal string) {
	s.SetGoal(goal)
}

type writeCaptureHandler struct {
	files map[string][]string
}

func (h *writeCaptureHandler) OnWrite(s *State, file string) {
	h.files[s.SessionWorktree()] = append(h.files[s.SessionWorktree()], file)
}
func (h *writeCaptureHandler) OnGoal(*State, string) {}
