package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunCheckDetectsStaleGeneratedDocs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "demo.go"), "package demo\n\n// Hello returns a greeting.\nfunc Hello() {}\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	if err := run(root, out, true); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing output error = %v", err)
	}
	if err := run(root, out, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, out, true); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, out, "package generated\n")
	if err := run(root, out, true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale output error = %v", err)
	}
}

func TestRunAtomicallyReplacesOutput(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "demo.go"), "package demo\n\n// Hello returns a greeting.\nfunc Hello() {}\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	if err := run(root, out, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(root, out, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && os.SameFile(before, after) {
		t.Fatal("generated file was rewritten in place instead of atomically replaced")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", after.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "docs_gen.go" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestRunSkipsNestedModules(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/root\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "root.go"), "package root\n\n// Root returns a value.\nfunc Root() {}\n")
	nested := filepath.Join(root, "tools")
	writeTestFile(t, filepath.Join(nested, "go.mod"), "module example.com/tools\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(nested, "tools.go"), "package tools\n\n// Nested must not appear.\nfunc Nested() {}\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	if err := run(root, out, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "example.com/root.Root") {
		t.Fatalf("root operation missing from generated docs:\n%s", data)
	}
	if strings.Contains(string(data), "example.com/tools.Nested") || strings.Contains(string(data), "example.com/root/tools.Nested") {
		t.Fatalf("nested module leaked into generated docs:\n%s", data)
	}
}

func TestRunIncludeFileFiltersPackages(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "public", "public.go"), "package public\n\n// Public returns a value.\nfunc Public() {}\n")
	writeTestFile(t, filepath.Join(root, "public", "sub", "sub.go"), "package sub\n\n// Sub returns a value.\nfunc Sub() {}\n")
	writeTestFile(t, filepath.Join(root, "internal", "internal.go"), "package internal\n\n// Internal must not appear.\nfunc Internal() {}\n")
	writeTestFile(t, filepath.Join(root, "docgen-packages.txt"), "# packages used by the reference application\n\npublic\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	var stdout, stderr bytes.Buffer
	code := cli([]string{
		"-root", root,
		"-out", out,
		"-include-file", "docgen-packages.txt",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"example.com/demo/public.Public", "example.com/demo/public/sub.Sub"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("included operation %s missing from generated docs:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "example.com/demo/internal.Internal") {
		t.Fatalf("excluded package leaked into generated docs:\n%s", data)
	}
}

func TestRunIncludeFileRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "demo.go"), "package demo\n\n// Hello returns a greeting.\nfunc Hello() {}\n")
	writeTestFile(t, filepath.Join(root, "docgen-packages.txt"), "../outside\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	err := runWithOutput(root, out, "docgen-packages.txt", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "must be module-relative") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadModulePathIgnoresCommentsAndUnquotes(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "comment", content: "module example.com/demo // generated module\n\ngo 1.27.3\n", want: "example.com/demo"},
		{name: "quoted", content: "module \"example.com/demo\"\n\ngo 1.27.3\n", want: "example.com/demo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "go.mod")
			writeTestFile(t, path, test.content)
			got, err := readModulePath(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("module path = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCLIRejectsUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unexpected arguments: unexpected") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCLIHelpSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestCLIWritesStatusToInjectedStdout(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.3\n")
	writeTestFile(t, filepath.Join(root, "demo.go"), "package demo\n\n// Hello returns a greeting.\nfunc Hello() {}\n")
	out := filepath.Join(t.TempDir(), "docs_gen.go")

	var stdout, stderr bytes.Buffer
	if code := cli([]string{"-root", root, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 operations, 0 fields") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
