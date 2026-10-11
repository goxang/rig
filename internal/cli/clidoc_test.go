package cli

import (
	stdflag "flag"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var update = stdflag.Bool("update", false, "rewrite docs/cli.md")

// TestCLIDoc keeps docs/cli.md the command tree; go test ./internal/cli -run CLIDoc -update rewrites it.
func TestCLIDoc(t *testing.T) {
	var b strings.Builder
	b.WriteString("# CLI\n\nEvery command; `rig <command> --help` has its flags. Generated from the code.\n\n| command | what it does |\n|---|---|\n")
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			if s.Hidden || s.Name() == "help" || s.Name() == "completion" {
				continue
			}
			if s.Runnable() {
				use := strings.TrimSpace(strings.TrimPrefix(s.UseLine(), "rig "))
				use = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(use, "[flags]")), " ")
				b.WriteString("| `" + strings.ReplaceAll(use, "|", `\|`) + "` | " + strings.ReplaceAll(s.Short, "|", `\|`) + " |\n")
			}
			walk(s)
		}
	}
	walk(newRoot())
	if *update {
		if err := os.WriteFile("../../docs/cli.md", []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile("../../docs/cli.md"); string(got) != b.String() {
		t.Fatal("docs/cli.md is stale: go test ./internal/cli -run CLIDoc -update")
	}
}

// A flag shorthand a command shares with a persistent one panics only when that command runs.
func TestFlagsMerge(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s: %v", s.CommandPath(), r)
					}
				}()
				s.InheritedFlags()
			}()
			walk(s)
		}
	}
	walk(newRoot())
}
