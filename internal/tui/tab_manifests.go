package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/MohammadmahdiAhmadi/rig/internal/viz"
	"github.com/MohammadmahdiAhmadi/rig/manifest"
)

// manifestsTab browses any folder of manifests: objects by kind, how each one relates to the rest, what is wrong.
type manifestsTab struct {
	dirs    []string
	set     *manifest.Set
	err     string
	filter  string
	sel     int
	offset  int
	issues  bool
	yamlOff int
}

type manifestsMsg struct {
	gen int
	set *manifest.Set
	err error
}

func (t *manifestsTab) name() string { return "Manifests" }
func (t *manifestsTab) typing() bool { return false }
func (t *manifestsTab) hints() [][2]string {
	return [][2]string{{"/", "filter"}, {"i", "issues"}, {"d", "folder"}, {"r", "rescan"}, {"J/K", "yaml"}}
}

func (t *manifestsTab) open(m *model) tea.Cmd {
	t.dirs = m.app.ManifestDirs()
	return t.scan(m)
}

func (t *manifestsTab) refresh(m *model) tea.Cmd { return nil }

func (t *manifestsTab) scan(m *model) tea.Cmd {
	dirs, gen := t.dirs, m.gen
	return func() tea.Msg {
		set, err := manifest.Scan(dirs...)
		return manifestsMsg{gen: gen, set: set, err: err}
	}
}

func (t *manifestsTab) visible() []*manifest.Object {
	if t.set == nil {
		return nil
	}
	var out []*manifest.Object
	for _, o := range t.set.Objects {
		if t.filter == "" || strings.Contains(strings.ToLower(o.ID()), strings.ToLower(t.filter)) {
			out = append(out, o)
		}
	}
	return out
}

func (t *manifestsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case manifestsMsg:
		if msg.gen == m.gen {
			t.set, t.err = msg.set, ""
			if msg.err != nil {
				t.err = msg.err.Error()
			}
		}
	case tea.KeyMsg:
		if listKeys(msg, &t.sel, len(t.visible())) {
			t.yamlOff = 0
			return nil
		}
		switch msg.String() {
		case "/":
			m.ask("filter", t.filter, func(v string) tea.Cmd {
				t.filter, t.sel, t.offset = v, 0, 0
				return nil
			})
		case "i":
			t.issues = !t.issues
		case "r":
			return t.scan(m)
		case "d":
			m.ask("folders (space separated)", strings.Join(t.dirs, " "), func(v string) tea.Cmd {
				t.dirs = strings.Fields(v)
				t.sel, t.offset = 0, 0
				return t.scan(m)
			})
		case "J":
			t.yamlOff += 5
		case "K":
			t.yamlOff = max(0, t.yamlOff-5)
		}
	}
	return nil
}

