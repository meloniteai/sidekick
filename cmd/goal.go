package cmd

import (
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/ipc"
)

func newGoalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "goal <text>",
		Short: "Set the active session goal",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			worktree, baseRef := resolveGoalAnchor()
			data, err := ipc.MarshalData(ipc.GoalData{
				Goal:     strings.Join(args, " "),
				Worktree: worktree,
				BaseRef:  baseRef,
			})
			if err != nil {
				return err
			}
			_, err = ipc.Send(ipc.Request{Type: ipc.TypeGoal, Data: data})
			return err
		},
	}
}

// resolveGoalAnchor returns the cwd's git toplevel and HEAD SHA so the
// daemon can re-anchor the session to the caller's perspective when
// `hud goal` is invoked from a worktree. Empty values fall through and
// the daemon keeps the existing anchor in place.
func resolveGoalAnchor() (worktree, baseRef string) {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", ""
	}
	worktree = strings.TrimSpace(string(top))
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return worktree, ""
	}
	return worktree, strings.TrimSpace(string(head))
}
