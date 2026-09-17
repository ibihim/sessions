package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newRootCmd() *cobra.Command {
	var interval time.Duration

	root := &cobra.Command{
		Use:   "sessions",
		Short: "Claude Code sessions — what you were doing, and where it is",
		Long: "Run bare, it is a picker: the last three days of sessions, " +
			"running ones first. Enter opens the one under the cursor — " +
			"focusing its window if it has one, opening a new one if it " +
			"does not — and f forks it into a new window. / filters by " +
			"title or path.\n\n" +
			"The picker rescans every --interval, so a session going idle " +
			"or a new one starting shows without a keypress.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// A floor, because the timer does not wait for the scan it
			// starts: at a millisecond, scans would pile up faster than
			// they finish.
			if interval != 0 && interval < time.Second {
				return fmt.Errorf("--interval %s is too short: 1s at least, or 0 to turn rescanning off", interval)
			}
			dir, err := sessions.DefaultRoot()
			if err != nil {
				return err
			}
			return runPicker(cmd.Context(), dir, interval)
		},
	}

	// Local rather than persistent: it means nothing to the verbs, and
	// list has an --interval of its own, for --watch.
	root.Flags().DurationVar(&interval, "interval", 30*time.Second,
		"rescan the picker every (0 to never)")

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
