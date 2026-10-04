package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/spec"
)

// k8sObjects is a runtime that knows a service's manifests and the cluster's live objects (Kubernetes, kind).
type k8sObjects interface {
	Objects(s *spec.Service) ([]*manifest.Object, *manifest.Object, error)
	LiveObject(ctx context.Context, kind, name string) ([]byte, error)
	InjectedEnv(s *spec.Service) map[string]bool
	KubectlArgs() []string
}

type (
	// manifestEditedMsg: the editor closed on an object's document (or on what the cluster runs, for a sync).
	manifestEditedMsg struct {
		obj      *manifest.Object
		file     string
		original []byte
		written  time.Time // the temp file's mtime before the editor: unchanged means not saved
		err      error
	}
	resourcesMsg struct {
		svc string
		res []core.Resources
		err error
	}
	syncedMsg struct {
		obj  *manifest.Object
		yaml []byte
		err  error
	}
)

func (m *model) k8s() (k8sObjects, bool) {
	k, ok := m.app.Runtime().(k8sObjects)
	return k, ok
}

// serviceManifests (F) lists the objects a deploy of svc applies, then what to do with the one picked.
func serviceManifests(m *model, svc string) tea.Cmd {
	k, ok := m.k8s()
	if !ok {
		m.setStatus(m.app.Env.Name+": not on Kubernetes, no manifests", true)
		return nil
	}
	objs, _, err := k.Objects(m.app.Spec.Services[svc])
	if err != nil || len(objs) == 0 {
		m.setStatus(fmt.Sprintf("%s: no manifests found (%v)", svc, err), true)
		return nil
	}
	var names, desc []string
	byName := map[string]*manifest.Object{}
	for _, o := range objs {
		names = append(names, o.ID())
		desc = append(desc, relTo(m.app.Spec.Dir, o.File))
		byName[o.ID()] = o
	}
	m.pick("manifests of "+svc, names, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		objectActions(m, byName[c[0]], svc)
		return nil
	})
	return nil
}

// objectActions offers what can be done with one manifest object: edit its file, edit what runs,
// bring the file up to what runs, apply the file.
func objectActions(m *model, o *manifest.Object, svc string) {
	opts := []string{"edit the file", "edit on the cluster", "sync file from the cluster", "apply the file"}
	desc := []string{
		relTo(m.app.Spec.Dir, o.File) + " in your editor; saved back into the file",
		"kubectl edit: saving applies at once",
		"what runs now, written into the file (in your editor first; $VARS kept)",
		"what rig would deploy, for this object only",
	}
	m.pick(o.ID(), opts, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		switch c[0] {
		case opts[0]:
			return editManifest(m, o, nil)
		case opts[1]:
			return editLive(m, o)
		case opts[2]:
			return syncManifest(m, o, svc)
		case opts[3]:
			return applyObject(m, o)
		}
		return nil
	})
}

// editManifest opens o's document (or start, when given) in the editor; the Manifests and Services
// screens write it into o's file when the editor exits with a change.
func editManifest(m *model, o *manifest.Object, start []byte) tea.Cmd {
	original, err := o.YAML()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	if start == nil {
		start = original
	}
	f, err := os.CreateTemp("", "rig-manifest-*.yaml")
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	_, _ = f.Write(start)
	f.Close()
	file := f.Name()
	// an old mtime, so a save within the same second still shows
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(file, old, old)
	c := exec.Command("sh", "-c", editorCmd()+` "$1"`, "rig-edit", file)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return manifestEditedMsg{obj: o, file: file, original: original, written: old, err: err}
	})
}

func (m *model) manifestEdited(msg manifestEditedMsg) tea.Cmd {
	defer os.Remove(msg.file)
	if msg.err != nil {
		m.setStatus("editor: "+msg.err.Error(), true)
		return nil
	}
	raw, err := os.ReadFile(msg.file)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	if st, err := os.Stat(msg.file); err == nil && st.ModTime().Equal(msg.written) {
		m.setStatus("not saved: "+msg.obj.ID()+" left as it was", false)
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(msg.original)) {
		m.setStatus("no change to "+msg.obj.ID(), false)
		return nil
	}
	if err := manifest.ReplaceDoc(msg.obj, raw); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	o := reread(msg.obj)
	m.rescanManifests()
	m.confirm = &confirm{text: fmt.Sprintf("saved %s in %s; apply it to %s?", o.ID(), relTo(m.app.Spec.Dir, o.File), m.app.Env.Name), run: func() tea.Msg {
		return applyObjectNow(m, o)()
	}}
	return nil
}

// reread is o as its file says now.
func reread(o *manifest.Object) *manifest.Object {
	set, err := manifest.Scan(o.File)
	if err != nil {
		return o
	}
	for _, x := range set.Objects {
		if x.Doc == o.Doc && x.ID() == o.ID() {
			return x
		}
	}
	return o
}

func (m *model) rescanManifests() {
	for _, t := range m.tabs {
		if mt, ok := t.(*manifestsTab); ok && mt.set != nil {
			if set, err := manifest.Scan(mt.dirs...); err == nil {
				mt.set = set
			}
		}
	}
}

// editLive hands the terminal to `kubectl edit`, with the editor rig uses.
func editLive(m *model, o *manifest.Object) tea.Cmd {
	k, ok := m.k8s()
	if !ok {
		m.setStatus(m.app.Env.Name+" is not on Kubernetes", true)
		return nil
	}
	if m.app.Env.Protected {
		m.confirm = &confirm{text: "kubectl edit " + o.ID() + " on PROTECTED " + m.app.Env.Name + "?", run: func() tea.Msg { return runEditLive(k, o)() }}
		return nil
	}
	return runEditLive(k, o)
}

