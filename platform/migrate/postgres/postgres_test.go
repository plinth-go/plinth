package postgres

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plinth-go/plinth/platform/migrate"
)

func TestNewConfiguresPostgresBackend(t *testing.T) {
	name := "app_" + strings.Repeat("x", IdentifierLimit)
	err := New("://invalid", true, nil,
		migrate.Migration{Name: name, FS: fstest.MapFS{}},
	).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database identifiers are limited to 63 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewSupportsDisabledMigrations(t *testing.T) {
	if err := New("", false, nil).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
