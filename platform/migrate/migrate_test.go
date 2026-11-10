package migrate

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/plinth-go/plinth/platform/internal/pgtest"
	"github.com/pressly/goose/v3"
)

type typedNilFS struct{}

func (*typedNilFS) Open(string) (fs.File, error) { panic("typed nil filesystem was used") }

const testIdentifierLimit = 63

func newTestMigrator(url string, enabled bool, logger *slog.Logger, sources ...Migration) *Migrator {
	return NewWithConfig(Config{
		URL:             url,
		Enabled:         enabled,
		Logger:          logger,
		Driver:          "pgx",
		Dialect:         goose.DialectPostgres,
		IdentifierLimit: testIdentifierLimit,
	}, sources...)
}

func TestPreflightsAllSourcesBeforeOpeningDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator("://invalid", true, logger,
		Migration{Name: "one", FS: fstest.MapFS{}},
		Migration{Name: "two"},
	)
	err := migrator.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires name and filesystem") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsEmptyURLWhenEnabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator(" \t ", true, logger,
		Migration{Name: "app", FS: fstest.MapFS{}},
	)
	err := migrator.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "migration database URL must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsIncompleteBackendConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := Migration{Name: "app", FS: fstest.MapFS{}}
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "driver",
			cfg: Config{URL: "://invalid", Enabled: true, Logger: logger,
				Dialect: goose.DialectPostgres},
			want: "driver must not be empty",
		},
		{
			name: "dialect",
			cfg: Config{URL: "://invalid", Enabled: true, Logger: logger,
				Driver: "pgx"},
			want: "dialect must not be empty",
		},
		{
			name: "negative identifier limit",
			cfg: Config{URL: "://invalid", Enabled: true, Logger: logger,
				Driver: "pgx", Dialect: goose.DialectPostgres, IdentifierLimit: -1},
			want: "identifier limit must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewWithConfig(tt.cfg, source).Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHonorsCanceledContextBeforeOpeningDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator("postgres://user:pass@127.0.0.1:1/db?sslmode=disable", true, logger,
		Migration{Name: "app", FS: fstest.MapFS{}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := migrator.Start(ctx)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "apply migrations") {
		t.Fatalf("error = %v, want canceled migration context", err)
	}
}

func TestMigrationApplyErrorReportsPartialProgress(t *testing.T) {
	cause := errors.New("statement failed")
	partial := &goose.PartialError{
		Applied: []*goose.MigrationResult{{}, {}},
		Failed: &goose.MigrationResult{Source: &goose.Source{
			Type:    goose.TypeSQL,
			Version: 7,
		}},
		Err: cause,
	}
	err := migrationApplyError("app", "goose_version_app", []string{"plinth"}, partial)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause", err)
	}
	for _, want := range []string{
		`run migrations "app"`,
		"goose_version_app",
		"version 7",
		"2 migration(s) completed",
		"earlier sources already completed: plinth",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestRejectsTypedNilFilesystemBeforeOpeningDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var files *typedNilFS
	migrator := newTestMigrator("://invalid", true, logger,
		Migration{Name: "app", FS: files},
	)
	err := migrator.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires name and filesystem") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsCollidingSourceTablesBeforeOpeningDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator("://invalid", true, logger,
		Migration{Name: "app", FS: fstest.MapFS{}},
		Migration{Name: "App!", FS: fstest.MapFS{}},
	)
	err := migrator.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "duplicate migration source") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsSourceTableOverConfiguredIdentifierLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	name := "app_" + strings.Repeat("x", testIdentifierLimit)
	migrator := newTestMigrator("://invalid", true, logger,
		Migration{Name: name, FS: fstest.MapFS{}},
	)
	err := migrator.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database identifiers are limited to 63 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsInvalidSourceNamesBeforeOpeningDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "invalid UTF-8", source: string([]byte{0xff}), want: "valid UTF-8"},
		{name: "control character", source: "app\nsource", want: "printable"},
		{name: "too long", source: strings.Repeat("x", maxMigrationSourceNameBytes+1), want: "must not exceed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migrator := newTestMigrator("://invalid", true, logger,
				Migration{Name: tt.source, FS: fstest.MapFS{}},
			)
			err := migrator.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMigrationTableNameAtConfiguredIdentifierLimitIsAccepted(t *testing.T) {
	const prefix = "goose_version_"
	name := strings.Repeat("x", testIdentifierLimit-len(prefix))
	table := migrationTableName(name)
	if len(table) != testIdentifierLimit {
		t.Fatalf("table %q has %d bytes, want %d", table, len(table), testIdentifierLimit)
	}
}

