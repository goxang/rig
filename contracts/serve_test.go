package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// wire is a Caller that sends every call through JSON to served, as the host and a plugin would.
type wire struct {
	served *Served
	comp   any
}

func trip[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func (w *wire) lookup() Lookup {
	return Lookup{
		Component: func(_ context.Context, name string) (any, error) {
			if name != "c" {
				return nil, fmt.Errorf("no component %q", name)
			}
			return w.comp, nil
		},
		Service: func(name string) (*spec.Service, error) { return &spec.Service{Name: name}, nil },
	}
}

func (w *wire) Query(ctx context.Context, name string, a protocol.Args) (*protocol.Frame, error) {
	fn, ok := w.served.Sources[strings.TrimPrefix(name, "p.")]
	if !ok {
		return nil, fmt.Errorf("no source %s", name)
	}
	f, err := fn(ctx, trip(a))
	return trip(f), err
}

func (w *wire) Run(ctx context.Context, name string, a protocol.Args, _ bool) (*protocol.RunResult, error) {
	fn, ok := w.served.Actions[strings.TrimPrefix(name, "p.")]
	if !ok {
		return nil, fmt.Errorf("no action %s", name)
	}
	out, err := fn(ctx, trip(a), io.Discard)
	return trip(&protocol.RunResult{Outputs: out}), err
}

func (w *wire) Stream(ctx context.Context, name string, a protocol.Args) (<-chan protocol.StreamBatch, error) {
	fn, ok := w.served.Streams[strings.TrimPrefix(name, "p.")]
	if !ok {
		return nil, fmt.Errorf("no stream %s", name)
	}
	ch := make(chan protocol.StreamBatch)
	go func() {
		defer close(ch)
		_ = fn(ctx, trip(a), func(f *protocol.Frame) error { ch <- protocol.StreamBatch{Frame: trip(f)}; return nil })
		ch <- protocol.StreamBatch{Done: true}
	}()
	return ch, nil
}

func roundTrip(t *testing.T, kind core.Kind, comp any) any {
	t.Helper()
	w := &wire{comp: comp}
	s, err := Serve(kind, comp, w.lookup())
	if err != nil {
		t.Fatal(err)
	}
	w.served = s
	ct := ForKind(string(kind))
	pr := &protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{}}
	for n := range s.Sources {
		pr.Sources[n] = protocol.SourceSpec{}
	}
	for n := range s.Streams {
		pr.Sources[n] = protocol.SourceSpec{Stream: true}
	}
	for n := range s.Actions {
		pr.Actions[n] = protocol.ActionSpec{Effect: s.Effects[n]}
	}
	if ps := ct.Check(pr); len(ps) > 0 {
		t.Fatalf("served %s falls short of %s: %v", kind, ct.ID, ps)
	}
	for n := range pr.Sources {
		if _, ok := ct.Sources[n]; !ok {
			t.Errorf("served source %s is not in %s", n, ct.ID)
		}
	}
	for n := range pr.Actions {
		if _, ok := ct.Actions[n]; !ok {
			t.Errorf("served action %s is not in %s", n, ct.ID)
		}
	}
	v, err := Bridge(kind, w, "p", "c", pr)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type fakeDB struct {
	deleted [][]string
	set     string
	log     string
}

func (*fakeDB) Query(_ context.Context, db, q string) (core.Table, error) {
	return core.Table{Columns: []string{"db", "q"}, Rows: [][]string{{db, q}}, Note: "1 row"}, nil
}
func (*fakeDB) Exec(context.Context, string, string) (int64, error) { return 7, nil }
func (*fakeDB) Databases(context.Context) ([]string, error)         { return []string{"a", "b"}, nil }
func (*fakeDB) CreateDatabase(context.Context, string) error        { return nil }
func (*fakeDB) DropDatabase(context.Context, string) error          { return nil }
func (*fakeDB) Ping(context.Context) error                          { return nil }
func (*fakeDB) QueryLanguage() string                               { return "sql" }
func (d *fakeDB) RunQuery(ctx context.Context, q string) (core.Table, error) {
	return d.Query(ctx, "", q)
}
func (*fakeDB) Browse(_ context.Context, p []string) (core.Table, bool, error) {
	return core.Table{Columns: []string{"name"}, Rows: [][]string{{strings.Join(p, "/")}}}, len(p) == 2, nil
}
func (*fakeDB) QueryAt(_ context.Context, p []string, q string) (core.Table, error) {
	return core.Table{Columns: []string{"q"}, Rows: [][]string{{q}}}, nil
}
func (*fakeDB) SuggestQuery(_ context.Context, p []string) string {
	return "select * from " + p[len(p)-1]
}
func (*fakeDB) SuggestRowQuery(_ context.Context, _ []string, t core.Table, row int) string {
	return "where id = " + t.Rows[row][0]
}
func (d *fakeDB) Delete(_ context.Context, _ []string, t core.Table, rows []int) error {
	for _, r := range rows {
		d.deleted = append(d.deleted, t.Rows[r])
	}
	return nil
}
func (d *fakeDB) Set(_ context.Context, _ []string, t core.Table, row, col int, v string) error {
	d.set = t.Rows[row][0] + "." + t.Columns[col] + "=" + v
	return nil
}
func (d *fakeDB) Snapshot(_ context.Context, file string, log io.Writer) error {
	d.log = "snap " + file
	return nil
}
func (*fakeDB) Restore(context.Context, string, io.Writer) error { return nil }
func (*fakeDB) Seed(context.Context, io.Writer) error            { return nil }
func (*fakeDB) Fresh(context.Context) (bool, error)              { return true, nil }
func (*fakeDB) Host() string                                     { return "pg" }

func TestDatabaseRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := &fakeDB{}
	v := roundTrip(t, core.KindDatabase, d)
	db := v.(core.Database)
	tb, err := db.Query(ctx, "x", "select 1")
	if err != nil || !slices.Equal(tb.Rows[0], []string{"x", "select 1"}) || tb.Note != "1 row" {
		t.Fatalf("query: %+v %v", tb, err)
	}
	tb, _ = db.(core.Querier).RunQuery(ctx, "@Switch select 2")
	if !slices.Equal(tb.Rows[0], []string{"Switch", "select 2"}) {
		t.Fatalf("@db query: %+v", tb)
	}
	if n, _ := db.Exec(ctx, "", ""); n != 7 {
		t.Fatalf("exec: %d", n)
	}
	br, ok := core.As[core.Browser](v)
	if !ok {
		t.Fatal("browse not admitted")
	}
	if _, leaf, _ := br.Browse(ctx, []string{"s", "t"}); !leaf {
		t.Fatal("leaf lost")
	}
	pq, _ := core.As[core.PathQuerier](v)
	if q := pq.SuggestQuery(ctx, []string{"s", "t"}); q != "select * from t" {
		t.Fatalf("suggest: %q", q)
	}
	tbl := core.Table{Columns: []string{"id", "name"}, Rows: [][]string{{"1", "a"}, {"2", "b"}, {"3", "c"}}}
	rq, _ := core.As[core.RowQuerier](v)
	if q := rq.SuggestRowQuery(ctx, nil, tbl, 2); q != "where id = 3" {
		t.Fatalf("row query: %q", q)
	}
	ed, _ := core.As[core.Editor](v)
	if err := ed.Delete(ctx, nil, tbl, []int{0, 2}); err != nil || len(d.deleted) != 2 || d.deleted[1][0] != "3" {
		t.Fatalf("delete: %v %v", d.deleted, err)
	}
	if _ = ed.Set(ctx, nil, tbl, 1, 1, "z"); d.set != "2.name=z" {
		t.Fatalf("set: %q", d.set)
	}
	sn, _ := core.As[core.Snapshotter](v)
	if _ = sn.Snapshot(ctx, "f", io.Discard); d.log != "snap f" {
		t.Fatalf("snapshot: %q", d.log)
	}
	sd, _ := core.As[core.Seeder](v)
	if fresh, _ := sd.Fresh(ctx); !fresh || sd.Host() != "pg" {
		t.Fatal("seeder")
	}
	if _, ok := core.As[core.Actioner](v); ok {
		t.Fatal("actions admitted without the capability")
	}
}

