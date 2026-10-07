package engine

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/viz"
)

// Evidence is one fact rig why found, cited by its ID.
type Evidence struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"` // status, health, reach, logs, traces, metrics, alert, change
	Service string    `json:"service,omitempty"`
	At      time.Time `json:"at,omitempty"`
	Bad     bool      `json:"bad"`
	Summary string    `json:"summary"`
	Lines   []string  `json:"lines,omitempty"`
	// Blames are the services this evidence points at besides its own (a log line naming its database).
	Blames []string `json:"blames,omitempty"`
}

// Suspect is a service ranked by the evidence against it.
type Suspect struct {
	Service  string   `json:"service"`
	Score    int      `json:"score"`
	Evidence []string `json:"evidence"`
}

// Incident is what rig why gathered about one service and what it depends on.
type Incident struct {
	Service string `json:"service"`
	// Symptom is what the user described, when they said more than a service's name.
	Symptom  string     `json:"symptom,omitempty"`
	Env      string     `json:"env"`
	At       time.Time  `json:"at"`
	Window   string     `json:"window"`
	Checked  []string   `json:"checked"`
	Evidence []Evidence `json:"evidence"`
	Suspects []Suspect  `json:"suspects"`
}

var errorLine = regexp.MustCompile(`(?i)\b(error|err=|panic|fatal|exception|timeout|timed out|refused|unreachable|no such host|failed|broken pipe|reset by peer)\b`)

// Investigate gathers evidence about service and everything it depends on over the last window:
// state and restarts, health probes, the components they host, error lines in their logs, failed
// traces, firing alerts and recent changes; then ranks the likely culprits. It needs no AI.
// An empty service checks every service.
func (a *App) Investigate(ctx context.Context, service string, window time.Duration) (*Incident, error) {
	var targets []string
	if service != "" {
		if _, err := a.Service(service); err != nil {
			return nil, err
		}
		targets = []string{service}
	}
	names, err := a.Targets(targets, true)
	if err != nil {
		return nil, err
	}
	inc := &Incident{Service: service, Env: a.envName(), At: time.Now(), Window: window.String(), Checked: names}
	var mu sync.Mutex
	add := func(e Evidence) {
		mu.Lock()
		inc.Evidence = append(inc.Evidence, e)
		mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, st := range a.StatusAll(ctx, names) {
		a.statusEvidence(st, window, add)
		if s := a.Spec.Services[st.Service]; s != nil && s.Health != nil && st.State == core.StateRunning {
			wg.Add(1)
			go func() { defer wg.Done(); a.healthEvidence(ctx, st.Service, add) }()
		}
	}
	for _, n := range names {
		wg.Add(1)
		go func() { defer wg.Done(); a.logEvidence(ctx, n, names, window, add) }()
	}
	in := map[string]bool{}
	for _, n := range names {
		in[n] = true
	}
	for _, c := range a.componentHosts() {
		if in[c.host] {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.Reach(ctx, c.name); err != nil && err != core.ErrUnsupported {
					add(Evidence{Kind: "reach", Service: c.host, Bad: true, Summary: fmt.Sprintf("component %s (in %s) does not answer: %s", c.name, c.host, firstLine(err))})
				}
			}()
		}
	}
	traced := []string{service}
	if service == "" {
		traced = names[:min(10, len(names))]
	}
	for _, n := range traced {
		wg.Add(1)
		go func() { defer wg.Done(); a.traceEvidence(ctx, n, window, add) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); a.metricEvidence(ctx, names, window, add) }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		fs, _ := a.CheckAlerts(ctx)
		for _, f := range fs {
			add(Evidence{Kind: "alert", Bad: true, Summary: "alert firing: " + f.String()})
		}
	}()
	wg.Wait()
	a.changeEvidence(window, add)

	sort.SliceStable(inc.Evidence, func(i, j int) bool {
		if inc.Evidence[i].Bad != inc.Evidence[j].Bad {
			return inc.Evidence[i].Bad
		}
		return inc.Evidence[i].Service < inc.Evidence[j].Service
	})
	for i := range inc.Evidence {
		inc.Evidence[i].ID = fmt.Sprintf("E%d", i+1)
	}
	inc.Suspects = Rank(service, inc.Evidence)
	return inc, nil
}

