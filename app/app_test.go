package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/kernel"
	"github.com/urfave/cli/v3"
)

type testDatabase struct {
	Port int `env:"PORT,default=5432"`
}

type testConfig struct {
	HTTPAddr string       `env:"HTTP_ADDR,default=:8080"`
	Token    string       `env:"TOKEN,required"`
	Database testDatabase `env:"DB"`
	Optional string       `env:"OPTIONAL"`
	Ignored  string       `env:"-"`
}

type testEnvDecoder struct{}

func (*testEnvDecoder) EnvDecode(context.Context, string) error { return nil }

type testRecordingDecoder struct {
	Value string
}

func (d *testRecordingDecoder) EnvDecode(_ context.Context, value string) error {
	d.Value = value
	return nil
}

func TestEnvTemplate(t *testing.T) {
	data, err := EnvTemplate(&testConfig{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"HTTP_ADDR=:8080\n",
		"# required\nTOKEN=\n",
		"DB_PORT=5432\n",
		"# OPTIONAL=\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Ignored") || strings.Contains(text, " -=") {
		t.Fatalf("ignored field leaked:\n%s", text)
	}
}

func TestEnvTemplateWritesFieldDescriptions(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,default=ok" envdoc:"First line.\nSecond line."`
	}

	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	want := "# First line.\n# Second line.\nVALUE=ok\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("missing description in:\n%s", data)
	}
}

