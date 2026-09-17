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
		Short: "Claude Code and Codex sessions — what you were doing, and where it is",
		Long: "Run bare, it is a picker: the last three days of sessions, " +
			"running ones first. Enter opens the one under the cursor — " +
			"focusing a running terminal's identified window or resuming a saved " +
			"session in a new one — and f forks it into a new window. / filters by " +
			"title or path.\n\n" +
			"The picker rescans every --interval, so a session going idle " +
			"or a new one starting shows without a keypress.",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			_, err := selectedProvider(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// A floor, because the timer does not wait for the scan it
			// starts: at a millisecond, scans would pile up faster than
			// they finish.
			if interval != 0 && interval < time.Second {
				return fmt.Errorf("--interval %s is too short: 1s at least, or 0 to turn rescanning off", interval)
			}
			catalog, err := sessions.DefaultCatalog()
			if err != nil {
				return err
			}
			provider, err := selectedProvider(cmd)
			if err != nil {
				return err
			}
			return runPicker(cmd.Context(), catalog, provider, interval)
		},
	}

	// Local rather than persistent: it means nothing to the verbs, and
	// list has an --interval of its own, for --watch.
	root.Flags().DurationVar(&interval, "interval", 30*time.Second,
		"rescan the picker every (0 to never)")
	root.PersistentFlags().String("provider", "all", "include all, claude, or codex sessions")

	root.AddCommand(newListCmd())
	root.AddCommand(newFindCmd())
	root.AddCommand(newPromptsCmd())
	root.AddCommand(newOpenCmd())

	return root
}

func selectedProvider(cmd *cobra.Command) (sessions.Provider, error) {
	flag := cmd.Flag("provider")
	if flag == nil {
		return sessions.All, nil
	}
	return sessions.ParseProvider(flag.Value.String())
}

func loadCatalog(cmd *cobra.Command) (sessions.Catalog, []sessions.Session, error) {
	c, err := sessions.DefaultCatalog()
	if err != nil {
		return c, nil, err
	}
	provider, err := selectedProvider(cmd)
	if err != nil {
		return c, nil, err
	}
	all, err := c.Scan(cmd.Context(), provider)
	if err != nil {
		if len(all) == 0 {
			return c, nil, err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
	}
	return c, all, nil
}

// Execute runs the root command. The context carries interrupt handling,
// which --watch needs to stop redrawing on ctrl-c.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
