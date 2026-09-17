package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newFindCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "find <text>",
		Short: "Find a session by something you typed in it",
		Long: "Searches the prompts you typed, excluding assistant replies — " +
			"you remember your own words. <text> is a plain substring: " +
			"matched anywhere in a prompt, ignoring case, with no regex " +
			"or wildcards — quote it if it contains spaces. Sessions " +
			"whose transcript has aged out are skipped: they cannot be " +
			"resumed or read.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog, all, err := loadCatalog(cmd)
			if err != nil {
				return err
			}
			hits, err := catalog.Search(all, args[0])
			if err != nil {
				if len(hits) == 0 {
					return err
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
			}

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), "sessions", hits)
			}
			return writeHitsText(cmd, hits)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of text")

	return cmd
}

func writeHitsText(cmd *cobra.Command, hits []sessions.Hit) error {
	out := cmd.OutOrStdout()
	if len(hits) == 0 {
		fmt.Fprintln(out, "No matching sessions.")
		return nil
	}
	// Each hit prints as two lines: the session, then the prompt that
	// matched. tabwriter ends a block at any line with fewer cells, so the
	// rows are aligned together into a buffer first — interleaving the
	// prompts directly would align every hit in isolation, against nothing.
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTOOL\tSTATUS\tWS\tAGE\tMSGS\tTITLE\tWHERE")
	for _, h := range hits {
		s := h.Session
		title := s.Title
		if title == "" {
			title = s.Name
		}
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			shortID(s), s.Tool(), status(s), workspace(s), age(s.EndedAt), s.Messages,
			truncate(title, 52), where(s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	color := colorEnabled()
	rows := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	header, at := rows[0], whereAt(rows[0])
	if color {
		header = dimLine(header)
	}
	fmt.Fprintln(out, header)
	for i, h := range hits {
		row := rows[i+1]
		// The prompt echo dims as one piece with its count: it is context
		// for the row above, not a row of its own.
		echo := "    ⤷ " + truncate(h.Prompt, 72)
		if h.Matches > 1 {
			echo += fmt.Sprintf("  (+%d more)", h.Matches-1)
		}
		if color {
			row = paintRow(row, h.Session, at)
			echo = dimLine(echo)
		}
		fmt.Fprintln(out, row)
		fmt.Fprintln(out, echo)
	}
	return nil
}
