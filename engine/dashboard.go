package engine

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

// AllValue is the "all" choice of a dashboard variable: a regex that matches anything.
const AllValue = "$__all"

// VarValues are the choices of a dashboard variable: its fixed values, or the values of its label
// over the series its query returns now.
func (a *App) VarValues(ctx context.Context, v *spec.DashVar) ([]string, error) {
	if len(v.Values) > 0 || v.Query == "" {
		return v.Values, nil
	}
	m, _, err := Get[core.Metrics](a, core.KindMetrics, v.Source)
	if err != nil {
		return nil, err
	}
	ss, err := m.Instant(ctx, v.Query)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if x := s.Labels[v.Label]; x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DefaultVar is what a variable starts as: its default, else all (when offered), else the first value.
func DefaultVar(v *spec.DashVar, values []string) []string {
	switch {
	case v.Default != "":
		return []string{v.Default}
	case v.All:
		return []string{AllValue}
	case len(values) > 0:
		return values[:1]
	}
	return nil
}

var dashVar = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

// ExpandQuery fills a panel query's $variables (several values become a|b, all becomes .*) and
// Grafana's $__range, $__interval and $__rate_interval for the time range and step.
func ExpandQuery(q string, vars map[string][]string, rng, step time.Duration) string {
	if !strings.Contains(q, "$") {
		return q
	}
	return dashVar.ReplaceAllStringFunc(q, func(m string) string {
		name := strings.Trim(m, "${}")
		switch name {
		case "__range":
			return promDuration(rng)
		case "__interval":
			return promDuration(step)
		case "__rate_interval":
			return promDuration(max(4*step, time.Minute))
		}
		vs, ok := vars[name]
		if !ok {
			return m
		}
		var parts []string
		for _, v := range vs {
			if v == AllValue {
				return ".*"
			}
			parts = append(parts, v)
		}
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, "|") + ")"
	})
}

func promDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return strings.TrimSuffix(d.String(), "0s")
	}
	return (d.Round(time.Second) / time.Second * time.Second).String()
}