func (a *App) statusEvidence(st core.Status, window time.Duration, add func(Evidence)) {
	e := Evidence{Kind: "status", Service: st.Service, Summary: fmt.Sprintf("%s is %s (%d/%d ready)", st.Service, st.State, st.Ready, st.Desired)}
	switch st.State {
	case core.StateRunning:
	case core.StateStopped, core.StateFailed, core.StateAbsent, core.StateStarting, core.StateDegraded:
		e.Bad = true
	default:
		e.Summary = fmt.Sprintf("%s: state unknown: %s", st.Service, st.Message)
	}
	if st.Message != "" && e.Bad {
		e.Summary += ": " + st.Message
	}
	add(e)
	for _, in := range st.Instances {
		if in.Restarts > 0 {
			add(Evidence{Kind: "status", Service: st.Service, Bad: true, Summary: fmt.Sprintf("%s restarted %d times", st.Service, in.Restarts)})
		}
		if !in.Started.IsZero() && time.Since(in.Started) < window {
			add(Evidence{Kind: "change", Service: st.Service, At: in.Started, Summary: fmt.Sprintf("%s (re)started %s ago", st.Service, time.Since(in.Started).Round(time.Second))})
		}
	}
}

func (a *App) healthEvidence(ctx context.Context, name string, add func(Evidence)) {
	s := a.Spec.Services[name]
	port := s.PortNumber(s.Health.Port)
	if port == 0 {
		port = s.FirstPort()
	}
	addr, err := a.Resolve(ctx, fmt.Sprintf("svc://%s:%d", name, port))
	if err != nil {
		add(Evidence{Kind: "health", Service: name, Bad: true, Summary: fmt.Sprintf("%s health check unreachable: %s", name, firstLine(err))})
		return
	}
	url := "http://" + strings.TrimPrefix(addr, "http://") + s.Health.Path
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		add(Evidence{Kind: "health", Service: name, Bad: true, Summary: fmt.Sprintf("%s health check %s failed: %s", name, s.Health.Path, firstLine(err))})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	e := Evidence{Kind: "health", Service: name, Bad: resp.StatusCode >= 500, Summary: fmt.Sprintf("%s health %s: %s", name, s.Health.Path, resp.Status)}
	if b := strings.Join(strings.Fields(string(body)), " "); b != "" && e.Bad {
		e.Lines = []string{b}
	}
	add(e)
}

// logEvidence keeps the error lines of name's recent logs and which other services they mention.
func (a *App) logEvidence(ctx context.Context, name string, services []string, window time.Duration, add func(Evidence)) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ch, err := a.Logs(ctx, name, core.LogOptions{Since: window, Tail: 500})
	if err != nil {
		return
	}
	var lines []string
	total := 0
	blames := map[string]bool{}
	hosts := a.componentHosts()
	for l := range ch {
		if !errorLine.MatchString(l.Text) {
			continue
		}
		total++
		if len(lines) < 12 {
			lines = append(lines, strings.TrimSpace(l.Text))
		}
		for _, other := range services {
			if other != name && mentions(l.Text, other) {
				blames[other] = true
			}
		}
		for _, c := range hosts {
			if c.host != name && mentions(l.Text, c.name) {
				blames[c.host] = true
			}
		}
	}
	if total == 0 {
		return
	}
	e := Evidence{Kind: "logs", Service: name, Bad: true, Summary: fmt.Sprintf("%s logged %d error lines in the last %s", name, total, window), Lines: lines}
	for b := range blames {
		e.Blames = append(e.Blames, b)
	}
	sort.Strings(e.Blames)
	if len(e.Blames) > 0 {
		e.Summary += ", naming " + strings.Join(e.Blames, ", ")
	}
	add(e)
}

func mentions(line, name string) bool {
	return regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(name) + `([^a-z0-9_-]|$)`).MatchString(line)
}