type fakeRuntime struct{ scaled int }

var started = time.Unix(1700000000, 0)

func (*fakeRuntime) Discover(context.Context) ([]core.Workload, error) { return nil, nil }
func (*fakeRuntime) Start(context.Context, *spec.Service) error        { return nil }
func (*fakeRuntime) Stop(context.Context, *spec.Service) error         { return nil }
func (*fakeRuntime) Restart(context.Context, *spec.Service) error      { return nil }
func (*fakeRuntime) Status(_ context.Context, s *spec.Service) (core.Status, error) {
	if s.Name == "gone" {
		return core.Status{}, fmt.Errorf("gone")
	}
	return core.Status{Service: s.Name, State: core.StateRunning, Ready: 2, Desired: 2,
		Instances: []core.Instance{{ID: "i1", Started: started, Memory: 1 << 20}}, Autoscale: &core.Bounds{Min: 1, Max: 3}}, nil
}
func (r *fakeRuntime) Scale(_ context.Context, _ *spec.Service, n int) error {
	r.scaled = n
	return nil
}
func (*fakeRuntime) Logs(_ context.Context, s *spec.Service, o core.LogOptions) (<-chan core.LogLine, error) {
	ch := make(chan core.LogLine, 2)
	ch <- core.LogLine{Time: started, Service: s.Name, Text: fmt.Sprint(o.Since)}
	close(ch)
	return ch, nil
}
func (*fakeRuntime) Exec(ctx context.Context, s *spec.Service, o core.ExecOptions) error {
	if c := CaptureOf(ctx); c != nil {
		c.Argv = append([]string{s.Name}, o.Command...)
		return nil
	}
	return fmt.Errorf("no terminal")
}
func (*fakeRuntime) Deploy(context.Context, *spec.Service, core.Release) error { return nil }
func (*fakeRuntime) Forward(_ context.Context, t core.Target) (string, error) {
	return fmt.Sprintf("127.0.0.1:%d", t.Port+1), nil
}

func TestRuntimeRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := &fakeRuntime{}
	v := roundTrip(t, core.KindRuntime, r)
	rt := v.(core.Runtime)
	sts, err := v.(core.StatusLister).StatusAll(ctx, []*spec.Service{{Name: "api"}, {Name: "gone"}})
	if err != nil || sts[0].State != core.StateRunning || sts[0].Autoscale.Max != 3 || !sts[0].Instances[0].Started.Equal(started) ||
		sts[0].Instances[0].Memory != 1<<20 || sts[1].State != core.StateUnknown {
		t.Fatalf("status: %+v %v", sts, err)
	}
	if _ = rt.Scale(ctx, &spec.Service{Name: "api"}, 4); r.scaled != 4 {
		t.Fatal("scale")
	}
	ch, err := rt.Logs(ctx, &spec.Service{Name: "api"}, core.LogOptions{Since: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	l := <-ch
	if l.Text != "1m30s" || !l.Time.Equal(started) || l.Service != "api" {
		t.Fatalf("logs: %+v", l)
	}
	var out bytes.Buffer
	err = rt.Exec(ctx, &spec.Service{Name: "true"}, core.ExecOptions{Stdout: &out})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	fw, ok := core.As[core.Forwarder](v)
	if !ok {
		t.Fatal("forward not admitted")
	}
	if addr, _ := fw.Forward(ctx, core.Target{Port: 80}); addr != "127.0.0.1:81" {
		t.Fatalf("forward: %q", addr)
	}
	if _, ok := core.As[core.StateStore](v); ok {
		t.Fatal("state admitted without the capability")
	}
}

type fakeK8s struct {
	fakeRuntime
	file    string
	applied []string
}

func (k *fakeK8s) Objects(*spec.Service) ([]*manifest.Object, *manifest.Object, error) {
	set, err := manifest.Scan(k.file)
	if err != nil {
		return nil, nil, err
	}
	return set.Objects, set.Get("Deployment", "api"), nil
}
func (*fakeK8s) LiveObject(_ context.Context, kind, name string) ([]byte, error) {
	return []byte(`{"kind":"` + kind + `"}`), nil
}
func (*fakeK8s) LiveObjects(context.Context) ([]byte, error) { return []byte("kind: Service\n"), nil }
func (*fakeK8s) InjectedEnv(*spec.Service) map[string]bool   { return map[string]bool{"OTEL": true} }
func (*fakeK8s) KubectlArgs() []string                       { return []string{"--context", "c"} }
func (k *fakeK8s) ApplyManifests(_ context.Context, objs []*manifest.Object) error {
	for _, o := range objs {
		k.applied = append(k.applied, fmt.Sprintf("%s#%d %s", o.ID(), o.Doc, o.Labels["edited"]))
	}
	return nil
}

func TestManifestsRoundTrip(t *testing.T) {
	ctx := context.Background()
	file := t.TempDir() + "/api.yaml"
	if err := os.WriteFile(file, []byte("apiVersion: v1\nkind: Service\nmetadata: {name: api}\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: api}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := &fakeK8s{file: file}
	v := roundTrip(t, core.KindRuntime, k)
	m, ok := core.As[core.Manifester](v)
	if !ok {
		t.Fatal("manifests not admitted")
	}
	objs, w, err := m.Objects(&spec.Service{Name: "api"})
	if err != nil || len(objs) != 2 || w == nil || w.ID() != "Deployment/api" || w.Doc != 1 {
		t.Fatalf("objects: %v %v %v", objs, w, err)
	}
	w.Node = manifest.Parse(file, []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: api, labels: {edited: yes}}\n")).Objects[0].Node
	if err := m.ApplyManifests(ctx, []*manifest.Object{w}); err != nil || len(k.applied) != 1 || k.applied[0] != "Deployment/api#1 yes" {
		t.Fatalf("apply: %v %v", k.applied, err)
	}
	if !m.InjectedEnv(&spec.Service{Name: "api"})["OTEL"] || m.KubectlArgs()[1] != "c" {
		t.Fatal("injected env or kubectl args")
	}
}
