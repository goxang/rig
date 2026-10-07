package main

import (
	"os"
	"runtime/debug"

	_ "github.com/goxang/rig/adapters/all"
	"github.com/goxang/rig/internal/cli"
	"github.com/goxang/rig/internal/tui"
)

var version = "dev"

// buildVersion is the release's -ldflags version, else what go records: the module version of a
// go install, or the commit (and whether the tree had changes) of a build from a checkout.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if version != "dev" || !ok {
		return version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, at, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+changes"
			}
		}
	}
	if rev == "" {
		return version
	}
	return "dev-" + rev[:min(12, len(rev))] + dirty + " (" + at + ")"
}

func main() {
	cli.Version = buildVersion()
	cli.UI = tui.Run
	cli.Resume = tui.Resume
	cli.AIChat = tui.Chat
	cli.PickSession = tui.PickSession
	cli.CloseUISession = tui.CloseSession
	cli.Pinned, cli.Unpin = tui.Pinned, tui.Unpin
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