func TestEnvTemplateRejectsInvalidFieldDescriptions(t *testing.T) {
	tests := []struct {
		name string
		cfg  any
		want string
	}{
		{
			name: "nested struct",
			cfg: struct {
				Nested struct{} `env:"" envdoc:"not supported"`
			}{},
			want: "envdoc is only supported on value fields",
		},
		{
			name: "ignored field",
			cfg: struct {
				Value string `env:"-" envdoc:"not supported"`
			}{},
			want: "ignored env field cannot have options",
		},
		{
			name: "NUL",
			cfg: struct {
				Value string `env:"VALUE" envdoc:"bad\x00value"`
			}{},
			want: "envdoc must not contain NUL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := EnvTemplate(test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnvTemplateKeepsNestedEnvNames(t *testing.T) {
	type flatConfig struct {
		RetryBase time.Duration `env:"JOB_RETRY_BASE,default=1s"`
		RetryMax  time.Duration `env:"JOB_RETRY_MAX,default=1m"`
	}
	type jobConfig struct {
		RetryBase time.Duration `env:"RETRY_BASE,default=1s"`
		RetryMax  time.Duration `env:"RETRY_MAX,default=1m"`
	}
	type implicitSegment struct {
		Job jobConfig `env:""`
	}
	type implicitNoTagSegment struct {
		Job jobConfig
	}
	type explicitSegment struct {
		Job jobConfig `env:"JOB"`
	}

	flat, err := EnvTemplate(flatConfig{}, WithEnvPrefix("SHELF"))
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]any{
		"implicit field segment":        implicitSegment{},
		"implicit field segment no tag": implicitNoTagSegment{},
		"explicit env segment":          explicitSegment{},
	} {
		t.Run(name, func(t *testing.T) {
			nested, err := EnvTemplate(config, WithEnvPrefix("SHELF_"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(nested, flat) {
				t.Fatalf("nested template differs from flat template:\nflat:\n%s\nnested:\n%s", flat, nested)
			}
		})
	}
}

func TestEnvTemplateDerivesFieldSegments(t *testing.T) {
	type config struct {
		HTTPAddr string `env:",default=:8080"`
		Job      struct {
			PollInterval time.Duration `env:",default=500ms"`
		} `env:""`
	}

	data, err := EnvTemplate(config{}, WithEnvPrefix("SHELF"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SHELF_HTTP_ADDR=:8080\n", "SHELF_JOB_POLL_INTERVAL=500ms\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in:\n%s", want, data)
		}
	}
}

func TestEnvSegment(t *testing.T) {
	for input, want := range map[string]string{
		"Job":               "JOB",
		"PollInterval":      "POLL_INTERVAL",
		"HTTPAddr":          "HTTP_ADDR",
		"HTTP2Enabled":      "HTTP2_ENABLED",
		"OpsOpenAPIEnabled": "OPS_OPEN_API_ENABLED",
	} {
		if got := envSegment(input); got != want {
			t.Errorf("envSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLoadEnvLoadsNestedStruct(t *testing.T) {
	type jobConfig struct {
		RetryBase time.Duration `env:"RETRY_BASE,default=1s"`
		RetryMax  time.Duration `env:"RETRY_MAX,default=1m"`
	}
	type config struct {
		Job jobConfig `env:""`
	}

	t.Setenv("PLINTH_TEST_ENV_JOB_RETRY_BASE", "2500ms")
	t.Setenv("PLINTH_TEST_ENV_JOB_RETRY_MAX", "45s")

	var cfg config
	if err := LoadEnv(context.Background(), &cfg, WithEnvPrefix("PLINTH_TEST_ENV")); err != nil {
		t.Fatal(err)
	}
	if cfg.Job.RetryBase != 2500*time.Millisecond || cfg.Job.RetryMax != 45*time.Second {
		t.Fatalf("loaded config = %#v", cfg)
	}
}

func TestEmbeddedOpsSettingsUsesNestedEnvKeys(t *testing.T) {
	type config struct {
		Ops kernel.OpsSettings `env:""`
	}
	const prefix = "PLINTH_TEST_OPS"

	template, err := EnvTemplate(config{}, WithEnvPrefix(prefix))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		prefix + "_OPS_LIVENESS_ENABLED=true\n",
		prefix + "_OPS_LIVENESS_PATH=/livez\n",
		prefix + "_OPS_READINESS_ENABLED=true\n",
		prefix + "_OPS_READINESS_PATH=/readyz\n",
		prefix + "_OPS_OPENAPI_ENABLED=true\n",
		prefix + "_OPS_OPENAPI_PATH=/openapi.json\n",
		prefix + "_OPS_DOCS_PATH=/docs/\n",
		prefix + "_OPS_ACCESS_LOG_ENABLED=false\n",
	} {
		if !strings.Contains(string(template), want) {
			t.Fatalf("missing %q in:\n%s", want, template)
		}
	}

	t.Setenv(prefix+"_OPS_READINESS_ENABLED", "false")
	t.Setenv(prefix+"_OPS_LIVENESS_PATH", "/healthz")
	t.Setenv(prefix+"_OPS_ACCESS_LOG_ENABLED", "true")
	var cfg config
	if err := LoadEnv(context.Background(), &cfg, WithEnvPrefix(prefix)); err != nil {
		t.Fatal(err)
	}
	if !cfg.Ops.LivenessEnabled || cfg.Ops.ReadinessEnabled || !cfg.Ops.OpenAPIEnabled || !cfg.Ops.AccessLogEnabled || cfg.Ops.LivenessPath != "/healthz" {
		t.Fatalf("Ops = %#v", cfg.Ops)
	}
}

func TestLoadEnvCreatesNestedPointerAndRunsDecoder(t *testing.T) {
	type jobConfig struct {
		Timeout time.Duration `env:"TIMEOUT,default=30s"`
	}
	type config struct {
		Job     *jobConfig           `env:""`
		Decoder testRecordingDecoder `env:"DECODER"`
	}

	t.Setenv("PLINTH_TEST_POINTER_JOB_TIMEOUT", "3s")
	t.Setenv("PLINTH_TEST_POINTER_DECODER", "decoded")

	var cfg config
	if err := LoadEnv(context.Background(), &cfg, WithEnvPrefix("PLINTH_TEST_POINTER")); err != nil {
		t.Fatal(err)
	}
	if cfg.Job == nil || cfg.Job.Timeout != 3*time.Second || cfg.Decoder.Value != "decoded" {
		t.Fatalf("loaded config = %#v", cfg)
	}
}

func TestLoadEnvRunsValidateTags(t *testing.T) {
	type jobConfig struct {
		Timeout    time.Duration `env:"TIMEOUT,default=30s" validate:"gt=0"`
		StaleAfter time.Duration `env:"STALE_AFTER,default=2m" validate:"gtfield=Timeout"`
	}
	type config struct {
		Job jobConfig `env:""`
	}

	t.Setenv("PLINTH_TEST_VALIDATE_JOB_TIMEOUT", "30s")
	t.Setenv("PLINTH_TEST_VALIDATE_JOB_STALE_AFTER", "10s")

	var cfg config
	err := LoadEnv(context.Background(), &cfg, WithEnvPrefix("PLINTH_TEST_VALIDATE"))
	if err == nil || !strings.Contains(err.Error(), "PLINTH_TEST_VALIDATE_JOB_STALE_AFTER") || !strings.Contains(err.Error(), `validate:"gtfield=Timeout"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadEnvReportsInvalidValidateTag(t *testing.T) {
	type config struct {
		Value string `env:"PLINTH_TEST_INVALID_RULE" validate:"not_a_rule"`
	}
	var cfg config
	err := LoadEnv(context.Background(), &cfg)
	if err == nil || !strings.Contains(err.Error(), "validate environment") {
		t.Fatalf("error = %v", err)
	}
}

func TestNestedDuplicateEnvName(t *testing.T) {
	type jobConfig struct {
		RetryBase time.Duration `env:"RETRY_BASE,default=1s"`
	}
	type acrossLevels struct {
		RetryBase time.Duration `env:"JOB_RETRY_BASE,default=1s"`
		Job       jobConfig     `env:""`
	}
	type siblingJob struct {
		Value string `env:"VALUE"`
	}
	type acrossSiblings struct {
		First  siblingJob `env:"JOB"`
		Second siblingJob `env:"JOB"`
	}

	t.Run("across levels", func(t *testing.T) {
		var config acrossLevels
		if _, err := EnvTemplate(config); err == nil || !strings.Contains(err.Error(), "duplicates env JOB_") {
			t.Fatalf("EnvTemplate error = %v", err)
		}
		if err := LoadEnv(context.Background(), &config); err == nil || !strings.Contains(err.Error(), "duplicates env JOB_") {
			t.Fatalf("LoadEnv error = %v", err)
		}
	})

	t.Run("across siblings", func(t *testing.T) {
		var config acrossSiblings
		if _, err := EnvTemplate(config); err == nil || !strings.Contains(err.Error(), "duplicates env JOB_") {
			t.Fatalf("EnvTemplate error = %v", err)
		}
		if err := LoadEnv(context.Background(), &config); err == nil || !strings.Contains(err.Error(), "duplicates env JOB_") {
			t.Fatalf("LoadEnv error = %v", err)
		}
	})
}

func TestEnvCommandWritesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env.example")
	application := Application{Name: "test", Config: &testConfig{}}
	var stdout bytes.Buffer
	if err := env(application, []string{"-out", out}, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != out+"\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".env.example" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestEnvCommandAppliesRootPrefix(t *testing.T) {
	type jobConfig struct {
		RetryBase time.Duration `env:"RETRY_BASE,default=1s"`
	}
	type config struct {
		Job jobConfig `env:""`
	}
	application := Application{Name: "test", Config: &config{}, EnvPrefix: "SHELF"}

	var stdout bytes.Buffer
	if err := env(application, []string{"-out", "-"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "SHELF_JOB_RETRY_BASE=1s\n") {
		t.Fatalf("template = %q", stdout.String())
	}
}

func TestEnvHelpSucceedsWithoutConfig(t *testing.T) {
	var stdout bytes.Buffer
	err := env(Application{Name: "test"}, []string{"-h"}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, want := range []string{"Usage: test env [flags]", "-out string", "-force"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in help:\n%s", want, text)
		}
	}
}

func TestCommandHelpSucceedsWithoutDependencies(t *testing.T) {
	for _, command := range []string{"serve", "check", "env"} {
		t.Run(command, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(context.Background(), Application{Name: "test"}, []string{command, "--help"}, &stdout)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), "Usage: test "+command) {
				t.Fatalf("help = %q", stdout.String())
			}
		})
	}
}

func TestTopLevelHelpUsesInjectedWriter(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), Application{Name: "test"}, []string{"help"}, &stdout); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, want := range []string{"Usage: test <command>", "serve", "check", "env", "help"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in help:\n%s", want, text)
		}
	}
}

func TestCustomCommandRunsWithFlags(t *testing.T) {
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name:  "greet",
			Usage: "Print a greeting",
			Flags: []Flag{
				&cli.StringFlag{Name: "name", Value: "world"},
			},
			Action: func(_ context.Context, command *cli.Command) error {
				_, err := fmt.Fprintf(command.Root().Writer, "hello %s\n", command.String("name"))
				return err
			},
		}},
	}
	var stdout, stderr bytes.Buffer
	if err := runIO(context.Background(), application, []string{"greet", "--name", "plinth"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello plinth\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCustomCommandCanRunMoreThanOnce(t *testing.T) {
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name: "greet",
			Flags: []Flag{
				&cli.StringFlag{Name: "name", Value: "world"},
			},
			Action: func(_ context.Context, command *cli.Command) error {
				_, err := fmt.Fprintln(command.Root().Writer, command.String("name"))
				return err
			},
		}},
	}
	for _, want := range []string{"first", "second"} {
		var stdout, stderr bytes.Buffer
		if err := runIO(context.Background(), application, []string{"greet", "--name", want}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != want+"\n" {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want+"\n")
		}
	}
}

func TestCustomCommandAliasRuns(t *testing.T) {
	called := false
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name:    "seed",
			Aliases: []string{"load"},
			Action: func(context.Context, *cli.Command) error {
				called = true
				return nil
			},
		}},
	}
	if err := Run(context.Background(), application, []string{"load"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("custom command alias did not run")
	}
}

func TestCustomCommandSupportsNestedSubcommands(t *testing.T) {
	called := false
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name: "admin",
			Commands: []*cli.Command{{
				Name: "ping",
				Action: func(_ context.Context, command *cli.Command) error {
					called = true
					_, err := fmt.Fprintln(command.Root().Writer, "pong")
					return err
				},
			}},
		}},
	}
	var stdout, stderr bytes.Buffer
	if err := runIO(context.Background(), application, []string{"admin", "ping"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("nested custom command did not run")
	}
	if stdout.String() != "pong\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCustomCommandHelpSucceedsWithoutDependencies(t *testing.T) {
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name:  "greet",
			Usage: "Print a greeting",
			Flags: []Flag{
				&cli.StringFlag{Name: "name", Usage: "name to greet"},
			},
		}},
	}
	var stdout, stderr bytes.Buffer
	if err := runIO(context.Background(), application, []string{"greet", "--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"greet", "--name", "name to greet"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in help:\n%s", want, stdout.String())
		}
	}
}

func TestTopLevelHelpListsCustomCommands(t *testing.T) {
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name:  "seed",
			Usage: "Load demo data",
		}},
	}
	var stdout bytes.Buffer
	if err := run(context.Background(), application, []string{"help"}, &stdout); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"seed", "Load demo data"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in help:\n%s", want, stdout.String())
		}
	}
}

func TestCustomCommandErrorsAreReturned(t *testing.T) {
	want := errors.New("custom failure")
	application := Application{
		Name: "test",
		Commands: []*Command{{
			Name: "fail",
			Action: func(context.Context, *cli.Command) error {
				return want
			},
		}},
	}
	err := Run(context.Background(), application, []string{"fail"})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestCustomCommandNamesCannotShadowFrameworkCommands(t *testing.T) {
	called := false
	application := Application{
		Name: "test",
		Serve: func(context.Context) error {
			called = true
			return nil
		},
		Commands: []*Command{{Name: "serve"}},
	}
	err := Run(context.Background(), application, []string{"serve"})
	if err == nil || !strings.Contains(err.Error(), "conflicts with a framework command") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("serve ran with an invalid command registration")
	}
}

func TestCustomCommandNamesAndAliasesMustBeUnique(t *testing.T) {
	tests := []struct {
		name     string
		commands []*Command
		want     string
	}{
		{
			name:     "duplicate name",
			commands: []*Command{{Name: "seed"}, {Name: "seed"}},
			want:     `command "seed" duplicates command "seed"`,
		},
		{
			name:     "alias duplicates name",
			commands: []*Command{{Name: "seed"}, {Name: "load", Aliases: []string{"seed"}}},
			want:     `command "seed" duplicates command "seed"`,
		},
		{
			name:     "alias shadows framework command",
			commands: []*Command{{Name: "seed", Aliases: []string{"check"}}},
			want:     `command "check" conflicts with a framework command`,
		},
		{
			name:     "invalid name",
			commands: []*Command{{Name: "two words"}},
			want:     "invalid name or alias",
		},
		{
			name:     "flag-like name",
			commands: []*Command{{Name: "-h"}},
			want:     "invalid name or alias",
		},
		{
			name:     "nil command",
			commands: []*Command{nil},
			want:     "command 0 is nil",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Run(context.Background(), Application{Name: "test", Commands: test.commands}, []string{"help"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCheckRunsWiringCheckWithValidatedRouter(t *testing.T) {
	router := kernel.NewRouter(nil, "test", "1")
	application := Application{
		Name:        "test",
		BuildRoutes: func() *kernel.Router { return router },
		CheckWiring: func(got *kernel.Router) error {
			if got != router {
				t.Fatalf("wiring check router = %p, want %p", got, router)
			}
			return nil
		},
	}
	if err := Run(context.Background(), application, []string{"check"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckReportsWiringFailure(t *testing.T) {
	want := errors.New("routes not wired")
	application := Application{
		Name:        "test",
		BuildRoutes: func() *kernel.Router { return kernel.NewRouter(nil, "test", "1") },
		CheckWiring: func(*kernel.Router) error { return want },
	}
	err := Run(context.Background(), application, []string{"check"})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "HTTP wiring check") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRecoversWiringPanic(t *testing.T) {
	application := Application{
		Name:        "test",
		BuildRoutes: func() *kernel.Router { return kernel.NewRouter(nil, "test", "1") },
		CheckWiring: func(*kernel.Router) error { panic("broken wiring check") },
	}
	err := Run(context.Background(), application, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), "HTTP wiring check failed: broken wiring check") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckDoesNotRunWiringCheckAfterRouteFailure(t *testing.T) {
	called := false
	application := Application{
		Name: "test",
		BuildRoutes: func() *kernel.Router {
			router := kernel.NewRouter(nil, "test", "1")
			router.Raw(http.MethodGet, "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			return router
		},
		CheckWiring: func(*kernel.Router) error {
			called = true
			return nil
		},
	}
	if err := Run(context.Background(), application, []string{"check"}); err == nil {
		t.Fatal("check unexpectedly passed")
	}
	if called {
		t.Fatal("wiring check ran after route validation failed")
	}
}

func TestEnvCommandPreservesExistingFileWithoutForce(t *testing.T) {
	out := filepath.Join(t.TempDir(), ".env.example")
	if err := os.WriteFile(out, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := Application{Name: "test", Config: &testConfig{}}
	err := Run(context.Background(), application, []string{"env", "-out", out})
	if err == nil || !strings.Contains(err.Error(), "already exists; use -force to overwrite") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep me\n" {
		t.Fatalf("file changed without -force: %q", data)
	}
	info, statErr := os.Stat(out)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEnvCommandForceAtomicallyReplacesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env.example")
	if err := os.WriteFile(out, []byte("old contents\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}

	application := Application{Name: "test", Config: &testConfig{}}
	if err := Run(context.Background(), application, []string{"env", "-out", out, "-force"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && os.SameFile(before, after) {
		t.Fatal("force rewrote the file in place instead of replacing it atomically")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", after.Mode().Perm())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "HTTP_ADDR=:8080\n") {
		t.Fatalf("generated file missing expected content:\n%s", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".env.example" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestEnvCommandForceReplacesSymlinkWithoutFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, ".env.example")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}

	application := Application{Name: "test", Config: &testConfig{}}
	if err := Run(context.Background(), application, []string{"env", "-out", out, "-force"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil {
		t.Fatal(err)
	} else if string(data) != "sentinel\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
	info, err := os.Lstat(out)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("output is still a symlink: %s", info.Mode())
	}
}

func TestEnvCommandRejectsDanglingSymlinkWithoutForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "missing")
	out := filepath.Join(dir, ".env.example")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}

	application := Application{Name: "test", Config: &testConfig{}}
	err := Run(context.Background(), application, []string{"env", "-out", out})
	if err == nil || !strings.Contains(err.Error(), "already exists; use -force to overwrite") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dangling symlink target was created: %v", statErr)
	}
}

func TestEnvRejectsPositionalArguments(t *testing.T) {
	out := filepath.Join(t.TempDir(), ".env.example")
	application := Application{Name: "test", Config: &testConfig{}}
	err := Run(context.Background(), application, []string{"env", "-out", out, "extra"})
	if err == nil || !strings.Contains(err.Error(), "takes no positional arguments") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("env wrote output despite invalid arguments: %v", statErr)
	}
}

func TestEnvRejectsEmptyOutputPath(t *testing.T) {
	application := Application{Name: "test", Config: &testConfig{}}
	err := Run(context.Background(), application, []string{"env", "-out", ""})
	if err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRecoversRegistrationPanic(t *testing.T) {
	application := Application{
		Name: "test",
		BuildRoutes: func() *kernel.Router {
			panic("invalid route tag")
		},
	}
	err := Run(context.Background(), application, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), "invalid route tag") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandsRejectExtraArguments(t *testing.T) {
	called := false
	application := Application{
		Name: "test",
		BuildRoutes: func() *kernel.Router {
			return kernel.NewRouter(nil, "test", "1")
		},
		Serve: func(context.Context) error {
			called = true
			return nil
		},
	}

	for _, command := range []string{"serve", "check", "help"} {
		err := Run(context.Background(), application, []string{command, "extra"})
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Fatalf("%s error = %v", command, err)
		}
	}
	if called {
		t.Fatal("serve ran despite extra arguments")
	}
}

func TestCheckValidatesOpenAPI(t *testing.T) {
	application := Application{
		Name: "test",
		BuildRoutes: func() *kernel.Router {
			type input struct {
				kernel.Meta `route:"GET /ok"`
			}
			router := kernel.NewRouter(nil, "test", "1")
			router.Handle(func(context.Context, input) (map[string]bool, error) {
				return map[string]bool{"ok": true}, nil
			})
			return router
		},
	}
	if err := Run(context.Background(), application, []string{"check"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckReportsRouteValidation(t *testing.T) {
	application := Application{
		Name: "test",
		BuildRoutes: func() *kernel.Router {
			router := kernel.NewRouter(nil, "test", "1")
			router.Raw(http.MethodGet, "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			return router
		},
	}
	err := Run(context.Background(), application, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), "visible raw route") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckValidatesEnvConfig(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,requird"`
	}
	application := Application{
		Name:   "test",
		Config: &config{},
		BuildRoutes: func() *kernel.Router {
			return kernel.NewRouter(nil, "test", "1")
		},
	}
	err := Run(context.Background(), application, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), `unknown env option "requird"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRejectsNestedDuplicateEnvName(t *testing.T) {
	type jobConfig struct {
		RetryBase time.Duration `env:"RETRY_BASE,default=1s"`
	}
	type config struct {
		RetryBase time.Duration `env:"JOB_RETRY_BASE,default=1s"`
		Job       jobConfig     `env:""`
	}
	application := Application{
		Name:      "test",
		Config:    &config{},
		EnvPrefix: "CHECK",
		BuildRoutes: func() *kernel.Router {
			return kernel.NewRouter(nil, "test", "1")
		},
	}
	err := Run(context.Background(), application, []string{"check"})
	if err == nil || !strings.Contains(err.Error(), "duplicates env CHECK_JOB_RETRY_BASE") {
		t.Fatalf("error = %v", err)
	}
}

func TestDuplicateEnvName(t *testing.T) {
	type duplicated struct {
		First  string `env:"VALUE"`
		Second string `env:"VALUE"`
	}
	_, err := EnvTemplate(duplicated{})
	if err == nil || !strings.Contains(err.Error(), "duplicates env VALUE") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsUnknownOption(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,requird"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), `unknown env option "requird"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsPrefixOnLeaf(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,prefix=APP_"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "prefix= is only supported at the config root") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsNameAndPrefix(t *testing.T) {
	type nested struct {
		Value string `env:"VALUE"`
	}
	type config struct {
		Nested nested `env:"NESTED,prefix=APP_"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "prefix= is only supported at the config root") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateKeepsExplicitEmptyDefault(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,default="`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "VALUE=\n") || strings.Contains(string(data), "# VALUE=\n") {
		t.Fatalf("template = %q", data)
	}
}

func TestEnvTemplateRejectsRequiredEmptyDefault(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,required,default="`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "required and default cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsOptionsAfterDefault(t *testing.T) {
	for _, tag := range []string{"VALUE,default=1,required", "VALUE,default=1,prefix=APP_", "VALUE,default=1,default=2"} {
		t.Run(tag, func(t *testing.T) {
			_, _, _, _, _, err := parseEnvTag(tag)
			if err == nil || !strings.Contains(err.Error(), "default must be the last env option") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnvTemplateRejectsNestedStructRequiredOrDefault(t *testing.T) {
	type nested struct {
		Value string `env:"VALUE"`
	}
	type requiredConfig struct {
		Nested nested `env:",required"`
	}
	type defaultConfig struct {
		Nested nested `env:",default=1"`
	}
	for name, config := range map[string]any{
		"required": requiredConfig{},
		"default":  defaultConfig{},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := EnvTemplate(config)
			if err == nil || !strings.Contains(err.Error(), "nested struct cannot declare required or default") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnvTemplateRejectsInvalidKey(t *testing.T) {
	type config struct {
		Value string `env:"BAD-NAME"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "not a valid shell variable name") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsNewlineDefault(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,default=line1\nline2"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "must not contain a newline") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsNULDefault(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,default=before\x00after"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "must not contain NUL") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateQuotesShellValues(t *testing.T) {
	type config struct {
		Spaced string `env:"SPACED,default=hello world"`
		Dollar string `env:"DOLLAR,default=$HOME"`
		Quoted string `env:"QUOTED,default=it's fine"`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"SPACED='hello world'\n",
		"DOLLAR='$HOME'\n",
		"QUOTED='it'\\''s fine'\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestEnvTemplateRejectsRecursiveStruct(t *testing.T) {
	type recursive struct {
		Next *recursive
	}
	_, err := EnvTemplate(recursive{})
	if err == nil || !strings.Contains(err.Error(), "recursive struct type") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsRecursivePointer(t *testing.T) {
	type recursivePointer *recursivePointer
	type config struct {
		Value recursivePointer `env:"VALUE"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "recursive pointer type") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsTaggedPrivateField(t *testing.T) {
	type config struct {
		value string `env:"VALUE"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "env tag on unexported field") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateRejectsIgnoredFieldOptions(t *testing.T) {
	type config struct {
		Value string `env:"-,required"`
	}
	_, err := EnvTemplate(config{})
	if err == nil || !strings.Contains(err.Error(), "ignored env field cannot have options") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateAllowsNamedNestedStruct(t *testing.T) {
	type nested struct {
		Value string `env:"VALUE"`
	}
	type config struct {
		Nested nested `env:"NESTED"`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "NESTED_VALUE=\n") {
		t.Fatalf("template = %q", data)
	}
}

func TestEnvTemplateAllowsNamedDecoderStruct(t *testing.T) {
	type config struct {
		Value testEnvDecoder `env:"VALUE"`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# VALUE=\n") {
		t.Fatalf("template = %q", data)
	}
}

func TestEnvTemplatePreservesDefaultTrailingSpace(t *testing.T) {
	type config struct {
		Value string `env:"VALUE,default=value "`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "VALUE='value '\n") {
		t.Fatalf("template = %q", data)
	}
}

func TestEnvTemplateRejectsInvalidRootPrefix(t *testing.T) {
	_, err := EnvTemplate(struct{}{}, WithEnvPrefix("BAD-"))
	if err == nil || !strings.Contains(err.Error(), "not a valid shell variable prefix") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvTemplateAllowsOptionsWithoutName(t *testing.T) {
	type config struct {
		Value string `env:",default=derived"`
	}
	data, err := EnvTemplate(config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "VALUE=derived\n") {
		t.Fatalf("template = %q", data)
	}
}

func TestEnvTemplateRejectsEmptyOption(t *testing.T) {
	for _, tag := range []string{"VALUE,", "VALUE,,required"} {
		t.Run(tag, func(t *testing.T) {
			_, _, _, _, _, err := parseEnvTag(tag)
			if err == nil || !strings.Contains(err.Error(), "empty env option") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnvTemplateRejectsDuplicateRequired(t *testing.T) {
	for _, tag := range []string{"VALUE,required,required", "VALUE,required,REQUIRED"} {
		t.Run(tag, func(t *testing.T) {
			_, _, _, _, _, err := parseEnvTag(tag)
			if err == nil || !strings.Contains(err.Error(), "duplicate required option") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
