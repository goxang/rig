package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// ScrapeTarget is one entry of a Prometheus file_sd file.
type ScrapeTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// ScrapeTargets lists the environment's services with metrics as a Prometheus container on the
// docker runtime's network sees them: containers by name, local processes through host (the
// container's name for the host, host.docker.internal say).
func (a *App) ScrapeTargets(ctx context.Context, host string) ([]ScrapeTarget, error) {
	if a.onKubernetes() {
		return nil, fmt.Errorf("%s is on Kubernetes: scrape it with the cluster's own Prometheus", a.envName())
	}
	var out []ScrapeTarget
	for _, n := range a.Spec.ServiceNames() {
		s := a.Spec.Services[n]
		if s.Metrics == nil || a.SharedElsewhere(n) {
			continue
		}
		port := s.PortNumber(s.Metrics.Port)
		if port == 0 {
			continue
		}
		addr := net.JoinHostPort(n, strconv.Itoa(port))
		if a.Env.Runtime.Type != "docker" {
			r, err := a.Resolve(ctx, fmt.Sprintf("svc://%s:%d", n, port))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
			h, p, err := net.SplitHostPort(r)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
			if h == "127.0.0.1" || h == "localhost" || h == "::1" {
				h = host
			}
			addr = net.JoinHostPort(h, p)
		}
		labels := map[string]string{"service": n, "env": a.Env.Name}
		if s.Metrics.Path != "" && s.Metrics.Path != "/metrics" {
			labels["__metrics_path__"] = s.Metrics.Path
		}
		out = append(out, ScrapeTarget{Targets: []string{addr}, Labels: labels})
	}
	return out, nil
}

func MarshalTargets(ts []ScrapeTarget) ([]byte, error) {
	if ts == nil {
		ts = []ScrapeTarget{}
	}
	return json.MarshalIndent(ts, "", "  ")
}