func TestLifecycleIsSingleUse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator("", false, logger)
	if err := migrator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Start(context.Background()); err == nil {
		t.Fatal("repeated Start was accepted")
	}
	if err := migrator.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Start(context.Background()); err == nil {
		t.Fatal("Start after Stop was accepted")
	}
}

func TestZeroValueIsUsable(t *testing.T) {
	var migrator Migrator
	if err := migrator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Start(context.Background()); err == nil {
		t.Fatal("repeated Start was accepted")
	}
	if err := migrator.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !migrator.closed {
		t.Fatal("zero-value migrator did not close")
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var migrator *Migrator
	if got := migrator.Name(); got != "migrator" {
		t.Fatalf("Name = %q", got)
	}
	if err := migrator.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Start(context.Background()); err == nil {
		t.Fatal("nil migrator started")
	}
}

func TestStopBeforeStartPreventsStart(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator("", false, logger)
	if err := migrator.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Start(context.Background()); err == nil {
		t.Fatal("Start after Stop was accepted")
	}
}

func TestIntegrationAppliesSource(t *testing.T) {
	databaseURL := pgtest.RequireEnv(t, "PLINTH_TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schemaURL, cleanup := pgtest.CreateSchema(t, ctx, databaseURL, "plinth_platform_test")
	defer cleanup()

	source := fstest.MapFS{
		"00001_platform_test.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE plinth_platform_test (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE plinth_platform_test;
`)},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator(schemaURL, true, logger, Migration{Name: "platform_test", FS: source})
	if err := migrator.Start(ctx); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("pgx", schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM plinth_platform_test`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows = %d", count)
	}

	var versionTable string
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('goose_version_platform_test')::text`).Scan(&versionTable); err != nil {
		t.Fatal(err)
	}
	if versionTable != "goose_version_platform_test" {
		t.Fatalf("version table = %q", versionTable)
	}
}

func TestIntegrationPreflightsProvidersBeforeApplying(t *testing.T) {
	databaseURL := pgtest.RequireEnv(t, "PLINTH_TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schemaURL, cleanup := pgtest.CreateSchema(t, ctx, databaseURL, "plinth_platform_preflight")
	defer cleanup()

	first := fstest.MapFS{
		"00001_first.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE plinth_platform_first (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE plinth_platform_first;
`)},
	}
	invalid := fstest.MapFS{
		"00001_one.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00001_two.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 2;\n")},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator(schemaURL, true, logger,
		Migration{Name: "first", FS: first},
		Migration{Name: "invalid", FS: invalid},
	)
	if err := migrator.Start(ctx); err == nil || !strings.Contains(err.Error(), "create migration provider") {
		t.Fatalf("error = %v", err)
	}

	db, err := sql.Open("pgx", schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('plinth_platform_first') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("first source was applied before every provider passed preflight")
	}
}

func TestIntegrationReportsPartialApplication(t *testing.T) {
	databaseURL := pgtest.RequireEnv(t, "PLINTH_TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schemaURL, cleanup := pgtest.CreateSchema(t, ctx, databaseURL, "plinth_platform_partial")
	defer cleanup()

	source := fstest.MapFS{
		"00001_first.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE plinth_platform_first (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE plinth_platform_first;
`)},
		"00002_second.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE plinth_platform_second (id integer PRIMARY KEY);
SELECT * FROM plinth_platform_missing;

-- +goose Down
DROP TABLE plinth_platform_second;
`)},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrator := newTestMigrator(schemaURL, true, logger, Migration{Name: "partial", FS: source})
	err := migrator.Start(ctx)
	if err == nil {
		t.Fatal("partial migration succeeded")
	}
	for _, want := range []string{
		`run migrations "partial"`,
		"failed at version 2",
		"1 migration(s) completed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}

	db, err := sql.Open("pgx", schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var firstExists, secondExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass('plinth_platform_first') IS NOT NULL,
		       to_regclass('plinth_platform_second') IS NOT NULL
	`).Scan(&firstExists, &secondExists); err != nil {
		t.Fatal(err)
	}
	if !firstExists || secondExists {
		t.Fatalf("first exists=%v second exists=%v; want completed first, rolled-back second", firstExists, secondExists)
	}
}
