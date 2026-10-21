package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/host"
)

// keyPresets map a key to a global command; keys left out go to the screen. Keys every preset
// shares (q, @, ?, tab, ...) stay in the global switch.
var keyPresets = map[string]map[string]string{
	"rig": {":": "palette", "ctrl+k": "search", "ctrl+e": "env", ".": "menu", "T": "task"},
}

// preset is keys: from the project (.rig/local.yaml), else ~/.config/rig/config.yaml, else rig.
func (m *model) preset() string {
	if k := m.app.Spec.Keys; k != "" {
		return k
	}
	dir, _ := os.UserConfigDir()
	var c struct{ Keys string }
	if b, err := os.ReadFile(filepath.Join(dir, "rig", "config.yaml")); err == nil {
		_ = yaml.Unmarshal(b, &c)
	}
	return cmp.Or(c.Keys, "rig")
}

// globalKey runs the preset's command for s, or a view's own key; ok is false when neither has it.
func (m *model) globalKey(s string) (tea.Cmd, bool) {
	if c, ok := keyPresets[m.preset()][s]; ok {
		return m.command(c), true
	}
	for i, t := range m.tabs {
		if v, ok := t.(*viewTab); ok && v.v.Key == s {
			return m.jump(i), true
		}
	}
	return nil, false
}

// command runs a palette line: the CLI's grammar, with what the UI does itself handled here and
// the rest run as `rig <line>` with its output in a box.
func (m *model) command(line string) tea.Cmd {
	f := strings.Fields(line)
	if len(f) == 0 {
		return nil
	}
	verb, args := f[0], f[1:]
	switch verb {
	case "palette":
		if m.locked() {
			return nil
		}
		m.ask(":", "", func(line string) tea.Cmd {
			if line = strings.TrimSpace(line); line != "" {
				m.cmdHist = append(slices.DeleteFunc(m.cmdHist, func(h string) bool { return h == line }), line)
				m.cmdHist = m.cmdHist[max(0, len(m.cmdHist)-100):]
			}
			return m.command(line)
		})
		m.prompt.recall = slices.Clone(m.cmdHist)
		m.prompt.histAt = len(m.prompt.recall)
		m.prompt.input.SetSuggestions(slices.Clone(m.cmdHist))
		return nil
	case "search":
		m.search()
		return nil
	case "menu":
		m.menu(nil)
		return nil
	case "q", "quit":
		return m.quit()
	case "reload":
		return m.reload()
	case "save":
		id, err := m.saveSession()
		if err != nil {
			m.setStatus("save session: "+err.Error(), true)
		} else {
			m.setStatus("session "+id+" saved: rig opens on it from now on, as you leave it (rig --fresh starts clean)", false)
		}
		return nil
	case "env":
		m.pickEnv()
		return nil
	case "envinfo":
		return m.fetchEnvInfo()
	case "ns", "namespace":
		return m.pickNamespace()
	case "kubeconfig":
		return m.fetchKubeconfig(cmp.Or(m.kubeEnv, m.app.Env.Name))
	case "theme":
		m.pickTheme()
		return nil
	case "task":
		if len(args) == 0 {
			m.pickTask("")
			return nil
		}
		if t, ok := m.app.Tasks()[args[0]]; ok {
			m.askTaskArgs(args[0], t.Args, nil)
			return nil
		}
	case "plugins":
		return m.pluginsScreen()
	case "open":
		// open <screen> [words]: the words filter a view's table or board (open Jira PIN-12)
		want := strings.ToLower(strings.Join(args, " "))
		if want == "plugins" {
			return m.pluginsScreen()
		}
		for i, t := range m.tabs {
			if strings.ToLower(t.name()) == want {
				return m.jump(i)
			}
		}
		if len(args) > 1 {
			for i, t := range m.tabs {
				if v, ok := t.(*viewTab); ok && strings.EqualFold(t.name(), args[0]) {
					return batch(m.jump(i), v.lookup(m, strings.Join(args[1:], " ")))
				}
			}
		}
		m.setStatus("no screen "+want, true)
		return nil
	case "service":
		if s := m.servicesTab(); s != nil && len(args) > 0 {
			s.focusService(m, args[0])
			return m.jump(m.tabIndex(s))
		}
	case "start", "stop", "restart", "deploy", "build", "logs":
		if s := m.servicesTab(); s != nil && len(args) > 0 {
			return s.ops(m, map[string]string{"start": "s", "stop": "x", "restart": "r", "deploy": "d", "build": "b", "logs": "l"}[verb], args)
		}
	}
	if t, ok := m.all["cmd:"+verb]; ok {
		return m.transientScreen(t)
	}
	return m.runRig(line, f)
}

