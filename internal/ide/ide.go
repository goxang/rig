// Package ide writes the project's services into GoLand and VS Code, so they can be watched, stopped
// and debugged from the IDE while rig runs them.
package ide

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

// Result says what Write wrote.
type Result struct {
	Services, Debug int
	Folders         []string
	VSCode          string
	// Dashboard is false when the GoLand Services view could not be set to show the configs.
	Dashboard bool
}

// Write puts every service into GoLand, in one folder per section of the Services screen: a run
// config that starts it and follows its logs (stop stops it), and for Go services a Go Remote config
// that brings its debugger up first. VS Code gets the attach configs.
func Write(a *engine.App, names []string) (Result, error) {
	var r Result
	self, err := os.Executable()
	if err != nil {
		return r, err
	}
	rig := shellQuote(self) + " -f " + shellQuote(a.Spec.File) + " -e " + shellQuote(a.Env.Name)
	dir := filepath.Join(a.Spec.Dir, ".idea", "runConfigurations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "rig_*.xml"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	sections := a.Spec.SectionMap()
	folders := map[string]bool{}
	var goSvcs []string
	for _, n := range names {
		s := a.Spec.Services[n]
		folder := "rig · " + sections[n]
		attach := "exec " + rig + " attach " + shellQuote(n)
		switch {
		case s.Role == spec.RoleInfra:
			// shared with other environments: GoLand's stop only stops following it
			folder, attach = "rig · infra", attach+" --keep"
		case sections[n] == "":
			folder = "rig · other"
		}
		folders[folder] = true
		isGo := s.Build != nil && s.Build.Go != ""
		_, local := runtimeOf(a, n).(core.ProcessLocator)
		if isGo && !local {
			attach += " --debug" // the debugger's port-forward lives as long as this
		}
		files := map[string]string{n: shConfig(n, folder, attach)}
		if isGo {
			goSvcs = append(goSvcs, n)
			before := ""
			if local {
				helper := n + " · dlv"
				files[helper] = shConfig(helper, "rig · debugger starters", "exec "+rig+" debug --detach "+shellQuote(n))
				before = helper
			}
			files[n+" · debug"] = remoteConfig(n+" · debug", folder, core.DebugPort(n), before)
		}
		for name, xml := range files {
			if err := os.WriteFile(filepath.Join(dir, fileName(name)), []byte(xml), 0o644); err != nil {
				return r, err
			}
		}
		r.Services++
	}
	r.Debug = len(goSvcs)
	for f := range folders {
		r.Folders = append(r.Folders, f)
	}
	sort.Strings(r.Folders)
	r.Dashboard = showInServices(filepath.Join(a.Spec.Dir, ".idea", "workspace.xml")) == nil
	if len(goSvcs) > 0 {
		if r.VSCode, err = vscode(a.Spec.Dir, goSvcs); err != nil {
			return r, err
		}
	}
	return r, nil
}

func runtimeOf(a *engine.App, svc string) core.Runtime {
	rt, _, err := a.Owner(svc)
	if err != nil {
		return nil
	}
	return rt
}

func fileName(config string) string {
	return "rig_" + strings.NewReplacer("-", "_", ".", "_", " ", "_", "·", "", "/", "_").Replace(config) + ".xml"
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"$`\\&;|<>()*?[]#~!{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shConfig runs script in GoLand's Run console (so the Services view shows its output and its stop
// button signals it), not in a terminal.
func shConfig(name, folder, script string) string {
	return fmt.Sprintf(`<component name="ProjectRunConfigurationManager">
  <configuration default="false" name="%s" type="ShConfigurationType" folderName="%s">
    <option name="SCRIPT_TEXT" value="%s" />
    <option name="INDEPENDENT_SCRIPT_PATH" value="true" />
    <option name="SCRIPT_PATH" value="" />
    <option name="SCRIPT_OPTIONS" value="" />
    <option name="INDEPENDENT_SCRIPT_WORKING_DIRECTORY" value="true" />
    <option name="SCRIPT_WORKING_DIRECTORY" value="$PROJECT_DIR$" />
    <option name="INDEPENDENT_INTERPRETER_PATH" value="true" />
    <option name="INTERPRETER_PATH" value="/bin/bash" />
    <option name="INTERPRETER_OPTIONS" value="" />
    <option name="EXECUTE_IN_TERMINAL" value="false" />
    <option name="EXECUTE_SCRIPT_FILE" value="false" />
    <envs />
    <method v="2" />
  </configuration>
</component>
`, html.EscapeString(name), html.EscapeString(folder), html.EscapeString(script))
}

// remoteConfig attaches GoLand's debugger to the service's stable port, after running before (the
// config that starts the debugger server) when there is one.
func remoteConfig(name, folder string, port int, before string) string {
	method := `<method v="2" />`
	if before != "" {
		method = fmt.Sprintf(`<method v="2">
      <option name="RunConfigurationTask" enabled="true" run_configuration_name="%s" run_configuration_type="ShConfigurationType" />
    </method>`, html.EscapeString(before))
	}
	return fmt.Sprintf(`<component name="ProjectRunConfigurationManager">
  <configuration default="false" name="%s" type="GoRemoteDebugConfigurationType" factoryName="Go Remote" folderName="%s" port="%d">
    <option name="disconnectOption" value="LEAVE" />
    <disconnect value="LEAVE" />
    %s
  </configuration>
</component>
`, html.EscapeString(name), html.EscapeString(folder), port, method)
}

var dashboardTypes = []string{"ShConfigurationType", "GoRemoteDebugConfigurationType"}

// showInServices adds the configs' types to GoLand's Services view (workspace.xml RunDashboard).
// GoLand writes workspace.xml from memory when it closes, so an open GoLand may undo this.
func showInServices(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	var missing []string
	for _, t := range dashboardTypes {
		if !strings.Contains(text, `<option value="`+t+`" />`) {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var opts strings.Builder
	for _, t := range missing {
		opts.WriteString("\n        <option value=\"" + t + "\" />")
	}
	comp := strings.Index(text, `<component name="RunDashboard">`)
	switch {
	case comp < 0:
		end := strings.LastIndex(text, "</project>")
		if end < 0 {
			return errors.New("workspace.xml has no </project>")
		}
		text = text[:end] + `  <component name="RunDashboard">
    <option name="configurationTypes">
      <set>` + opts.String() + `
      </set>
    </option>
  </component>
` + text[end:]
	default:
		rest := text[comp:]
		end := strings.Index(rest, "</component>")
		types := strings.Index(rest, `<option name="configurationTypes">`)
		set := strings.Index(rest, "<set>")
		if types < 0 || set < types || end < set {
			at := comp + len(`<component name="RunDashboard">`)
			text = text[:at] + `
    <option name="configurationTypes">
      <set>` + opts.String() + `
      </set>
    </option>` + text[at:]
			break
		}
		at := comp + set + len("<set>")
		text = text[:at] + opts.String() + text[at:]
	}
	return os.WriteFile(path, []byte(text), 0o644)
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

// GoLand finds the GoLand launcher: $RIG_GOLAND, PATH, then where JetBrains Toolbox and snap put it.
func GoLand() string {
	if p := os.Getenv("RIG_GOLAND"); p != "" {
		return p
	}
	for _, n := range []string{"goland", "goland.sh"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local/share/JetBrains/Toolbox/scripts/goland"),
		filepath.Join(home, ".local/share/JetBrains/Toolbox/apps/goland/bin/goland"),
		filepath.Join(home, ".local/share/JetBrains/Toolbox/apps/goland/bin/goland.sh"),
		"/snap/bin/goland",
		"/Applications/GoLand.app/Contents/MacOS/goland",
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// Open brings the project up in GoLand (a running GoLand just focuses it), detached from rig.
func Open(dir string) error {
	bin := GoLand()
	if bin == "" {
		return errors.New("GoLand not found: put goland on PATH or set RIG_GOLAND")
	}
	cmd := exec.Command(bin, dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
