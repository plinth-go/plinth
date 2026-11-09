package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/plinth-go/plinth/platform/jobs"
)

func TestSystemEchoJobRegistration(t *testing.T) {
	registry := jobs.NewRegistry()
	registerSystemJobs(registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler, ok := registry.Handler("system.echo")
	if !ok {
		t.Fatal("system.echo is not registered")
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":"hello"}`)); err != nil {
		t.Fatal(err)
	}
}
