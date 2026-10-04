package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// ideCommand writes one remote-debug configuration per Go service on its stable debug port, so
// `D` in the UI (or rig debug) followed by the IDE's "rig: <service>" config is the whole flow.
func ideCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ide [service|group...]",
		Short: "write GoLand and VS Code \"rig: <service>\" remote-debug configs (D in the UI, then run the config)",
		RunE: withApp(func(_ context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			var svcs []string
			for _, n := range names {
				if s := a.Spec.Services[n]; s.Build != nil && s.Build.Go != "" {
					svcs = append(svcs, n)
				}
			}
			if len(svcs) == 0 {
				return errors.New("no Go services (build.go) among the targets")
			}
			dir := a.Spec.Dir
			if err := goland(dir, svcs); err != nil {
				return err
			}
			vs, err := vscode(dir, svcs)
			if err != nil {
				return err
			}
			fmt.Printf("%s %d GoLand configs in .idea/runConfigurations, VS Code ones in %s\n", green("✓"), len(svcs), vs)
			fmt.Println(dim("  D on a service in rig (or rig debug <service>), then run \"rig: <service>\" in the IDE"))
			return nil
		}),
	}
}

const golandConfig = `<component name="ProjectRunConfigurationManager">
  <configuration default="false" name="rig: %s" type="GoRemoteDebugConfigurationType" factoryName="Go Remote" folderName="rig" port="%d">
    <option name="disconnectOption" value="LEAVE" />
    <disconnect value="LEAVE" />
    <method v="2" />
  </configuration>
</component>
`

func goland(dir string, svcs []string) error {
	out := filepath.Join(dir, ".idea", "runConfigurations")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, s := range svcs {
		f := filepath.Join(out, "rig_"+strings.NewReplacer("-", "_", ".", "_").Replace(s)+".xml")
		if err := os.WriteFile(f, []byte(fmt.Sprintf(golandConfig, s, core.DebugPort(s))), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// vscode writes the configs between "// rig:begin" and "// rig:end" at the top of launch.json's
// configurations, as text: launch.json is JSONC (comments, trailing commas), which VS Code reads and
// encoding/json does not, so the rest of the file is left byte for byte.
func vscode(dir string, svcs []string) (string, error) {
	path := filepath.Join(dir, ".vscode", "launch.json")
	var block strings.Builder
	block.WriteString("\n    // rig:begin (rig ide rewrites this block)\n")
	for _, s := range svcs {
		c, _ := json.Marshal(map[string]any{"name": "rig: " + s, "type": "go", "request": "attach", "mode": "remote", "host": "127.0.0.1", "port": core.DebugPort(s)})
		block.WriteString("    " + string(c) + ",\n")
	}
	block.WriteString("    // rig:end")
	raw, err := os.ReadFile(path)
	text := string(raw)
	switch {
	case errors.Is(err, os.ErrNotExist):
		text = "{\n  \"version\": \"0.2.0\",\n  \"configurations\": [" + block.String() + "\n  ]\n}\n"
	case err != nil:
		return "", err
	default:
		if b, e := strings.Index(text, "\n    // rig:begin"), strings.Index(text, "// rig:end"); b >= 0 && e > b {
			text = text[:b] + text[e+len("// rig:end"):]
		}
		i := strings.Index(text, `"configurations"`)
		j := strings.Index(text[max(i, 0):], "[")
		if i < 0 || j < 0 {
			return "", fmt.Errorf("%s has no \"configurations\" list", path)
		}
		at := i + j + 1
		text = text[:at] + block.String() + text[at:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(dir, path)
	return rel, os.WriteFile(path, []byte(text), 0o644)
}