func (a *App) traceEvidence(ctx context.Context, service string, window time.Duration, add func(Evidence)) {
	tr, _, err := Get[core.Tracing](a, core.KindTracing, "")
	if err != nil {
		return
	}
	ts, err := tr.Search(ctx, core.TraceQuery{Service: service, Lookback: window, Limit: 100})
	if err != nil || len(ts) == 0 {
		return
	}
	failed, slowest := 0, ts[0]
	for _, t := range ts {
		if t.Error {
			failed++
		}
		if t.Duration > slowest.Duration {
			slowest = t
		}
	}
	add(Evidence{Kind: "traces", Service: service, Bad: failed > 0,
		Summary: fmt.Sprintf("%d of %d traces of %s failed; slowest %s (%s, trace %s)", failed, len(ts), service, slowest.Duration.Round(time.Millisecond), slowest.Root, slowest.ID)})
}

// InvestigateSymptom investigates what the user described: the first service its words name (by
// name, image or SERVICE_NAME), else every service.
func (a *App) InvestigateSymptom(ctx context.Context, words []string, window time.Duration) (*Incident, error) {
	service := ""
	for _, w := range words {
		if n := a.serviceNamed(strings.Trim(w, ",.:;?!'\"")); n != "" {
			service = n
			break
		}
	}
	inc, err := a.Investigate(ctx, service, window)
	if err != nil {
		return nil, err
	}
	if len(words) > 1 || service == "" {
		inc.Symptom = strings.Join(words, " ")
	}
	return inc, nil
}

func (a *App) serviceNamed(w string) string {
	if w == "" {
		return ""
	}
	for n, s := range a.Spec.Services {
		if strings.EqualFold(n, w) || strings.EqualFold(path.Base(strings.Split(s.Image, ":")[0]), w) || strings.EqualFold(s.Env["SERVICE_NAME"], w) {
			return n
		}
	}
	return ""
}

var worseUp = regexp.MustCompile(`(?i)err|fail|5\d\d|latenc|duration|p9\d|timeout|restart|lag|queue|pending|reject|drop`)

// metricEvidence reads the dashboard panels about the checked services (a $service variable, or the
// service's name in the query) over the window: where each started and where it is now.
func (a *App) metricEvidence(ctx context.Context, names []string, window time.Duration, add func(Evidence)) {
	step := max(window/20, 15*time.Second)
	n := 0
	for _, dn := range SortedKeys(a.Spec.Dashboards) {
		d := a.Spec.Dashboards[dn]
		for _, p := range d.Panels {
			for _, t := range p.Targets() {
				for _, svc := range names {
					if n >= 20 {
						return
					}
					var vars map[string][]string
					switch {
					case strings.Contains(t.Query, "$service") && d.Vars["service"] != nil:
						vars = map[string][]string{"service": {svc}}
					case !strings.Contains(t.Query, svc):
						continue
					}
					src, _, err := Get[core.Metrics](a, core.KindMetrics, p.Source)
					if err != nil {
						return
					}
					to := time.Now()
					ss, err := src.Range(ctx, ExpandQuery(t.Query, vars, window, step), to.Add(-window), to, step)
					if err != nil || len(ss) == 0 {
						continue
					}
					n++
					first, last := 0.0, 0.0
					for _, sr := range ss {
						if len(sr.Points) == 0 {
							continue
						}
						k := min(3, len(sr.Points))
						f, _ := summarise(sr.Points[:k], "avg")
						l, _ := summarise(sr.Points[len(sr.Points)-k:], "avg")
						first, last = first+f, last+l
					}
					bad := worseUp.MatchString(p.Title+" "+t.Query) && last > 2*first && last > 0
					add(Evidence{Kind: "metrics", Service: svc, Bad: bad, Summary: fmt.Sprintf("%s (%s) for %s: %s at the start of the window, %s now",
						cmp.Or(p.Title, t.Query), dn, svc, viz.Human(first, p.Unit), viz.Human(last, p.Unit))})
				}
			}
		}
	}
}

