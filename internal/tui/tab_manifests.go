package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/manifest"
)

// manifestsTab browses any folder of manifests: objects by kind, or folders and files (t), how each
// object relates to the rest, what is wrong (i: the file's, I: all). Marked objects (space) or the
// selected one can be applied to the environment (a) or become a rig.yaml service (n).
type manifestsTab struct {
	dirs   []string
	set    *manifest.Set
	err    string
	filter string
	sel    int
	offset int
	issues string // "", "file" or "all"
	marked map[string]bool

	// fields is the selected object field by field; inFields gives it the keys, and edits save into the file
	fields    *jsonTree
	fieldsObj *manifest.Object
	fieldsID  string
	inFields  bool
	jumpPath  string // a search hit's field, selected once its object shows
	// viaSearch marks the object/field currently shown as reached from a search jump, so esc
	// from the fields view reopens the last search results instead of just backing out to the list
	viaSearch bool

	tree bool   // folders and files instead of every object
	cwd  string // the folder the tree shows
	file string // in the tree: the file whose objects are listed
	// svcOf caches serviceOf per object, until the next scan
	svcOf map[*manifest.Object]string
	// owners maps an object (file#Kind/name) to the service whose deploy applies it; the list shows
	// only those unless all is set (u), since folders often hold manifests nothing deploys
	owners map[string]string
	all    bool
	// hits is the search index, built once per scan and reused across searches
	hits    []manifestHit
	hitsSet *manifest.Set
	// cluster shows what runs in the namespace instead of the files (c toggles; on Kubernetes the
	// screen opens on it); files is the scan, kept for syncing a live object into its file
	cluster   bool
	files     *manifest.Set
	live      *manifest.Set
	liveStale bool
}

// liveLister is a runtime that lists the namespace's objects (Kubernetes, kind).
type liveLister interface {
	LiveObjects(ctx context.Context) ([]byte, error)
}

type manifestsLiveMsg struct {
	gen int
	set *manifest.Set
	err error
}

const liveFile = "cluster:"

func isLive(o *manifest.Object) bool { return strings.HasPrefix(o.File, liveFile) }

func (t *manifestsTab) fetchLive(m *model) tea.Cmd {
	l, ok := m.app.Runtime().(liveLister)
	if !ok {
		return nil
	}
	t.liveStale = false
	gen, ctx, where := m.gen, m.work(), liveFile+m.app.Env.Name
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		y, err := l.LiveObjects(c)
		if err != nil {
			return manifestsLiveMsg{gen: gen, err: err}
		}
		return manifestsLiveMsg{gen: gen, set: manifest.Parse(where, y)}
	}
}

// show puts the files or the cluster's objects on the list.
func (t *manifestsTab) show() {
	t.set = t.files
	if t.cluster {
		t.set = t.live
	}
	t.svcOf, t.fieldsObj = nil, nil
}

type manifestFoldersMsg struct {
	root    string
	folders map[string]int
}

// pickFolders offers every manifest folder of the project, the ones showing marked.
func (t *manifestsTab) pickFolders(m *model, msg manifestFoldersMsg) {
	if len(msg.folders) == 0 {
		m.setStatus("no manifests under "+msg.root, true)
		return
	}
	names := make([]string, 0, len(msg.folders))
	for d := range msg.folders {
		names = append(names, d)
	}
	sort.Strings(names)
	var desc, chosen []string
	for _, d := range names {
		desc = append(desc, fmt.Sprintf("%d files", msg.folders[d]))
		for _, cur := range t.dirs {
			if abs(cur) == filepath.Join(msg.root, d) {
				chosen = append(chosen, d)
			}
		}
	}
	m.pickMany("manifest folders to show (space marks, enter shows them)", names, desc, chosen, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		t.dirs = nil
		for _, d := range c {
			t.dirs = append(t.dirs, filepath.Join(msg.root, d))
		}
		t.sel, t.offset, t.cwd, t.file = 0, 0, "", ""
		return t.scan(m)
	})
}