func (m *model) servicesTab() *servicesTab {
	for _, t := range m.tabs {
		if s, ok := t.(*servicesTab); ok {
			return s
		}
	}
	return nil
}

func (m *model) tabIndex(t tab) int {
	for i, x := range m.tabs {
		if x == t {
			return i
		}
	}
	return m.active
}

// runRig runs the line as a rig command in this project and environment.
func (m *model) runRig(line string, args []string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	a, gen := m.app, m.gen
	return m.do(":"+line, func(ctx context.Context) error {
		c := exec.CommandContext(ctx, exe, append([]string{"-f", a.Spec.File, "-e", a.Env.Name}, args...)...)
		c.Dir = a.Spec.Dir
		c.Env = append(os.Environ(), "RIG_BRIEF=1", "NO_COLOR=1")
		out, err := c.CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		if err != nil {
			last := text
			if i := strings.LastIndex(text, "\n"); i >= 0 {
				last = text[i+1:]
			}
			return errors.New(cmp.Or(last, err.Error()))
		}
		if text != "" && program != nil {
			box := &envBox{env: ":" + line}
			inTable := false
			for l := range strings.SplitSeq(alignTabs(text), "\x00") {
				l, row := strings.CutPrefix(l, "\x01")
				if row && !inTable {
					l = sTitle.Render(l)
				}
				inTable = row
				box.lines = append(box.lines, envLine{text: "  " + l})
			}
			program.Send(envInfoMsg{gen: gen, box: box})
		}
		return nil
	})
}

// alignTabs lines up the tab-separated rows the CLI prints when piped; rows come back marked with
// \x01 so the first of each table can be drawn as its header, lines separated by \x00.
func alignTabs(text string) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for l := range strings.SplitSeq(text, "\n") {
		if strings.Contains(l, "\t") {
			fmt.Fprint(tw, "\x01"+l+"\x00\n")
		} else {
			tw.Flush()
			b.WriteString(l + "\x00\n")
		}
	}
	tw.Flush()
	return strings.TrimSuffix(strings.ReplaceAll(b.String(), "\x00\n", "\x00"), "\x00")
}

// search is ctrl+k: screens, services, tasks, actions and plugins in one filtered list; each item
// is a palette line.
func (m *model) search() {
	var items, desc []string
	add := func(line, d string) { items, desc = append(items, line), append(desc, d) }
	for _, t := range m.tabs {
		add("open "+t.name(), "screen")
	}
	add("plugins", "the plugins in use: info, logs, add, on, off, upgrade")
	for _, s := range m.app.Spec.ServiceNames() {
		add("service "+s, "service")
	}
	for _, t := range m.app.TaskNames() {
		add("task "+t, cmp.Or(m.app.TaskHelp(t), "task"))
	}
	for _, a := range m.eng().Actions() {
		if c, verb, ok := strings.Cut(a, "."); ok {
			add("do "+c+" "+verb, "action")
		}
	}
	ms, _ := host.Discover(m.app.Spec.Dir, m.app.Spec.Plugins)
	for _, p := range ms {
		add("plugin info "+p.Name, cmp.Or(p.Description, "plugin"))
	}
	m.pick("search", items, desc, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		return m.command(chosen[0])
	})
}

// menu is . or right-click: the screen's keys as a list, each one pressed when chosen. A right-click
// opens it as a dropdown where the pointer is, so the eye stays on what was clicked.
func (m *model) menu(at *[2]int) {
	t := m.tabs[m.active]
	var items, keys []string
	key := map[string]string{}
	for _, h := range t.hints() {
		if strings.ContainsAny(h[0], " /↑↓←→") && h[0] != "/" || h[1] == "" || key[h[1]] != "" {
			continue
		}
		key[h[1]] = h[0]
		items, keys = append(items, h[1]), append(keys, h[0])
	}
	if len(items) == 0 {
		return
	}
	m.pick(t.name(), items, keys, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		return t.update(m, keyOf(key[chosen[0]]))
	})
	m.picker.at = at
}

// keyFor is the preset's key for a command, or "" when it has none.
func (m *model) keyFor(command string) string {
	for k, c := range keyPresets[m.preset()] {
		if c == command {
			return k
		}
	}
	return ""
}

// commandAsking runs a palette line, first asking for the ${ask} in it.
func (m *model) commandAsking(label, line string) tea.Cmd {
	if !strings.Contains(line, "${ask}") {
		return m.command(line)
	}
	m.ask(label, "", func(v string) tea.Cmd { return m.command(strings.ReplaceAll(line, "${ask}", v)) })
	return nil
}

// commandMsg runs a palette line on the UI loop, as a confirmed answer does.
type commandMsg string
