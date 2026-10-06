package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/internal/scaffold"
)

// initDeep scans every folder, lets the user pick what to keep, and writes it to rig.yaml: a new
// file, or the picks added to the services, tests and manifests of the one there.
func initDeep(ctx context.Context, dry, all bool) error {
	start := time.Now()
	fmt.Fprint(os.Stderr, "scanning every folder… ")
	cands, err := scaffold.Deep(ctx, ".")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d candidates in %s\n", len(cands), time.Since(start).Round(time.Millisecond))
	if len(cands) == 0 {
		fmt.Println("nothing runnable found: rig init writes a starting rig.yaml anyway")
		return nil
	}
	existing, _ := os.ReadFile("rig.yaml")
	have := haveNames(existing)
	var fresh []scaffold.Candidate
	for _, c := range cands {
		if !have[c.Name] {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) == 0 {
		fmt.Println("rig.yaml already has everything found")
		return nil
	}
	picked := fresh
	if !all {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("rig init --deep picks on a terminal; --all takes every candidate")
		}
		if picked, err = pickCandidates(fresh); err != nil || len(picked) == 0 {
			if err == nil {
				fmt.Println("nothing picked, rig.yaml unchanged")
			}
			return err
		}
	}
	p := scaffold.PlanOf(".", picked)
	var out []byte
	if existing == nil {
		if out, err = p.YAML(); err != nil {
			return err
		}
	} else if out, err = mergeInto(existing, p); err != nil {
		return err
	}
	if dry {
		_, err := os.Stdout.Write(out)
		return err
	}
	if err := os.WriteFile("rig.yaml", out, 0o644); err != nil {
		return err
	}
	verb := "wrote"
	if existing != nil {
		verb = "added to"
	}
	fmt.Printf("%s %s rig.yaml: %d services, %d test suites, %d manifest folders\n", green("✓"), verb, len(p.Services), len(p.Suites), len(p.Manifests))
	fmt.Println("  check ports, health checks and passwords; then: rig up, or rig for the control plane")
	return nil
}

