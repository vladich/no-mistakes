package cli

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
)

func newAxiCleanupCmd() *cobra.Command {
	var runID string
	var discard bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove one finished run's retained scratch worktree",
		Long: "Worktrees survive success, failure, timeout, cancellation and daemon restart.\n" +
			"Only an explicit caller cleanup request removes them. The run must be finished.\n" +
			"Dirty or untracked files require --discard-uncommitted; review and preserve them first.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id := strings.TrimSpace(runID)
			if id == "" {
				return emitError(cmd, 1, "cleanup requires --run <id>")
			}
			env, err := openAxiEnvWithOptions(axiEnvOptions{ensureDaemonConn: true, explicitRunID: id})
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			defer env.close()
			var result ipc.CleanupRunResult
			if err := env.client.Call(ipc.MethodCleanupRun, &ipc.CleanupRunParams{RunID: id, DiscardUncommitted: discard}, &result); err != nil {
				return emitError(cmd, 1, fmt.Sprintf("cleanup run: %v", err))
			}
			emitDoc(cmd, toon.Field{Key: "run", Value: result.RunID}, toon.Field{Key: "worktree", Value: result.Path}, toon.Field{Key: "removed", Value: result.Removed})
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "exact finished run whose worktree the caller requests to remove")
	cmd.Flags().BoolVar(&discard, "discard-uncommitted", false, "explicitly discard staged, unstaged and untracked files")
	return cmd
}