func runEditLive(k k8sObjects, o *manifest.Object) tea.Cmd {
	c := exec.Command("kubectl", append(k.KubectlArgs(), "edit", strings.ToLower(o.Kind)+"/"+o.Name)...)
	c.Env = append(os.Environ(), "KUBE_EDITOR="+editorCmd())
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "kubectl edit " + o.ID() + ": " + err.Error(), err: true}
		}
		return statusMsg{text: "kubectl edit " + o.ID() + " ✓"}
	})
}

// syncManifest reads what runs and opens it in the editor; saving writes it into the file.
func syncManifest(m *model, o *manifest.Object, svc string) tea.Cmd {
	k, ok := m.k8s()
	if !ok {
		m.setStatus(m.app.Env.Name+" is not on Kubernetes", true)
		return nil
	}
	drop := map[string]bool{}
	if s := m.app.Spec.Services[svc]; s != nil {
		drop = k.InjectedEnv(s)
	}
	ctx := m.ctx
	m.setStatus("reading "+o.ID()+" from "+m.app.Env.Name+"…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		live, err := k.LiveObject(c, strings.ToLower(o.Kind), o.Name)
		if err != nil {
			return syncedMsg{obj: o, err: err}
		}
		y, err := manifest.FromLive(o, live, drop)
		return syncedMsg{obj: o, yaml: y, err: err}
	}
}

func (m *model) synced(msg syncedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("sync "+msg.obj.ID()+": "+msg.err.Error(), true)
		return nil
	}
	cur, _ := msg.obj.YAML()
	if bytes.Equal(bytes.TrimSpace(cur), bytes.TrimSpace(msg.yaml)) {
		m.setStatus(msg.obj.ID()+": the file already says what runs", false)
		return nil
	}
	m.setStatus("save to write what runs into "+filepath.Base(msg.obj.File)+"; quit without saving to keep the file", false)
	return editManifest(m, msg.obj, msg.yaml)
}

func applyObject(m *model, o *manifest.Object) tea.Cmd {
	if _, ok := m.app.Runtime().(applier); !ok {
		m.setStatus("this environment's runtime ("+m.app.Env.Runtime.Type+") does not apply manifests", true)
		return nil
	}
	return m.act("apply "+o.ID(), true, func(ctx context.Context) error {
		return m.app.Runtime().(applier).ApplyManifests(ctx, []*manifest.Object{o})
	})
}

// applyObjectNow applies without asking: the caller's confirmation was the question.
func applyObjectNow(m *model, o *manifest.Object) tea.Cmd {
	ap, ok := m.app.Runtime().(applier)
	if !ok {
		return func() tea.Msg { return statusMsg{text: "saved; this runtime does not apply manifests"} }
	}
	a, ctx := m.app, core.WithConfirmed(m.ctx)
	return func() tea.Msg {
		a.Confirmed = true
		defer func() { a.Confirmed = false }()
		if err := ap.ApplyManifests(ctx, []*manifest.Object{o}); err != nil {
			return statusMsg{text: "apply " + o.ID() + ": " + err.Error(), err: true}
		}
		return statusMsg{text: "apply " + o.ID() + " ✓"}
	}
}

// editResources (R) shows a container's requests and limits and sets new ones.
func editResources(m *model, svc string) tea.Cmd {
	a, ctx := m.app, m.ctx
	m.setStatus("reading the resources of "+svc+"…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		res, err := a.Resources(c, svc)
		return resourcesMsg{svc: svc, res: res, err: err}
	}
}

func (m *model) gotResources(msg resourcesMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("resources of "+msg.svc+": "+msg.err.Error(), true)
		return nil
	}
	if len(msg.res) == 0 {
		m.setStatus(msg.svc+": no containers", true)
		return nil
	}
	ask := func(r core.Resources) {
		dash := func(s string) string {
			if s == "" {
				return "-"
			}
			return s
		}
		cur := strings.Join([]string{dash(r.CPURequest), dash(r.CPULimit), dash(r.MemRequest), dash(r.MemLimit)}, " ")
		m.ask(fmt.Sprintf("%s/%s requests and limits: cpu-req cpu-limit mem-req mem-limit (- keeps one)", msg.svc, r.Container), cur, func(v string) tea.Cmd {
			f := strings.Fields(v)
			if len(f) != 4 {
				m.setStatus("type four values: cpu request, cpu limit, memory request, memory limit (- keeps one)", true)
				return nil
			}
			for i := range f {
				if f[i] == "-" {
					f[i] = ""
				}
			}
			n := core.Resources{Container: r.Container, CPURequest: f[0], CPULimit: f[1], MemRequest: f[2], MemLimit: f[3]}
			if n == r || n == (core.Resources{Container: r.Container}) {
				m.setStatus("no change", false)
				return nil
			}
			a, svc := m.app, msg.svc
			return m.act(fmt.Sprintf("resources of %s/%s to %s (restarts its pods)", svc, r.Container, strings.Join(strings.Fields(v), " ")), true, func(ctx context.Context) error {
				return a.SetResources(ctx, svc, n)
			})
		})
	}
	if len(msg.res) == 1 {
		ask(msg.res[0])
		return nil
	}
	var names, desc []string
	by := map[string]core.Resources{}
	for _, r := range msg.res {
		names = append(names, r.Container)
		desc = append(desc, fmt.Sprintf("cpu %s/%s  memory %s/%s", r.CPURequest, r.CPULimit, r.MemRequest, r.MemLimit))
		by[r.Container] = r
	}
	m.pick("container of "+msg.svc, names, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) > 0 {
			ask(by[c[0]])
		}
		return nil
	})
	return nil
}
