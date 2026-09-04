package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "sessions",
		Short: "Claude Code sessions — what you were doing, and where it is",
		Long: "Run bare, it is a picker: the last three days of sessions, " +
			"running ones first. Enter opens the one under the cursor — " +
			"focusing its window if it has one, opening a new one if it " +
			"does not — and f forks it into a new window. / filters by " +
			"title or path.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := sessions.DefaultRoot()
			if err != nil {
				return err
			}
			return runPicker(cmd.Context(), dir)
		},
	}

	root.AddCommand(newListCmd())
	root.AddCommand(newFindCmd())
	root.AddCommand(newPromptsCmd())
	root.AddCommand(newOpenCmd())

	return root
}

// Execute runs the root command. The context carries interrupt handling,
// which --watch needs to stop redrawing on ctrl-c.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