// changeEvidence: what the assistant changed, and when rig last changed the environment's state.
func (a *App) changeEvidence(window time.Duration, add func(Evidence)) {
	es, _ := ai.ReadAudit(a.AuditFile())
	for _, e := range es {
		if time.Since(e.At) < window && e.Risk != ai.Read.String() && e.Result == "ok" {
			add(Evidence{Kind: "change", At: e.At, Summary: fmt.Sprintf("the assistant ran %s %s ago", e.Command, time.Since(e.At).Round(time.Second))})
		}
	}
	if fi, err := os.Stat(filepath.Join(a.StateDir(), "state.json")); err == nil && time.Since(fi.ModTime()) < window {
		add(Evidence{Kind: "change", At: fi.ModTime(), Summary: fmt.Sprintf("rig changed this environment's state (a deploy, tag or env) %s ago", time.Since(fi.ModTime()).Round(time.Second))})
	}
}

type hosted struct{ name, host string }

// componentHosts are the components whose addr is a rig service (svc://name:port).
func (a *App) componentHosts() []hosted {
	var out []hosted
	for _, n := range a.componentNames() {
		var o struct {
			Addr string `yaml:"addr"`
		}
		if a.Spec.Components[n].Decode(&o) != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(o.Addr, "svc://"); ok {
			host, _, _ := strings.Cut(rest, ":")
			out = append(out, hosted{n, host})
		}
	}
	return out
}