// haveNames are the service and suite names rig.yaml already has.
func haveNames(raw []byte) map[string]bool {
	out := map[string]bool{}
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
		Tests    map[string]yaml.Node `yaml:"tests"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return out
	}
	for n := range doc.Services {
		out[n] = true
	}
	for n := range doc.Tests {
		out[n] = true
	}
	return out
}

// mergeInto adds the plan's services, suites and manifest folders to rig.yaml as text, at the end
// of each block, so the file's comments, anchors and layout stay as they are.
func mergeInto(raw []byte, p *scaffold.Plan) ([]byte, error) {
	gen, err := p.YAML()
	if err != nil {
		return nil, err
	}
	blocks := topBlocks(gen)
	text := string(raw)
	for _, key := range []string{"services", "tests"} {
		body := blocks[key]
		if strings.TrimSpace(body) == "" || strings.HasPrefix(strings.TrimSpace(body), "{") {
			continue
		}
		text = appendToBlock(text, key, body)
	}
	if len(p.Manifests) > 0 {
		text = strings.TrimRight(text, "\n") + "\n# found by rig init --deep: add them to manifests: above\n# manifests: [" + strings.Join(p.Manifests, ", ") + "]\n"
	}
	var check yaml.Node
	if err := yaml.Unmarshal([]byte(text), &check); err != nil {
		return nil, fmt.Errorf("adding to rig.yaml would break it (%w); run rig init --deep --dry-run and paste by hand", err)
	}
	return []byte(text), nil
}

// topBlocks splits a YAML file into its top-level keys' bodies (the indented lines under each).
func topBlocks(raw []byte) map[string]string {
	out := map[string]string{}
	key := ""
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if line != "" && line[0] != ' ' && line[0] != '#' && line[0] != '\n' {
			k, _, _ := strings.Cut(line, ":")
			key = k
			continue
		}
		if key != "" {
			out[key] += line
		}
	}
	return out
}

// appendToBlock puts body at the end of top-level key's block, or as a new block at the end.
func appendToBlock(text, key, body string) string {
	lines := strings.SplitAfter(text, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			at = i
			continue
		}
		if at >= 0 && l != "" && l[0] != ' ' && l[0] != '#' && l[0] != '\n' {
			// back over blank and comment lines that belong to the next block
			end := i
			for end > at+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(lines[end-1], "#")) {
				end--
			}
			return strings.Join(lines[:end], "") + "  # found by rig init --deep\n" + body + strings.Join(lines[end:], "")
		}
	}
	if at >= 0 {
		return strings.TrimRight(text, "\n") + "\n  # found by rig init --deep\n" + body
	}
	return strings.TrimRight(text, "\n") + "\n" + key + ":\n" + body
}

// ---- the picker ----

type candPicker struct {
	all      []scaffold.Candidate
	marked   map[int]bool
	sel, top int
	filter   string
	typing   bool
	done     bool
	h        int
}

func pickCandidates(cs []scaffold.Candidate) ([]scaffold.Candidate, error) {
	m := &candPicker{all: cs, marked: map[int]bool{}, h: 24}
	res, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr)).Run()
	if err != nil {
		return nil, err
	}
	p := res.(*candPicker)
	if !p.done {
		return nil, nil
	}
	var out []scaffold.Candidate
	for i, c := range p.all {
		if p.marked[i] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (p *candPicker) Init() tea.Cmd { return nil }

func (p *candPicker) visible() []int {
	var out []int
	f := strings.ToLower(p.filter)
	for i, c := range p.all {
		if f == "" || strings.Contains(strings.ToLower(c.Kind+" "+c.Name+" "+c.Where), f) {
			out = append(out, i)
		}
	}
	return out
}

func (p *candPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.h = msg.Height
	case tea.KeyMsg:
		vis := p.visible()
		if p.typing {
			switch msg.Type {
			case tea.KeyEnter:
				p.typing = false
			case tea.KeyEsc:
				p.typing, p.filter = false, ""
			case tea.KeyBackspace:
				if p.filter != "" {
					p.filter = p.filter[:len(p.filter)-1]
				}
			case tea.KeyRunes, tea.KeySpace:
				p.filter += string(msg.Runes)
			}
			p.sel = 0
			return p, nil
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return p, tea.Quit
		case "enter":
			p.done = true
			return p, tea.Quit
		case "up", "k":
			p.sel = max(0, p.sel-1)
		case "down", "j":
			p.sel = min(len(vis)-1, p.sel+1)
		case "pgup":
			p.sel = max(0, p.sel-10)
		case "pgdown":
			p.sel = min(len(vis)-1, p.sel+10)
		case " ", "x":
			if p.sel < len(vis) {
				p.marked[vis[p.sel]] = !p.marked[vis[p.sel]]
				p.sel = min(len(vis)-1, p.sel+1)
			}
		case "a":
			on := false
			for _, i := range vis {
				on = on || !p.marked[i]
			}
			for _, i := range vis {
				p.marked[i] = on
			}
		case "/":
			p.typing = true
		}
	}
	return p, nil
}

var (
	pickKind = map[string]string{"go": "Go main", "goland": "GoLand", "python": "Python", "node": "Node", "rust": "Rust", "java": "Java", "dotnet": ".NET",
		"ruby": "Ruby", "dockerfile": "Dockerfile", "compose": "compose", "manifest": "manifest", "helm": "Helm chart", "container": "container", "tests": "tests"}
	pAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Bold(true)
	pDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#7B7F85"))
	pCursor = lipgloss.NewStyle().Background(lipgloss.Color("#2F5A9E")).Bold(true)
)

func (p *candPicker) View() string {
	vis := p.visible()
	n := 0
	for _, on := range p.marked {
		if on {
			n++
		}
	}
	var b bytes.Buffer
	b.WriteString(pAccent.Render(" rig init --deep") + pDim.Render(fmt.Sprintf("  %d found · %d picked · space marks · a all · / filter · enter writes rig.yaml · esc quits", len(p.all), n)) + "\n")
	if p.typing || p.filter != "" {
		b.WriteString(" filter: " + p.filter)
		if p.typing {
			b.WriteString("▏")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
	}
	rows := max(3, p.h-3)
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+rows {
		p.top = p.sel - rows + 1
	}
	last := ""
	for k := p.top; k < len(vis) && k < p.top+rows; k++ {
		c := p.all[vis[k]]
		box := "[ ]"
		if p.marked[vis[k]] {
			box = pAccent.Render("[x]")
		}
		kind := pickKind[c.Kind]
		if kind == last {
			kind = ""
		} else {
			last = kind
		}
		line := fmt.Sprintf(" %s %-11s %-34s %s", box, kind, truncName(c.Name, 34), pDim.Render(c.Where))
		if k == p.sel {
			line = pCursor.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func truncName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
