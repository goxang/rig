package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/docs"
	"github.com/goxang/rig/skills"
)

func docsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "docs [config|design|manifests]",
		Short: "rig's reference, from the binary: every rig.yaml key (config), adapters (design), manifests",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := "config"
			if len(args) == 1 {
				name = args[0]
			}
			raw, err := docs.FS.ReadFile(strings.TrimSuffix(name, ".md") + ".md")
			if err != nil {
				return fmt.Errorf("no doc %q: have config, design, manifests", name)
			}
			_, err = os.Stdout.Write(raw)
			return err
		},
	}
}

func skillCommand() *cobra.Command {
	var install bool
	c := &cobra.Command{
		Use:   "skill",
		Short: "the agent skill for working with rig: print it, or --install it into .claude/skills and .agents/skills",
		RunE: func(*cobra.Command, []string) error {
			if !install {
				fmt.Print(skills.Rig)
				return nil
			}
			for _, dir := range []string{".claude/skills/rig", ".agents/skills/rig"} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				p := filepath.Join(dir, "SKILL.md")
				if err := os.WriteFile(p, []byte(skills.Rig), 0o644); err != nil {
					return err
				}
				fmt.Println(green("✓") + " " + p)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&install, "install", false, "write it into this project for Claude Code, Codex, opencode and other agents")
	return c
}