// Rank scores each service by the bad evidence against it: being down outweighs failing reach,
// which outweighs being named in another service's errors, then its own errors and restarts.
func Rank(target string, ev []Evidence) []Suspect {
	score := map[string]int{}
	cites := map[string][]string{}
	blame := func(svc string, n int, id string) {
		if svc == "" {
			return
		}
		score[svc] += n
		if !contains(cites[svc], id) {
			cites[svc] = append(cites[svc], id)
		}
	}
	for _, e := range ev {
		if !e.Bad {
			continue
		}
		switch e.Kind {
		case "status":
			if strings.Contains(e.Summary, "restarted") {
				blame(e.Service, 2, e.ID)
			} else {
				blame(e.Service, 5, e.ID)
			}
		case "reach":
			blame(e.Service, 4, e.ID)
		case "health":
			blame(e.Service, 3, e.ID)
		case "logs":
			blame(e.Service, 1, e.ID)
			for _, b := range e.Blames {
				blame(b, 3, e.ID)
			}
		case "traces", "alert", "metrics":
			blame(e.Service, 1, e.ID)
		}
	}
	var out []Suspect
	for s, n := range score {
		out = append(out, Suspect{Service: s, Score: n, Evidence: cites[s]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// a tie goes to the dependency: the asked-about service is usually the victim
		if (out[i].Service == target) != (out[j].Service == target) {
			return out[j].Service == target
		}
		return out[i].Service < out[j].Service
	})
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// FixPrompt is what "fix" after a rig why analysis tells the assistant.
const FixPrompt = `fix: apply the fix you proposed, now, with rig's tools (rig_file edits, rig_kv, rig_service, rig_deploy, ...); every change waits for the user's yes. Then check it worked with the same evidence (logs, traces, metrics) and say whether the symptom is gone.`

// Brief is one line about what was gathered: how much evidence, how much of it bad, the top suspects.
func (inc *Incident) Brief() string {
	bad := 0
	for _, e := range inc.Evidence {
		if e.Bad {
			bad++
		}
	}
	var top []string
	for _, s := range inc.Suspects[:min(3, len(inc.Suspects))] {
		top = append(top, fmt.Sprintf("%s (%d)", s.Service, s.Score))
	}
	line := fmt.Sprintf("%d pieces of evidence over %d services, %d bad", len(inc.Evidence), len(inc.Checked), bad)
	if len(top) > 0 {
		line += "; suspects: " + strings.Join(top, ", ")
	}
	return line
}

// Prompt is what the assistant is asked about the incident.
func (inc *Incident) Prompt() string {
	var b strings.Builder
	what := fmt.Sprintf("Service %q", inc.Service)
	if inc.Service == "" {
		what = "Something"
	}
	fmt.Fprintf(&b, "%s on environment %s misbehaves. rig gathered this evidence over the last %s (services checked: %s).\n\n",
		what, inc.Env, inc.Window, strings.Join(inc.Checked, ", "))
	if inc.Symptom != "" {
		fmt.Fprintf(&b, "The user describes the symptom as: %q\n\n", inc.Symptom)
	}
	inc.writeEvidence(&b)
	b.WriteString("\nrig's rule-based ranking of suspects: ")
	for i, s := range inc.Suspects {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (score %d: %s)", s.Service, s.Score, strings.Join(s.Evidence, " "))
	}
	b.WriteString(`

Answer in Markdown, short:
## Summary
one or two sentences naming the most likely root cause.
## Hypotheses
a numbered list, most likely first; each names the service at fault, why, and cites evidence IDs like [E3].
## Proposed fix
the change that would fix it: the file and the edit, the KV key and value, the config, or the rig
command (restart, scale, deploy), with how to check it worked. Say what you would still need to
know when the evidence does not settle it.
## Next steps
up to four concrete commands or checks (rig logs, rig restart, rig data, kubectl, ...).
Use the evidence above; when you have rig's tools, use them to look further (logs, traces, metrics,
the code) before you answer. Change nothing yet: the user answers "fix" to have you apply it.
`)
	return b.String()
}

func (inc *Incident) writeEvidence(w io.Writer) {
	for _, e := range inc.Evidence {
		mark := "ok"
		if e.Bad {
			mark = "BAD"
		}
		fmt.Fprintf(w, "- [%s] %s %s: %s\n", e.ID, mark, e.Kind, e.Summary)
		for _, l := range e.Lines {
			if len(l) > 300 {
				l = l[:300] + "…"
			}
			fmt.Fprintf(w, "    > %s\n", l)
		}
	}
}

// NextSteps are rig's own suggestions for the top suspect, for when no AI answers.
func (inc *Incident) NextSteps() []string {
	if len(inc.Suspects) == 0 {
		return []string{strings.TrimSpace("rig logs -F " + inc.Service), "rig doctor"}
	}
	top := inc.Suspects[0].Service
	return []string{"rig status " + top, "rig logs " + top, "rig restart " + top + "   (if it is down or wedged)", "rig doctor   (tools, components, ports)"}
}

// Markdown writes the incident report: summary (the AI's analysis when there is one), timeline,
// evidence and next steps.
func (inc *Incident) Markdown(w io.Writer, analysis string) {
	fmt.Fprintf(w, "# Incident: %s on %s\n\n%s · window %s · checked %s\n\n", cmp.Or(inc.Symptom, inc.Service), inc.Env, inc.At.Format("2006-01-02 15:04:05"), inc.Window, strings.Join(inc.Checked, ", "))
	if strings.TrimSpace(analysis) != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(analysis))
	} else {
		fmt.Fprintf(w, "## Summary\n\n")
		for i, s := range inc.Suspects {
			fmt.Fprintf(w, "%d. **%s** (score %d) — %s\n", i+1, s.Service, s.Score, strings.Join(s.Evidence, ", "))
		}
		if len(inc.Suspects) == 0 {
			fmt.Fprintln(w, "No bad evidence found.")
		}
		fmt.Fprintf(w, "\n## Next steps\n\n")
		for _, s := range inc.NextSteps() {
			fmt.Fprintf(w, "- `%s`\n", s)
		}
		fmt.Fprintln(w)
	}
	var tl []Evidence
	for _, e := range inc.Evidence {
		if !e.At.IsZero() {
			tl = append(tl, e)
		}
	}
	sort.Slice(tl, func(i, j int) bool { return tl[i].At.Before(tl[j].At) })
	if len(tl) > 0 {
		fmt.Fprintf(w, "## Timeline\n\n")
		for _, e := range tl {
			fmt.Fprintf(w, "- %s [%s] %s\n", e.At.Format("15:04:05"), e.ID, e.Summary)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "## Evidence\n\n")
	inc.writeEvidence(w)
}
