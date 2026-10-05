package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/goxang/rig/internal/scaffold"
)

// initProject writes rig.yaml from what the directory already has; it guesses, the user edits.
func initProject(ctx context.Context, force, dry bool, with []string) error {
	if _, err := os.Stat("rig.yaml"); err == nil && !force && !dry {
		return fmt.Errorf("rig.yaml exists (--force overwrites it, --dry-run prints what init would write)")
	}
	p, err := scaffold.Detect(ctx, ".", scaffold.Options{With: with})
	if err != nil {
		return err
	}
	raw, err := p.YAML()
	if err != nil {
		return err
	}
	if dry {
		_, err := os.Stdout.Write(raw)
		return err
	}
	if err := os.WriteFile("rig.yaml", raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s rig.yaml: %d services, %d components\n", green("✓"), len(p.Services), len(p.Components))
	for _, n := range p.Notes {
		fmt.Println("  · " + n)
	}
	if len(p.Services) == 0 {
		fmt.Println("  nothing found to run: add services by hand, or infrastructure with rig init --force --with " + strings.Join(scaffold.Presets(), ","))
	}
	fmt.Println("\nnext:")
	fmt.Println("  rig env          the environments and what each one runs on")
	fmt.Println("  rig up           start everything in dependency order")
	fmt.Println("  rig              the control plane")
	return nil
}
