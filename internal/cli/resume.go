package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

func resumeCommand() *cobra.Command {
	var closeIDs bool
	c := &cobra.Command{
		Use:   "resume [session|last]",
		Short: "continue an AI conversation or a saved UI session (S); without an id, pick one (d closes it)",
		Long: `Without an id, lists the project's AI conversations and saved UI sessions to pick from: enter
continues one, d closes it. With an id, continues that one; --close forgets the ids given.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			file := g.file
			if file == "" {
				f, err := spec.Find(".")
				if err != nil {
					return err
				}
				file = f
			}
			dir := filepath.Dir(file)
			data, err := spec.DataDir(dir)
			if err != nil {
				return err
			}
			if closeIDs {
				for _, id := range args {
					if err := closeSession(dir, data, id); err != nil {
						return err
					}
					fmt.Println("closed " + id)
				}
				return nil
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			if id == "" || id == "last" {
				rows, err := sessionRows(dir, data)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					fmt.Println("no sessions yet: rig ai starts a conversation, S in the UI saves a session")
					return nil
				}
				switch {
				case id == "last":
					sort.SliceStable(rows, func(i, j int) bool { return rows[i][2] > rows[j][2] })
					id = rows[0][0]
				case brief || PickSession == nil || !term.IsTerminal(int(os.Stdout.Fd())):
					printTable(os.Stdout, []string{"SESSION", "KIND", "SAVED", "WHAT"}, rows)
					return nil
				default:
					i, err := PickSession(rows, func(i int) error { return closeSession(dir, data, rows[i][0]) })
					if err != nil || i < 0 {
						return err
					}
					id = rows[i][0]
				}
			}
			openEnv := func(env string) (*engine.App, error) {
				if g.env != "" {
					env = g.env
				}
				a, err := engine.Open(file, env)
				if err == nil {
					a.Confirmed = g.yes
				}
				return a, err
			}
			if s, err := ai.LoadSession(data, id); err == nil {
				a, err := openEnv(s.Env)
				if err != nil {
					return err
				}
				defer a.Close()
				return AIChat(cmd.Context(), a, s.ID)
			}
			return Resume(cmd.Context(), openEnv, dir, id)
		},
	}
	c.Flags().BoolVar(&closeIDs, "close", false, "forget the sessions given instead of continuing one")
	return c
}

// sessionRows are AI conversations, then saved UI sessions: id, kind, saved, what.
func sessionRows(dir, data string) ([][]string, error) {
	var rows [][]string
	ss, err := ai.ListSessions(data)
	if err != nil {
		return nil, err
	}
	for _, s := range ss {
		rows = append(rows, []string{s.ID, "ai", s.Updated.Format("2006-01-02 15:04"), s.Summary()})
	}
	ui, err := Sessions(dir)
	for _, r := range ui {
		rows = append(rows, []string{r[0], "ui", r[1], r[2]})
	}
	return rows, err
}

func closeSession(dir, data, id string) error {
	if err := ai.CloseSession(data, id); err == nil {
		return nil
	}
	if CloseUISession == nil {
		return errors.New("no session " + id)
	}
	return CloseUISession(dir, id)
}
