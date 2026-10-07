package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/docs"
	"github.com/goxang/rig/skills"
	"github.com/goxang/rig/spec"
)

func docsCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "docs [config|guide|design|manifests] [words...]",
		Short:   "rig's reference, from the binary: every rig.yaml key (config), CLI, screens and AI (guide), adapters (design), manifests; words print only the sections holding them",
		Example: "  rig docs config tasks\n  rig docs load generator   (no doc named: every doc)",
		RunE: func(_ *cobra.Command, args []string) error {
			names := []string{"config"}
			if len(args) > 0 {
				names = docs.Names
				if slices.Contains(docs.Names, strings.TrimSuffix(args[0], ".md")) {
					names, args = []string{strings.TrimSuffix(args[0], ".md")}, args[1:]
				}
			}
			query := strings.Join(args, " ")
			var out strings.Builder
			var heads []string
			for _, n := range names {
				raw, _ := docs.FS.ReadFile(n + ".md")
				if query == "" {
					out.Write(raw)
					continue
				}
				found, h := docs.Sections(string(raw), query)
				out.WriteString(found)
				for _, x := range h {
					heads = append(heads, n+": "+x)
				}
			}
			if out.Len() == 0 {
				return fmt.Errorf("no section holds %q; sections:\n%s", query, strings.Join(heads, "\n"))
			}
			_, err := os.Stdout.WriteString(out.String())
			return err
		},
	}
}

func schemaCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "print rig.yaml's JSON Schema (editors: " + spec.SchemaHeader + ")",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := os.Stdout.Write(spec.SchemaJSON)
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
