package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

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
			if err := backupSelf(); err != nil {
				fmt.Fprintf(os.Stderr, "rig: could not back up current version (rollback unavailable): %v\n", err)
			}
			cmd := exec.Command(sh, "-c", "curl -fsSL https://raw.githubusercontent.com/goxang/rig/main/install.sh | sh")
			cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
			cmd.Env = os.Environ()
			if err := cmd.Run(); err != nil {
				return err
			}
			removeOldBinaries()
			return nil
		},
	}
}

func previousDir() (string, error) {
	cfg, err := os.UserConfigDir()
	return filepath.Join(cfg, "rig", "previous"), err
}

// previousBinary returns the single saved binary, named rig-<version>.
func previousBinary() (string, error) {
	dir, err := previousDir()
	if err != nil {
		return "", err
	}
	m, _ := filepath.Glob(filepath.Join(dir, "rig-*"))
	if len(m) == 0 {
		return "", fmt.Errorf("no previous version saved (run rig upgrade first)")
	}
	return m[0], nil
}

// backupSelf saves the running binary as the one previous version, replacing any older backup.
func backupSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := previousDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return copyExecutable(self, filepath.Join(dir, "rig-"+Version))
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst) // rename, not write: the running binary may be replaced
}

func rollbackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "restore the rig version that was installed before the last upgrade (run again to go forward)",
		RunE: func(*cobra.Command, []string) error {
			prev, err := previousBinary()
			if err != nil {
				return err
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if real, err := filepath.EvalSymlinks(self); err == nil {
				self = real
			}
			ver := strings.TrimPrefix(filepath.Base(prev), "rig-")
			staged := self + ".rollback"
			if err := copyExecutable(prev, staged); err != nil {
				return fmt.Errorf("stage %s: %w", ver, err)
			}
			if err := backupSelf(); err != nil { // so a second rollback undoes this one
				os.Remove(staged)
				return fmt.Errorf("save current version: %w", err)
			}
			if err := os.Rename(staged, self); err != nil {
				os.Remove(staged)
				return fmt.Errorf("restore %s: %w", ver, err)
			}
			fmt.Printf("rig: rolled back %s → %s\n", Version, ver)
			return nil
		},
	}
}

// installDir mirrors install.sh: RIG_INSTALL_DIR, else /usr/local/bin when writable, else ~/.local/bin.
func installDir() string {
	if d := os.Getenv("RIG_INSTALL_DIR"); d != "" {
		return d
	}
	if f, err := os.CreateTemp("/usr/local/bin", ".rig-w"); err == nil {
		f.Close()
		os.Remove(f.Name())
		return "/usr/local/bin"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

// removeOldBinaries deletes the other rig binaries on PATH and the running one, now that the
// installer put the new one in installDir.
func removeOldBinaries() {
	keep, err := filepath.EvalSymlinks(filepath.Join(installDir(), "rig"))
	if err != nil {
		return
	}
	old := map[string]bool{}
	if self, err := os.Executable(); err == nil {
		old[self] = true
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" {
			old[filepath.Join(d, "rig")] = true
		}
	}
	for p := range old {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real == keep {
			continue
		}
		if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if os.Remove(p) == nil {
			fmt.Printf("rig: removed old binary %s\n", p)
		}
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
