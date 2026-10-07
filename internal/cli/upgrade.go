package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/spec"
)

func migrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: fmt.Sprintf("rewrite the project file to the version this rig understands (%d)", spec.CurrentVersion),
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

func upgradeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "update rig to the latest release (or $RIG_VERSION), using the same installer as the install script",
		RunE: func(*cobra.Command, []string) error {
			sh, err := exec.LookPath("sh")
			if err != nil {
				return fmt.Errorf("upgrade needs sh: %w", err)
			}
			if os.Getenv("RIG_VERSION") == "" && os.Getenv("RIG_DOWNLOAD_URL") == "" {
				if latest := latestRelease(); latest != "" && latest == Version {
					fmt.Printf("rig %s is already the latest version\n", Version)
					return nil
				}
			}
			cmd := exec.Command(sh, "-c", "curl -fsSL https://raw.githubusercontent.com/goxang/rig/main/install.sh | sh")
			cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
			cmd.Env = os.Environ()
			return cmd.Run()
		},
	}
}

// latestRelease reads the tag GitHub's releases/latest redirects to; "" when it cannot tell.
func latestRelease() string {
	c := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Head("https://github.com/goxang/rig/releases/latest")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" {
		return path.Base(loc)
	}
	return ""
}

func uninstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "remove the rig binary (add --config to also drop ~/.config/rig)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dropConfig, _ := cmd.Flags().GetBool("config")
			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate rig binary: %w", err)
			}
			cfgDir, cfgErr := os.UserConfigDir()
			if cfgErr == nil {
				cfgDir = cfgDir + "/rig"
			}
			fmt.Println("this removes:")
			fmt.Println("  " + bin)
			if dropConfig && cfgErr == nil {
				fmt.Println("  " + cfgDir + " (AI setup, keys)")
			}
			if !g.yes {
				return fmt.Errorf("repeat with --yes to confirm")
			}
			if err := os.Remove(bin); err != nil {
				return fmt.Errorf("remove %s: %w", bin, err)
			}
			if dropConfig && cfgErr == nil {
				if err := os.RemoveAll(cfgDir); err != nil {
					return fmt.Errorf("remove %s: %w", cfgDir, err)
				}
			}
			fmt.Println("rig: uninstalled")
			return nil
		},
	}
	cmd.Flags().Bool("config", false, "also remove ~/.config/rig (AI setup, keys)")
	return cmd
}
