package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsInvalidArgumentsBeforeOpeningDatabase(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing URL", args: []string{"-source", "app=."}, want: "database URL is required"},
		{name: "missing source", args: []string{"-database-url", "postgres://localhost/db"}, want: "at least one -source"},
		{name: "bad source syntax", args: []string{"-database-url", "postgres://localhost/db", "-source", "app"}, want: "name=directory"},
		{name: "duplicate source", args: []string{"-database-url", "postgres://localhost/db", "-source", "app=.", "-source", "app=."}, want: `source "app" is repeated`},
		{name: "positional", args: []string{"-database-url", "postgres://localhost/db", "-source", "app=.", "extra"}, want: "unexpected arguments"},
		{name: "non-positive timeout", args: []string{"-database-url", "postgres://localhost/db", "-source", "app=.", "-timeout", "0s"}, want: "timeout must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsMissingSourceDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-database-url", "postgres://localhost/db",
		"-source", "app=" + filepath.Join(t.TempDir(), "missing"),
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "stat migration source") {
		t.Fatalf("error = %v", err)
	}
}

func TestSourceFlagsCanonicalizeWhitespace(t *testing.T) {
	var sources sourceFlags
	if err := sources.Set(" app = ./migrations "); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].name != "app" || sources[0].directory != "./migrations" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestRunHelpWritesUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-h"}, &stdout, &stderr)
	if err != flag.ErrHelp {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "Usage: plinth-migrate") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSourceDirectoryCanBeRelative(t *testing.T) {
	root := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Mkdir("migrations", 0o755); err != nil {
		t.Fatal(err)
	}
	var sources sourceFlags
	if err := sources.Set("app=migrations"); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(sources[0].directory)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("absolute = %q, want %q", got, want)
	}
}
