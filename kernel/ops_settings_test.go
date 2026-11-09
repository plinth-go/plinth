package kernel

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestOpsSettingsApply(t *testing.T) {
	base := OpsConfig{
		LivePath:    "/health",
		ReadyPath:   "/health/ready",
		OpenAPIPath: "/spec",
		DocsPath:    "/manual/",
		Checks:      []ReadyCheck{{Name: "test"}},
		Timeout:     3 * time.Second,
		Logger:      slog.Default(),
		Live:        func(http.ResponseWriter, *http.Request) {},
	}

	for bits := 0; bits < 8; bits++ {
		settings := OpsSettings{
			LivenessEnabled:  bits&1 != 0,
			LivenessPath:     "/healthz",
			ReadinessEnabled: bits&2 != 0,
			ReadinessPath:    "/readyz-custom",
			OpenAPIEnabled:   bits&4 != 0,
			OpenAPIPath:      "/spec.json",
			DocsPath:         "/manual/",
		}
		t.Run(fmt.Sprintf("%03b", bits), func(t *testing.T) {
			got := settings.Apply(base)
			if got.DisableLive != !settings.LivenessEnabled {
				t.Fatalf("DisableLive = %t, LivenessEnabled = %t", got.DisableLive, settings.LivenessEnabled)
			}
			if got.DisableReady != !settings.ReadinessEnabled {
				t.Fatalf("DisableReady = %t, ReadinessEnabled = %t", got.DisableReady, settings.ReadinessEnabled)
			}
			if got.DisableOpenAPI != !settings.OpenAPIEnabled {
				t.Fatalf("DisableOpenAPI = %t, OpenAPIEnabled = %t", got.DisableOpenAPI, settings.OpenAPIEnabled)
			}
			if got.LivePath != settings.LivenessPath || got.ReadyPath != settings.ReadinessPath ||
				got.OpenAPIPath != settings.OpenAPIPath || got.DocsPath != settings.DocsPath ||
				len(got.Checks) != len(base.Checks) || got.Timeout != base.Timeout ||
				got.Logger != base.Logger || got.Live == nil {
				t.Fatalf("Apply did not preserve runtime configuration: %#v", got)
			}
		})
	}

	got := OpsSettings{LivenessEnabled: true, ReadinessEnabled: true, OpenAPIEnabled: true}.Apply(base)
	if got.LivePath != base.LivePath || got.ReadyPath != base.ReadyPath || got.OpenAPIPath != base.OpenAPIPath || got.DocsPath != base.DocsPath {
		t.Fatalf("empty settings paths did not preserve runtime paths: %#v", got)
	}
}

func TestDefaultOpsSettingsEnablesEverything(t *testing.T) {
	settings := DefaultOpsSettings()
	got := settings.Apply(OpsConfig{})
	if got.DisableLive || got.DisableReady || got.DisableOpenAPI {
		t.Fatalf("default settings disabled operations: %#v", got)
	}
	if got.LivePath != settings.LivenessPath || got.ReadyPath != settings.ReadinessPath || got.OpenAPIPath != settings.OpenAPIPath || got.DocsPath != settings.DocsPath {
		t.Fatalf("default settings paths were not applied: %#v", got)
	}
}