type manifestsMsg struct {
	gen    int
	set    *manifest.Set
	owners map[string]string
	err    error
}

func objKey(o *manifest.Object) string { return abs(o.File) + "#" + o.ID() }

// manifestOwners finds, for each service, the objects its deploy applies: what the Kubernetes
// runtime would render, else its workload's bundle in set.
func manifestOwners(m *model, set *manifest.Set) map[string]string {
	out := map[string]string{}
	k, onK8s := m.k8s()
	for _, n := range m.app.Spec.ServiceNames() {
		s := m.app.Spec.Services[n]
		var objs []*manifest.Object
		if onK8s {
			objs, _, _ = k.Objects(s)
		} else {
			var sec struct {
				Workload string `yaml:"workload"`
			}
			_, _ = s.Section("k8s", &sec)
			if sec.Workload == "" {
				sec.Workload = n
			}
			if w := set.FindWorkload(sec.Workload); w != nil {
				objs = set.Bundle(w)
			}
		}
		for _, o := range objs {
			if _, taken := out[objKey(o)]; !taken {
				out[objKey(o)] = n
			}
		}
	}
	return out
}

// shown says whether the list includes o: every object with u, else those a service deploys.
func (t *manifestsTab) shown(o *manifest.Object) bool {
	if t.all || isLive(o) || len(t.owners) == 0 {
		return true
	}
	_, ok := t.owners[objKey(o)]
	return ok
}

// applier is a runtime that can apply manifests (Kubernetes).
type applier interface {
	ApplyManifests(ctx context.Context, objs []*manifest.Object) error
}

func (t *manifestsTab) name() string { return "Manifests" }
func (t *manifestsTab) typing() bool { return false }
func (t *manifestsTab) hints() [][2]string {
	if t.inFields {
		back := "back to objects"
		if t.viaSearch {
			back = "back to search"
		}
		return [][2]string{{"enter e", "edit field"}, {"a", "add field"}, {"D", "delete field"}, {"←→ space", "fold"}, {"+ - z", "expand all, fold all, toggle"}, {"y", "copy value"}, {"esc", back}}
	}
	if t.cluster {
		return [][2]string{{"c", "files instead of the cluster"}, {"→ tab", "edit fields (applied to the cluster)"}, {"v enter", "go to its service"}, {"e L", "edit on the cluster"},
			{"s", "sync into its file"}, {"/", "search fields and values"}, {"f", "filter"}, {"i/I", "issues"}, {"r", "reload"}, {"o", "editor"}}
	}
	return [][2]string{{"c", "what runs on the cluster"}, {"t ctrl+←→", "folders/objects"}, {"enter esc", "in/out"}, {"→ tab", "edit fields"}, {"v enter", "go to its service"}, {"e", "edit (saved into its file)"}, {"s", "sync file from the cluster"}, {"L", "edit on the cluster"},
		{"a", "apply"}, {"/", "search fields and values"}, {"f", "filter"}, {"space", "mark"}, {"n", "new service"}, {"i/I", "issues file/all"}, {"u", "also what no service deploys"}, {"d", "pick folders"}, {"r", "rescan"}, {"o", "editor"}}
}

func (t *manifestsTab) open(m *model) tea.Cmd {
	t.dirs = m.app.ManifestDirs()
	t.marked = map[string]bool{}
	_, t.cluster = m.app.Runtime().(liveLister)
	t.files, t.live, t.set = nil, nil, nil
	return batch(t.scan(m), t.fetchLive(m))
}

func (t *manifestsTab) refresh(m *model) tea.Cmd {
	if t.liveStale {
		return t.fetchLive(m)
	}
	return nil
}

func (t *manifestsTab) scan(m *model) tea.Cmd {
	dirs, gen := t.dirs, m.gen
	return func() tea.Msg {
		set, err := manifest.Scan(dirs...)
		if err != nil {
			return manifestsMsg{gen: gen, err: err}
		}
		return manifestsMsg{gen: gen, set: set, owners: manifestOwners(m, set)}
	}
}

