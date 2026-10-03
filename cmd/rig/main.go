package main

import (
	"os"

	_ "github.com/goxang/rig/adapters/all"
	"github.com/goxang/rig/internal/cli"
	"github.com/goxang/rig/internal/tui"
)

var version = "dev"

func main() {
	cli.Version = version
	cli.UI = tui.Run
	cli.Resume = tui.Resume
	cli.Sessions = func(dir string) ([][]string, error) {
		ss, err := tui.ListSessions(dir)
		var rows [][]string
		for _, s := range ss {
			rows = append(rows, []string{s.ID, s.Saved.Format("2006-01-02 15:04"), s.Summary()})
		}
		return rows, err
	}
	os.Exit(cli.Execute())
}
