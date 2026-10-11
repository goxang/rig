package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/spec"
)

func migrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: fmt.Sprintf("rewrite rig.yaml to version %d", spec.CurrentVersion),
		RunE: func(*cobra.Command, []string) error {
			file := g.file
			if file == "" {
				var err error
				if file, err = spec.Find("."); err != nil {
					return err
				}
			}
			from, changed, err := spec.Migrate(file)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Printf("%s: already version %d, nothing to do\n", file, spec.CurrentVersion)
				return nil
			}
			fmt.Printf("%s %s: version %d → %d\n", green("✓"), file, from, spec.CurrentVersion)
			return nil
		},
	}
}

func lintCommand() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "list rig.yaml values its imports, includes or plugins already give (--fix removes them)",
		RunE: func(*cobra.Command, []string) error {
			file := g.file
			if file == "" {
				var err error
				if file, err = spec.Find("."); err != nil {
					return err
				}
			}
			rs, fixed, err := spec.Redundant(file)
			if err != nil {
				return err
			}
			for _, r := range rs {
				fmt.Printf("%s:%d %s %s\n", filepath.Base(file), r.Line, r.Path, dim("same as "+r.From.String()))
			}
			if len(rs) == 0 {
				fmt.Println(green("✓") + " nothing redundant")
				return nil
			}
			if fix {
				if err := os.WriteFile(file, fixed, 0o644); err != nil {
					return err
				}
				fmt.Printf("%s removed %d\n", green("✓"), len(rs))
				return nil
			}
			return fmt.Errorf("%d redundant values (rig lint --fix removes them)", len(rs))
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "remove them from rig.yaml (comments stay; layout is re-indented)")
	cmd.Flags().Bool("redundant", true, "values the layers below rig.yaml already give (the only check so far)")
	return cmd
}
