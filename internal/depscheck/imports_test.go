package depscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const modulePath = "github.com/plinth-go/plinth/"

var optionalDependencies = []string{
	"github.com/alexedwards/scs/v2",
	"github.com/gorilla/websocket",
	"github.com/jackc/pgx/v5",
	"github.com/pressly/goose/v3",
	"github.com/redis/go-redis/v9",
	"github.com/golang-jwt/jwt/v5",
	"github.com/coreos/go-oidc/v3",
	"github.com/go-jose/go-jose/v4",
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/smithy-go",
	"github.com/prometheus/client_golang",
	"go.opentelemetry.io/otel",
}

var optionalFrameworkPackages = []string{
	modulePath + "kernel/websocket",
	modulePath + "kernel/auth",
	modulePath + "kernel/authz",
	modulePath + "kernel/cors",
	modulePath + "kernel/uploads",
	modulePath + "platform/csrf",
	modulePath + "platform/csrf/redis",
	modulePath + "platform/jobs",
	modulePath + "platform/jobs/admin",
	modulePath + "platform/jobs/migrations",
	modulePath + "platform/migrate",
	modulePath + "platform/idempotency",
	modulePath + "platform/jwt",
	modulePath + "platform/oidc",
	modulePath + "platform/redislock",
	modulePath + "platform/objectstore",
	modulePath + "platform/objectstore/localfs",
	modulePath + "platform/objectstore/s3",
	modulePath + "platform/observability/prometheus",
	modulePath + "platform/observability/otel",
	modulePath + "platform/postgres",
	modulePath + "platform/ratelimit",
	modulePath + "platform/ratelimit/redis",
	modulePath + "platform/redis",
	modulePath + "platform/session",
	modulePath + "platform/session/redis",
}

func TestCorePackagesDoNotCompileOptionalDependencies(t *testing.T) {
	for _, pkg := range []string{
		"app",
		"kernel",
		"logging",
		"runtime",
		"utils",
		"webui",
	} {
		t.Run(pkg, func(t *testing.T) {
			dependencies := packageDependencies(t, modulePath+pkg)
			for _, optional := range optionalDependencies {
				if hasDependency(dependencies, optional) {
					t.Fatalf("%s unexpectedly depends on %s", pkg, optional)
				}
			}
			for _, optional := range optionalFrameworkPackages {
				if hasDependency(dependencies, optional) {
					t.Fatalf("%s unexpectedly depends on optional package %s", pkg, optional)
				}
			}
		})
	}
}

func TestOptionalPackagesOnlyPullTheirDependencies(t *testing.T) {
	tests := []struct {
		pkg     string
		allowed map[string]bool
	}{
		{pkg: "kernel/websocket", allowed: set("github.com/gorilla/websocket")},
		{pkg: "kernel/auth", allowed: set()},
		{pkg: "kernel/authz", allowed: set()},
		{pkg: "kernel/cors", allowed: set()},
		{pkg: "kernel/uploads", allowed: set()},
		{pkg: "platform/csrf", allowed: set()},
		{pkg: "platform/csrf/redis", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/jobs", allowed: set("github.com/jackc/pgx/v5")},
		{pkg: "platform/jobs/admin", allowed: set("github.com/jackc/pgx/v5")},
		{pkg: "platform/jobs/migrations", allowed: set()},
		{pkg: "platform/idempotency", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/jwt", allowed: set("github.com/golang-jwt/jwt/v5")},
		{pkg: "platform/oidc", allowed: set("github.com/coreos/go-oidc/v3", "github.com/go-jose/go-jose/v4")},
		{pkg: "platform/objectstore", allowed: set()},
		{pkg: "platform/objectstore/localfs", allowed: set()},
		{pkg: "platform/objectstore/s3", allowed: set("github.com/aws/aws-sdk-go-v2", "github.com/aws/smithy-go")},
		{pkg: "platform/redislock", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/ratelimit", allowed: set()},
		{pkg: "platform/ratelimit/redis", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/observability/prometheus", allowed: set("github.com/prometheus/client_golang")},
		{pkg: "platform/observability/otel", allowed: set("go.opentelemetry.io/otel")},
		{pkg: "platform/postgres", allowed: set("github.com/jackc/pgx/v5")},
		{pkg: "platform/redis", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/session", allowed: set("github.com/alexedwards/scs/v2")},
		{pkg: "platform/session/redis", allowed: set("github.com/alexedwards/scs/v2", "github.com/redis/go-redis/v9")},
		{pkg: "platform/migrate", allowed: set("github.com/jackc/pgx/v5", "github.com/pressly/goose/v3")},
	}
	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			dependencies := packageDependencies(t, modulePath+tt.pkg)
			for _, optional := range optionalDependencies {
				if hasDependency(dependencies, optional) && !tt.allowed[optional] {
					t.Fatalf("%s unexpectedly depends on %s", tt.pkg, optional)
				}
			}
			for optional := range tt.allowed {
				if !hasDependency(dependencies, optional) {
					t.Fatalf("%s should depend on %s", tt.pkg, optional)
				}
			}
		})
	}
}

func hasDependency(dependencies map[string]bool, prefix string) bool {
	for dependency := range dependencies {
		if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
			return true
		}
	}
	return false
}

func packageDependencies(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	command := exec.Command(goTool(t), "list", "-deps", pkg)
	command.Dir = moduleRoot(t)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	dependencies := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		if line != "" {
			dependencies[line] = true
		}
	}
	return dependencies
}

func goTool(t *testing.T) string {
	t.Helper()
	if root := runtime.GOROOT(); root != "" {
		candidate := filepath.Join(root, "bin", "go")
		if info, err := exec.LookPath(candidate); err == nil && info != "" {
			return candidate
		}
	}
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate go tool: %v", err)
	}
	return path
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dependency test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
