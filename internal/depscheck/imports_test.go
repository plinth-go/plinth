package depscheck

import (
	"os/exec"
	"path"
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
	"net/http/pprof",
}

var optionalFrameworkPackages = []string{
	modulePath + "kernel/wshub",
	modulePath + "kernel/auth",
	modulePath + "kernel/authz",
	modulePath + "kernel/cors",
	modulePath + "kernel/uploads",
	modulePath + "platform/csrf",
	modulePath + "platform/rediscsrf",
	modulePath + "platform/jobs",
	modulePath + "platform/jobs/admin",
	modulePath + "platform/jobs/jobutil",
	modulePath + "platform/jobs/migrations",
	modulePath + "platform/pgjobs",
	modulePath + "platform/migrate",
	modulePath + "platform/pgmigrate",
	modulePath + "platform/idempotency",
	modulePath + "platform/jwtauth",
	modulePath + "platform/oidcauth",
	modulePath + "platform/redislock",
	modulePath + "platform/objectstore",
	modulePath + "platform/objectstore/localfs",
	modulePath + "platform/objectstore/s3store",
	modulePath + "platform/observability/prommetrics",
	modulePath + "platform/observability/oteltrace",
	modulePath + "platform/pgdb",
	modulePath + "platform/ratelimit",
	modulePath + "platform/redislimit",
	modulePath + "platform/redisclient",
	modulePath + "platform/session",
	modulePath + "platform/redissession",
}

func TestPublicPackageNamesAreUnambiguous(t *testing.T) {
	command := exec.Command(goTool(t), "list", "-f", "{{.ImportPath}} {{.Name}}", "./...")
	command.Dir = moduleRoot(t)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list packages: %v", err)
	}
	forbidden := map[string]bool{
		"jwt":        true,
		"oidc":       true,
		"otel":       true,
		"postgres":   true,
		"prometheus": true,
		"redis":      true,
		"s3":         true,
		"websocket":  true,
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("unexpected go list line %q", line)
		}
		importPath, packageName := fields[0], fields[1]
		if packageName == "main" {
			continue
		}
		relative := strings.TrimPrefix(importPath, modulePath)
		if relative == "internal" || strings.HasPrefix(relative, "internal/") || strings.Contains(relative, "/internal/") {
			continue
		}
		if got := path.Base(importPath); got != packageName {
			t.Errorf("package %s declares %q; directory name is %q", importPath, packageName, got)
		}
		if forbidden[packageName] {
			t.Errorf("package %s uses a bare vendor name; use technology-plus-responsibility naming", importPath)
		}
	}
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
		{pkg: "kernel/wshub", allowed: set("github.com/gorilla/websocket")},
		{pkg: "kernel/auth", allowed: set()},
		{pkg: "kernel/authz", allowed: set()},
		{pkg: "kernel/cors", allowed: set()},
		{pkg: "kernel/uploads", allowed: set()},
		{pkg: "platform/csrf", allowed: set()},
		{pkg: "platform/rediscsrf", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/jobs", allowed: set()},
		{pkg: "platform/jobs/admin", allowed: set()},
		{pkg: "platform/jobs/jobutil", allowed: set()},
		{pkg: "platform/jobs/migrations", allowed: set()},
		{pkg: "platform/pgjobs", allowed: set("github.com/jackc/pgx/v5")},
		{pkg: "platform/idempotency", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/jwtauth", allowed: set("github.com/golang-jwt/jwt/v5")},
		{pkg: "platform/oidcauth", allowed: set("github.com/coreos/go-oidc/v3", "github.com/go-jose/go-jose/v4")},
		{pkg: "platform/objectstore", allowed: set()},
		{pkg: "platform/objectstore/localfs", allowed: set()},
		{pkg: "platform/objectstore/s3store", allowed: set("github.com/aws/aws-sdk-go-v2", "github.com/aws/smithy-go")},
		{pkg: "platform/redislock", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/ratelimit", allowed: set()},
		{pkg: "platform/redislimit", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/observability/prommetrics", allowed: set("github.com/prometheus/client_golang")},
		{pkg: "platform/observability/oteltrace", allowed: set("go.opentelemetry.io/otel")},
		{pkg: "platform/pgdb", allowed: set("github.com/jackc/pgx/v5")},
		{pkg: "platform/redisclient", allowed: set("github.com/redis/go-redis/v9")},
		{pkg: "platform/session", allowed: set("github.com/alexedwards/scs/v2")},
		{pkg: "platform/redissession", allowed: set("github.com/alexedwards/scs/v2", "github.com/redis/go-redis/v9")},
		{pkg: "platform/migrate", allowed: set("github.com/pressly/goose/v3")},
		{pkg: "platform/pgmigrate", allowed: set("github.com/jackc/pgx/v5", "github.com/pressly/goose/v3")},
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
		name := "go"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(root, "bin", name)
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
