package cmd

import (
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
			data, err := ipc.MarshalData(ipc.GoalData{Goal: strings.Join(args, " ")})
			if err != nil {
				return err
			}
			_, err = ipc.Send(ipc.Request{Type: ipc.TypeGoal, Data: data})
			return err
		},
	}
}