// entry is a row of the list: an object, or in the tree a folder or a file.
type entry struct {
	obj   *manifest.Object
	dir   string // a folder (absolute)
	file  string // a file
	count int    // objects under a folder or in a file
}

func (e entry) id() string {
	switch {
	case e.obj != nil:
		return e.obj.File + "#" + e.obj.ID()
	case e.dir != "":
		return "dir:" + e.dir
	}
	return "file:" + e.file
}

func (t *manifestsTab) objects() []*manifest.Object {
	if t.set == nil {
		return nil
	}
	var out []*manifest.Object
	for _, o := range t.set.Objects {
		if t.tree && t.file != "" && o.File != t.file || !t.shown(o) {
			continue
		}
		if fuzzy(o.ID()+" "+o.File, t.filter) {
			out = append(out, o)
		}
	}
	return out
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// entries is what the list shows: objects, or the tree's folders and files under cwd.
func (t *manifestsTab) entries() []entry {
	if !t.tree || t.file != "" {
		var out []entry
		for _, o := range t.objects() {
			out = append(out, entry{obj: o})
		}
		return out
	}
	if t.set == nil {
		return nil
	}
	if t.cwd == "" {
		if len(t.dirs) == 1 {
			t.cwd = abs(t.dirs[0])
		} else {
			t.cwd = abs(m0(t.dirs))
		}
	}
	dirs, files := map[string]int{}, map[string]int{}
	for _, o := range t.set.Objects {
		if !t.shown(o) {
			continue
		}
		f := abs(o.File)
		rel, err := filepath.Rel(t.cwd, f)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if !fuzzy(rel, t.filter) {
			continue
		}
		if first, _, deeper := strings.Cut(rel, string(filepath.Separator)); deeper {
			dirs[filepath.Join(t.cwd, first)]++
		} else {
			files[o.File]++
		}
	}
	var out []entry
	for _, d := range sortedKeys(dirs) {
		out = append(out, entry{dir: d, count: dirs[d]})
	}
	for _, f := range sortedKeys(files) {
		out = append(out, entry{file: f, count: files[f]})
	}
	return out
}

// m0 is the folder every root shares.
func m0(dirs []string) string {
	if len(dirs) == 0 {
		return "."
	}
	common := abs(dirs[0])
	for _, d := range dirs[1:] {
		d = abs(d)
		for !strings.HasPrefix(d+string(filepath.Separator), common+string(filepath.Separator)) {
			common = filepath.Dir(common)
		}
	}
	return common
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (t *manifestsTab) current() (entry, bool) {
	es := t.entries()
	if t.sel < 0 || t.sel >= len(es) {
		return entry{}, false
	}
	return es[t.sel], true
}

// targets are the marked objects, else those the selection stands for (an object, or a file's).
func (t *manifestsTab) targets() []*manifest.Object {
	var out []*manifest.Object
	if len(t.marked) > 0 {
		for _, o := range t.set.Objects {
			if t.marked[o.File+"#"+o.ID()] || t.marked["file:"+o.File] {
				out = append(out, o)
			}
		}
		return out
	}
	e, ok := t.current()
	switch {
	case !ok:
	case e.obj != nil:
		out = append(out, e.obj)
	case e.file != "":
		for _, o := range t.set.Objects {
			if o.File == e.file {
				out = append(out, o)
			}
		}
	}
	return out
}

// fileInView is the file the issues key narrows to: the open file, the selected file or object's.
func (t *manifestsTab) fileInView() string {
	if t.tree && t.file != "" {
		return t.file
	}
	if e, ok := t.current(); ok {
		if e.obj != nil {
			return e.obj.File
		}
		return e.file
	}
	return ""
}

func (t *manifestsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case manifestFoldersMsg:
		t.pickFolders(m, msg)
	case manifestsMsg:
		if msg.gen == m.gen {
			t.files, t.err, t.owners = msg.set, "", msg.owners
			if msg.err != nil {
				t.err = msg.err.Error()
			}
			t.show()
		}
	case manifestsLiveMsg:
		if msg.gen == m.gen {
			if msg.err != nil {
				m.setStatus("cluster objects: "+msg.err.Error()+" · c shows the files", true)
				return nil
			}
			t.live = msg.set
			t.show()
		}
	case tea.KeyMsg:
		if t.issues != "" {
			if s := msg.String(); s == "esc" || s == "i" || s == "I" || s == "left" {
				t.issues = ""
			}
			return nil
		}
		if t.inFields && t.fields != nil {
			return t.fieldKey(m, msg)
		}
		if listKeys(msg, &t.sel, len(t.entries())) {
			t.viaSearch = false
			return nil
		}
		switch msg.String() {
		case "/":
			return t.search(m)
		case "f":
			m.ask("filter", t.filter, func(v string) tea.Cmd {
				t.filter, t.sel, t.offset = v, 0, 0
				return nil
			})
		case "u":
			t.all, t.sel, t.offset = !t.all, 0, 0
		case "c":
			if _, ok := m.app.Runtime().(liveLister); !ok {
				m.setStatus(m.app.Env.Name+" is not on Kubernetes: only the files", true)
				return nil
			}
			t.cluster, t.sel, t.offset, t.inFields, t.viaSearch = !t.cluster, 0, 0, false, false
			t.show()
			if t.cluster {
				m.setStatus("what runs in "+m.app.Env.Name+" · s writes an object into its file", false)
				return t.fetchLive(m)
			}
			m.setStatus("the manifest files · c shows what runs", false)
		case "t", "shift+left", "shift+right":
			if _, live := m.app.Runtime().(liveLister); live && msg.String() != "t" {
				cur := 1 + boolInt(t.tree)
				if t.cluster {
					cur = 0
				}
				return t.mode(m, (cur+map[bool]int{true: 2, false: 1}[msg.String() == "shift+left"])%3)
			}
			if t.cluster {
				m.setStatus("the cluster has no folders: c shows the files", false)
				return nil
			}
			t.tree, t.sel, t.offset, t.file, t.viaSearch = !t.tree, 0, 0, "", false
		case "v":
			return t.gotoService(m)
		case "enter", "right", "l", "tab":
			e, ok := t.current()
			if ok && e.obj != nil && msg.String() == "enter" {
				return t.gotoService(m)
			}
			if ok && e.obj != nil && t.fields != nil {
				t.inFields, t.viaSearch = true, false
				return nil
			}
			if !ok || !t.tree {
				return nil
			}
			switch {
			case e.dir != "":
				t.cwd, t.sel, t.offset = e.dir, 0, 0
			case e.file != "":
				t.file, t.sel, t.offset = e.file, 0, 0
			}
		case "esc", "left", "h", "backspace":
			switch {
			case t.filter != "":
				t.filter = ""
			case t.tree && t.file != "":
				t.file, t.sel, t.offset = "", 0, 0
			case t.tree && t.cwd != "" && t.cwd != filepath.Dir(t.cwd):
				t.cwd, t.sel, t.offset = filepath.Dir(t.cwd), 0, 0
			}
		case " ":
			if e, ok := t.current(); ok && e.dir == "" {
				t.marked[e.id()] = !t.marked[e.id()]
				if !t.marked[e.id()] {
					delete(t.marked, e.id())
				}
			}
		case "i":
			if t.fileInView() != "" {
				t.issues = "file"
			}
		case "I":
			t.issues = "all"
		case "a":
			if t.cluster {
				m.setStatus("these already run: an edit here applies at once", false)
				return nil
			}
			return t.apply(m)
		case "e", "s", "L":
			e, ok := t.current()
			if !ok || e.obj == nil {
				m.setStatus("select an object (t shows objects)", true)
				return nil
			}
			if isLive(e.obj) {
				if msg.String() != "s" {
					t.liveStale = true
					return editLive(m, e.obj)
				}
				var local *manifest.Object
				if t.files != nil {
					local = t.files.Get(e.obj.Kind, e.obj.Name)
				}
				if local == nil {
					m.setStatus("no manifest file holds "+e.obj.ID()+" (d picks the folders read)", true)
					return nil
				}
				return syncManifest(m, local, t.serviceOf(m, local))
			}
			switch msg.String() {
			case "e":
				return editManifest(m, e.obj, nil)
			case "s":
				return syncManifest(m, e.obj, t.serviceOf(m, e.obj))
			}
			return editLive(m, e.obj)
		case "o":
			pickEditor(m)
		case "n":
			if t.cluster {
				m.setStatus("a new service starts from a file: c shows the files", true)
				return nil
			}
			return t.newService(m)
		case "r":
			t.viaSearch = false
			return batch(t.scan(m), t.fetchLive(m))
		case "d":
			root := m.app.Spec.Dir
			m.setStatus("looking for manifest folders…", false)
			return func() tea.Msg { return manifestFoldersMsg{root: root, folders: manifest.Folders(root)} }
		}
	}
	return nil
}

func (t *manifestsTab) apply(m *model) tea.Cmd {
	objs := t.targets()
	if len(objs) == 0 {
		return nil
	}
	ap, ok := m.app.Runtime().(applier)
	if !ok {
		m.setStatus("this environment's runtime ("+m.app.Env.Runtime.Type+") does not apply manifests", true)
		return nil
	}
	label := fmt.Sprintf("apply %d objects", len(objs))
	if len(objs) == 1 {
		label = "apply " + objs[0].ID()
	}
	return m.act(label, true, func(ctx context.Context) error {
		if err := ap.ApplyManifests(ctx, objs); err != nil {
			return err
		}
		t.marked = map[string]bool{}
		return nil
	})
}

// newService appends a service for the selected workload to rig.yaml, asking its name, role and groups.
func (t *manifestsTab) newService(m *model) tea.Cmd {
	var w *manifest.Object
	for _, o := range t.targets() {
		if manifest.IsWorkload(o.Kind) {
			w = o
			break
		}
	}
	if w == nil {
		m.setStatus("select a workload (Deployment, StatefulSet, ...) to make it a service", true)
		return nil
	}
	if _, taken := m.app.Spec.Services[w.Name]; taken {
		m.setStatus(w.Name+" is already a service in "+filepath.Base(m.app.Spec.File), true)
		return nil
	}
	m.ask("new service: name role(app|infra|load) groups,comma", w.Name+" app "+strings.ToLower(w.Kind), func(v string) tea.Cmd {
		f := strings.Fields(v)
		if len(f) < 2 || (f[1] != "app" && f[1] != "infra" && f[1] != "load") {
			m.setStatus("write: name role [groups], role app, infra or load", true)
			return nil
		}
		groups := ""
		if len(f) > 2 {
			groups = "[" + strings.Join(strings.Split(f[2], ","), ", ") + "]"
		}
		file, _ := filepath.Rel(m.app.Spec.Dir, abs(w.File))
		img := ""
		if tpl, ok := manifest.Template(w); ok && len(tpl.Spec.Containers) > 0 {
			img = tpl.Spec.Containers[0].Image
		}
		if strings.Contains(img, "$") {
			img = "" // the manifest fills it at deploy
		}
		if err := addService(m.app.Spec.File, f[0], f[1], groups, img, strings.ToLower(w.Kind)+"/"+w.Name, file); err != nil {
			m.setStatus("rig.yaml: "+err.Error(), true)
			return nil
		}
		m.setStatus(fmt.Sprintf("added service %s to %s; switch environment (E) or restart rig to use it", f[0], filepath.Base(m.app.Spec.File)), false)
		return nil
	})
	return nil
}

// addService writes a service at the end of rig.yaml's services: block, keeping the file's comments.
func addService(file, name, role, groups, image, workload, manifestFile string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	if role != "app" {
		fmt.Fprintf(&b, "    role: %s\n", role)
	}
	if groups != "" {
		fmt.Fprintf(&b, "    groups: %s\n", groups)
	}
	if image != "" {
		fmt.Fprintf(&b, "    image: %q\n", image)
	}
	fmt.Fprintf(&b, "    k8s: { workload: %s, manifests: [%s] }\n", workload, manifestFile)
	lines := strings.SplitAfter(string(raw), "\n")
	in, at := false, -1
	for i, l := range lines {
		top := len(l) > 0 && l[0] != ' ' && l[0] != '#' && l[0] != '\n'
		switch {
		case strings.HasPrefix(l, "services:"):
			in = true
		case in && top:
			at = i
		}
		if at >= 0 {
			break
		}
	}
	if !in {
		return fmt.Errorf("no services: block")
	}
	if at < 0 {
		at = len(lines)
	}
	// before the blank lines and comments that lead into the next block
	for at > 0 && (strings.TrimSpace(lines[at-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#")) {
		at--
	}
	out := strings.Join(lines[:at], "") + b.String() + strings.Join(lines[at:], "")
	return os.WriteFile(file, []byte(out), 0o644)
}

func (t *manifestsTab) view(m *model, w, h int) string {
	modes, cur := []string{"objects", "folders and files"}, boolInt(t.tree)
	if _, ok := m.app.Runtime().(liveLister); ok {
		modes, cur = []string{"on the cluster", "files: objects", "files: folders"}, 1+boolInt(t.tree)
		if t.cluster {
			cur = 0
		}
	}
	return m.withStrip("mf:mode", modes, cur, w, h, func(h int) string { return t.body(m, w, h) })
}

// mode shows the cluster (0), the files' objects (1) or their folders (2).
func (t *manifestsTab) mode(m *model, i int) tea.Cmd {
	var cmd tea.Cmd
	if (i == 0) != t.cluster {
		cmd = t.update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	}
	if i > 0 && (i == 2) != t.tree {
		t.tree, t.sel, t.offset, t.file = i == 2, 0, 0, ""
	}
	return cmd
}

func (t *manifestsTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "mf:mode"); ok {
		if _, live := m.app.Runtime().(liveLister); live {
			return t.mode(m, i)
		}
		if (i == 1) != t.tree {
			t.tree, t.sel, t.offset, t.file = i == 1, 0, 0, ""
		}
		return nil
	}
	if t.fields != nil && t.fields.click(h) {
		t.inFields = true
		if h.double && !t.fields.current().container() {
			return t.editField(m)
		}
		return nil
	}
	t.inFields, t.viaSearch = false, false
	if h.id == "mf:svc" {
		return t.gotoService(m)
	}
	if h.id == "mf:rows" && t.issues == "" {
		if i := t.offset + h.y; i < len(t.entries()) {
			t.sel = i
			if h.double {
				return t.update(m, tea.KeyMsg{Type: tea.KeyEnter})
			}
		}
	}
	return nil
}

func (t *manifestsTab) body(m *model, w, h int) string {
	if t.err != "" {
		return panel("manifests", sRed.Render(t.err), w, h, true)
	}
	if t.set == nil {
		return panel("manifests", sDim.Render("scanning "+strings.Join(t.dirs, ", ")+"…"), w, h, true)
	}
	counts := map[string]int{}
	for _, i := range t.set.Issues {
		counts[i.Level]++
	}
	objects := fmt.Sprintf("%d objects", len(t.set.Objects))
	if !t.all && len(t.owners) > 0 {
		used := 0
		for _, o := range t.set.Objects {
			if t.shown(o) {
				used++
			}
		}
		if used < len(t.set.Objects) {
			objects = fmt.Sprintf("%d objects services deploy (u: all %d)", used, len(t.set.Objects))
		}
	}
	summary := objects + fmt.Sprintf(" · %d files · ", len(t.set.Files())) +
		sRed.Render(fmt.Sprintf("%d errors", counts["error"])) + " " + sAmber.Render(fmt.Sprintf("%d warnings", counts["warn"]))
	switch t.issues {
	case "all":
		return panel("issues · "+summary+" · esc back", t.issueList(t.set.Issues, w-4), w, h, true)
	case "file":
		f := t.fileInView()
		var mine []manifest.Issue
		for _, i := range t.set.Issues {
			if i.File == f || objFile(t.set, i.Object) == f {
				mine = append(mine, i)
			}
		}
		body := t.issueList(mine, w-4)
		if len(mine) == 0 {
			body = sGreen.Render("no issues in this file")
		}
		return panel(fmt.Sprintf("issues of %s · %d · I all · esc back", relTo(m.app.Spec.Dir, f), len(mine)), body, w, h, true)
	}

	lw := m.paneSize("manifests", splitGeo{total: w, minA: 20, minB: 30}, min(52, w*2/5), 0, 0, h)
	issuesOf := map[string]int{}
	for _, i := range t.set.Issues {
		if i.Level != "info" {
			issuesOf[i.Object]++
			if i.Object == "" {
				issuesOf["file:"+i.File]++
			}
		}
	}
	es := t.entries()
	var rows [][]string
	for _, e := range es {
		mark := "  "
		if t.marked[e.id()] {
			mark = sAccent.Render("▣ ")
		}
		switch {
		case e.obj != nil:
			warn := ""
			if n := issuesOf[e.obj.ID()]; n > 0 {
				warn = sAmber.Render(fmt.Sprintf("⚠%d", n))
			}
			rows = append(rows, []string{mark + kindColor(e.obj.Kind), e.obj.Name, warn})
		case e.dir != "":
			rows = append(rows, []string{mark + sAccent.Render("▸ dir"), filepath.Base(e.dir) + "/", sDim.Render(fmt.Sprint(e.count))})
		default:
			rows = append(rows, []string{mark + sDim.Render("file"), filepath.Base(e.file), sDim.Render(fmt.Sprint(e.count))})
		}
	}
	t.sel = min(t.sel, max(0, len(rows)-1))
	t.offset = scroll(t.sel, t.offset, h-3, len(rows))
	title := "objects"
	if t.tree {
		title = relTo(m.app.Spec.Dir, t.cwd) + "/"
		if t.file != "" {
			title = relTo(m.app.Spec.Dir, t.file)
		}
	}
	if t.filter != "" {
		title += " · " + t.filter
	}
	if n := len(t.marked); n > 0 {
		title += fmt.Sprintf(" · %d marked", n)
	}
	list := panel(title, table([]string{"KIND", "NAME", ""}, []int{16, lw - 26, 4}, rows, t.sel, t.offset, h-2), lw, h, true)
	m.zone("mf:rows", 1, 2, lw-2, min(h-3, len(rows)-t.offset))
	rw := w - lw
	e, ok := t.current()
	if !ok || e.obj == nil {
		help := "t: folders and files · enter opens a folder or a file · a applies the selected file (or marked ones)"
		if !t.tree {
			help = "no objects"
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, list, panel(summary, sDim.Render(help), rw, h, false))
	}
	o := e.obj
	graph := viz.RelationGraph(o.ID(), rels(t.set.In(o.ID()), true), rels(t.set.Out(o.ID()), false))
	graphH := min(lipgloss.Height(graph)+3, h/2)
	var mine []manifest.Issue
	for _, i := range t.set.Issues {
		if i.Object == o.ID() {
			mine = append(mine, i)
		}
	}
	head := sDim.Render(relTo(m.app.Spec.Dir, o.File))
	if isLive(o) {
		head = sDim.Render("running in " + strings.TrimPrefix(o.File, liveFile))
	}
	if svc := t.cachedService(m, o); svc != "" {
		link := "→ service " + svc + " (v)"
		st := sAccent
		if m.hovering(lw+1+lipgloss.Width(head)+2, 1, lipgloss.Width(link), 1) {
			st = sAccent.Underline(true)
		}
		m.zone("mf:svc", lw+1+lipgloss.Width(head)+2, 1, lipgloss.Width(link), 1)
		head += "  " + st.Render(link)
	}
	top := panel("relations · "+summary, head+"\n"+graph, rw, graphH, false)
	issH := 0
	var iss string
	if len(mine) > 0 {
		issH = min(len(mine)+2, 6)
		iss = panel("issues", t.issueList(mine, rw-4), rw, issH, false)
	}
	t.setFields(o)
	if t.jumpPath != "" {
		t.fields.selectPath(t.jumpPath)
		t.jumpPath, t.inFields = "", true
	}
	fh := h - graphH - issH
	ft := "fields · " + t.fields.current().path()
	if !t.inFields {
		ft += sDim.Render("  (→ or click: edit fields)")
	}
	fieldsBox := panel(ft, t.fields.view(m, lw+1, graphH+issH+1, rw-2, fh-2), rw, fh, t.inFields)
	parts := []string{top}
	if issH > 0 {
		parts = append(parts, iss)
	}
	parts = append(parts, fieldsBox)
	return lipgloss.JoinHorizontal(lipgloss.Top, list, lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func relTo(base, p string) string {
	if r, err := filepath.Rel(base, abs(p)); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// objFile is the file an issue's object (Kind/name) lives in.
func objFile(s *manifest.Set, id string) string {
	for _, o := range s.Objects {
		if o.ID() == id {
			return o.File
		}
	}
	return ""
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
	key := lipgloss.NewStyle().Foreground(cAccent)
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

// serviceOf is the rig service whose workload o is (or is bundled with), for the env vars its deploys inject.
func (t *manifestsTab) serviceOf(m *model, o *manifest.Object) string {
	k, ok := m.k8s()
	if !ok {
		return t.serviceByName(m, o)
	}
	for _, n := range m.app.Spec.ServiceNames() {
		objs, _, err := k.Objects(m.app.Spec.Services[n])
		if err != nil {
			continue
		}
		for _, x := range objs {
			if x.ID() == o.ID() && (isLive(o) || abs(x.File) == abs(o.File)) {
				return n
			}
		}
	}
	return t.serviceByName(m, o)
}

// serviceByName finds the service of o's workload (o itself, or the workload whose bundle holds o)
// by its k8s.workload, else by the service's name, for environments that are not on Kubernetes.
func (t *manifestsTab) serviceByName(m *model, o *manifest.Object) string {
	ws := []*manifest.Object{o}
	if !manifest.IsWorkload(o.Kind) && t.set != nil {
		ws = nil
		for _, w := range t.set.Workloads() {
			for _, x := range t.set.Bundle(w) {
				if x == o {
					ws = append(ws, w)
				}
			}
		}
	}
	for _, w := range ws {
		for _, n := range m.app.Spec.ServiceNames() {
			var sec struct {
				Workload string `yaml:"workload"`
			}
			_, _ = m.app.Spec.Services[n].Section("k8s", &sec)
			kind, name, ok := strings.Cut(sec.Workload, "/")
			if !ok {
				kind, name = "deployment", sec.Workload
			}
			if name == "" {
				name = n
			}
			if strings.EqualFold(kind, w.Kind) && name == w.Name || sec.Workload == "" && n == w.Name {
				return n
			}
		}
	}
	return ""
}

func (t *manifestsTab) cachedService(m *model, o *manifest.Object) string {
	if t.svcOf == nil {
		t.svcOf = map[*manifest.Object]string{}
	}
	svc, ok := t.svcOf[o]
	if !ok {
		if svc = t.owners[objKey(o)]; svc == "" {
			svc = t.serviceOf(m, o)
		}
		t.svcOf[o] = svc
	}
	return svc
}

// gotoService opens the Services screen on the service the selected object belongs to.
func (t *manifestsTab) gotoService(m *model) tea.Cmd {
	e, ok := t.current()
	if !ok || e.obj == nil {
		m.setStatus("select an object (t shows objects)", true)
		return nil
	}
	svc := t.cachedService(m, e.obj)
	if svc == "" {
		m.setStatus(e.obj.ID()+" belongs to no service in "+filepath.Base(m.app.Spec.File)+" (n makes one)", true)
		return nil
	}
	return m.showService(svc)
}

func (t *manifestsTab) atRoot() bool {
	return t.issues == "" && t.filter == "" && (!t.tree || t.file == "" && (t.cwd == "" || t.cwd == filepath.Dir(t.cwd)))
}