func (t *manifestsTab) view(m *model, w, h int) string {
	if t.err != "" {
		return panel("manifests", sRed.Render(t.err), w, h, true)
	}
	if t.set == nil {
		return panel("manifests", sDim.Render("scanning "+strings.Join(t.dirs, ", ")+"…"), w, h, true)
	}
	objs := t.visible()
	counts := map[string]int{}
	for _, i := range t.set.Issues {
		counts[i.Level]++
	}
	summary := fmt.Sprintf("%d objects · %d files · ", len(t.set.Objects), len(t.set.Files())) +
		sRed.Render(fmt.Sprintf("%d errors", counts["error"])) + " " + sAmber.Render(fmt.Sprintf("%d warnings", counts["warn"]))
	if t.issues {
		return panel("issues · "+summary, t.issueList(t.set.Issues, w-4), w, h, true)
	}

	lw := min(48, w*2/5)
	var rows [][]string
	issuesOf := map[string]int{}
	for _, i := range t.set.Issues {
		if i.Level != "info" {
			issuesOf[i.Object]++
		}
	}
	for _, o := range objs {
		mark := ""
		if n := issuesOf[o.ID()]; n > 0 {
			mark = sAmber.Render(fmt.Sprintf("⚠%d", n))
		}
		rows = append(rows, []string{kindColor(o.Kind), o.Name, mark})
	}
	t.sel = min(t.sel, max(0, len(rows)-1))
	t.offset = scroll(t.sel, t.offset, h-3, len(rows))
	title := "objects"
	if t.filter != "" {
		title += " · " + t.filter
	}
	list := panel(title, table([]string{"KIND", "NAME", ""}, []int{14, lw - 24, 4}, rows, t.sel, t.offset, h-2), lw, h, true)
	rw := w - lw
	if len(objs) == 0 {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, panel(summary, sDim.Render("no objects"), rw, h, false))
	}
	o := objs[t.sel]
	graph := viz.RelationGraph(o.ID(), rels(t.set.In(o.ID()), true), rels(t.set.Out(o.ID()), false))
	graphH := min(lipgloss.Height(graph)+3, h/2)
	var mine []manifest.Issue
	for _, i := range t.set.Issues {
		if i.Object == o.ID() {
			mine = append(mine, i)
		}
	}
	top := panel("relations · "+summary, sDim.Render(o.File)+"\n"+graph, rw, graphH, false)
	issH := 0
	var iss string
	if len(mine) > 0 {
		issH = min(len(mine)+2, 6)
		iss = panel("issues", t.issueList(mine, rw-4), rw, issH, false)
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(masked(o))
	lines := strings.Split(buf.String(), "\n")
	off := min(t.yamlOff, max(0, len(lines)-1))
	yamlBox := panel("yaml", colorYAML(strings.Join(lines[off:], "\n")), rw, h-graphH-issH, false)
	parts := []string{top}
	if issH > 0 {
		parts = append(parts, iss)
	}
	parts = append(parts, yamlBox)
	return lipgloss.JoinHorizontal(lipgloss.Top, list, lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func (t *manifestsTab) issueList(is []manifest.Issue, w int) string {
	var b strings.Builder
	for _, i := range is {
		lvl := map[string]string{"error": sRed.Render("error"), "warn": sAmber.Render("warn "), "info": sDim.Render("note ")}[i.Level]
		b.WriteString(lvl + " " + padRight(i.Object, 34) + " " + i.Text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func rels(es []manifest.Edge, incoming bool) []viz.Rel {
	var out []viz.Rel
	for _, e := range es {
		n := e.To
		if incoming {
			n = e.From
		}
		out = append(out, viz.Rel{Node: n, Label: e.Rel, Missing: e.Missing})
	}
	return out
}

var kindColors = map[string]lipgloss.Color{
	"Deployment": "#5794F2", "StatefulSet": "#5794F2", "DaemonSet": "#5794F2", "Job": "#8AB8FF", "CronJob": "#8AB8FF",
	"Service": "#73BF69", "Ingress": "#96D98D", "ConfigMap": "#F2CC0C", "Secret": "#FF780A",
	"HorizontalPodAutoscaler": "#B877D9", "PersistentVolumeClaim": "#FFB357",
}

func kindColor(k string) string {
	short := map[string]string{"HorizontalPodAutoscaler": "HPA", "PersistentVolumeClaim": "PVC", "PodDisruptionBudget": "PDB", "ServiceAccount": "SA"}[k]
	if short == "" {
		short = k
	}
	c, ok := kindColors[k]
	if !ok {
		return sDim.Render(short)
	}
	return lipgloss.NewStyle().Foreground(c).Render(short)
}

// colorYAML tints keys so a manifest reads at a glance.
func colorYAML(s string) string {
	key := lipgloss.NewStyle().Foreground(lipgloss.Color("#8AB8FF"))
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(l, " -")
		if k, v, ok := strings.Cut(trimmed, ":"); ok && !strings.Contains(k, " ") {
			b.WriteString(l[:len(l)-len(trimmed)] + key.Render(k) + ":" + v + "\n")
			continue
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// masked hides Secret values; the rest of the object shows as written.
func masked(o *manifest.Object) any {
	if o.Kind != "Secret" {
		return o.Node
	}
	var m map[string]any
	if o.Decode(&m) != nil {
		return o.Node
	}
	for _, k := range []string{"data", "stringData"} {
		if d, ok := m[k].(map[string]any); ok {
			for kk := range d {
				d[kk] = "••••••"
			}
		}
	}
	return m
}
