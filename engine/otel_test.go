package engine

import (
	"testing"

	"github.com/goxang/rig/spec"
)

func TestOTelEnv(t *testing.T) {
	half := 0.5
	a := &App{
		Spec: &spec.Project{Name: "shop", OTel: &spec.OTel{Endpoint: "svc://collector:otlp", Attributes: map[string]string{"team": "pay"}},
			Services: map[string]*spec.Service{"collector": {Name: "collector", Ports: map[string]int{"otlp": 4318}}}},
		Env: &spec.Environment{Name: "docker", Runtime: &spec.Component{Type: "docker"},
			OTel: &spec.OTel{Logs: "http://loki:3100/otlp/v1/logs", Sample: &half}},
	}
	env := a.otelEnv(&spec.Service{Name: "api"})
	want := map[string]string{
		"OTEL_SERVICE_NAME":                "api",
		"OTEL_RESOURCE_ATTRIBUTES":         "service.namespace=shop,deployment.environment=docker,team=pay",
		"OTEL_EXPORTER_OTLP_ENDPOINT":      "http://collector:4318",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://loki:3100/otlp/v1/logs",
		"OTEL_TRACES_EXPORTER":             "otlp",
		"OTEL_TRACES_SAMPLER_ARG":          "0.5",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if a.otelEnv(&spec.Service{Name: "db", Role: spec.RoleInfra}) != nil {
		t.Error("infrastructure got OTEL_* variables")
	}
}
