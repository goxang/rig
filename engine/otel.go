package engine

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/spec"
)

// otel is the project's otel: with the environment's on top, nil when neither has one.
func (a *App) otel() *spec.OTel {
	p, e := a.Spec.OTel, (*spec.OTel)(nil)
	if a.Env != nil {
		e = a.Env.OTel
	}
	if p == nil || e == nil {
		if p != nil {
			return p
		}
		return e
	}
	o := *p
	for _, f := range [][2]*string{{&o.Endpoint, &e.Endpoint}, {&o.Traces, &e.Traces}, {&o.Metrics, &e.Metrics}, {&o.Logs, &e.Logs}, {&o.Protocol, &e.Protocol}} {
		if *f[1] != "" {
			*f[0] = *f[1]
		}
	}
	if e.Sample != nil {
		o.Sample = e.Sample
	}
	o.Attributes = map[string]string{}
	for _, m := range []map[string]string{p.Attributes, e.Attributes} {
		for k, v := range m {
			o.Attributes[k] = v
		}
	}
	return &o
}

// otelEnv is the OTEL_* environment of service s.
func (a *App) otelEnv(s *spec.Service) map[string]string {
	o := a.otel()
	if o == nil || s.Role == spec.RoleInfra {
		return nil
	}
	attrs := []string{"service.namespace=" + a.Spec.Name, "deployment.environment=" + a.Env.Name}
	var extra []string
	for k, v := range o.Attributes {
		extra = append(extra, k+"="+v)
	}
	sort.Strings(extra)
	env := map[string]string{
		"OTEL_SERVICE_NAME":           s.Name,
		"OTEL_RESOURCE_ATTRIBUTES":    strings.Join(append(attrs, extra...), ","),
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_PROPAGATORS":            "tracecontext,baggage",
	}
	if o.Protocol != "" {
		env["OTEL_EXPORTER_OTLP_PROTOCOL"] = o.Protocol
	}
	if o.Endpoint != "" {
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = a.reachable(o.Endpoint)
	}
	for _, sig := range []struct{ name, addr string }{{"TRACES", o.Traces}, {"METRICS", o.Metrics}, {"LOGS", o.Logs}} {
		exporter := "none"
		if sig.addr != "" {
			env["OTEL_EXPORTER_OTLP_"+sig.name+"_ENDPOINT"] = a.reachable(sig.addr)
		}
		if sig.addr != "" || o.Endpoint != "" {
			exporter = "otlp"
		}
		env["OTEL_"+sig.name+"_EXPORTER"] = exporter
	}
	if o.Sample != nil {
		env["OTEL_TRACES_SAMPLER"] = "parentbased_traceidratio"
		env["OTEL_TRACES_SAMPLER_ARG"] = strconv.FormatFloat(*o.Sample, 'f', -1, 64)
	}
	return env
}

// reachable turns svc://service:port[/path] into an http URL the services reach: the service's own
// name inside a container network or cluster, a forwarded address for local processes.
func (a *App) reachable(addr string) string {
	rest, ok := strings.CutPrefix(addr, "svc://")
	if !ok {
		return addr
	}
	if a.Env.Runtime.Type == "local" {
		if r, ok := a.reached.Load(addr); ok {
			return r.(string)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if r, err := a.Resolve(ctx, addr); err == nil {
			a.reached.Store(addr, "http://"+r)
			return "http://" + r
		}
	}
	hostport, path, _ := strings.Cut(rest, "/")
	host, port, _ := strings.Cut(hostport, ":")
	if s, ok := a.Spec.Services[host]; ok {
		if n := s.PortNumber(port); n > 0 {
			port = strconv.Itoa(n)
		}
	}
	out := "http://" + host + ":" + port
	if path != "" {
		out += "/" + path
	}
	return out
}
