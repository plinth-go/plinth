// Package migrate provides optional Goose database migrations with independent
// version tables per source. The package is database-agnostic: callers select
// a database/sql driver and the matching Goose dialect through Config.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/plinth-go/plinth/utils"
	"github.com/pressly/goose/v3"
)

// Migration is one independently versioned migration source. Each source gets
// its own goose version table, so application migrations never prevent a later
// framework migration from being applied.
type Migration struct {
	Name string
	FS   fs.FS
}

// Config selects a database/sql driver and Goose dialect for migrations.
// Applications must import the database/sql driver they name in Driver. The
// optional per-database adapter packages register common drivers.
type Config struct {
	URL             string
	Enabled         bool
	Logger          *slog.Logger
	Driver          string
	Dialect         goose.Dialect
	IdentifierLimit int
}

// Migrator is a single-use component that validates every source provider
// before applying any migration source.
type Migrator struct {
	url        string
	enabled    bool
	logger     *slog.Logger
	driver     string
	dialect    goose.Dialect
	limit      int
	migrations []Migration

	mu      sync.Mutex
	started bool
	closed  bool
}

// NewWithConfig creates a migration component for an explicit database/sql
// driver and Goose dialect. An IdentifierLimit of zero disables the identifier
// length preflight for databases that do not impose one.
func NewWithConfig(cfg Config, sources ...Migration) *Migrator {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Migrator{
		url:        strings.TrimSpace(cfg.URL),
		enabled:    cfg.Enabled,
		logger:     cfg.Logger,
		driver:     strings.TrimSpace(cfg.Driver),
		dialect:    cfg.Dialect,
		limit:      cfg.IdentifierLimit,
		migrations: append([]Migration(nil), sources...),
	}
}

func (m *Migrator) Name() string { return "migrator" }

func (m *Migrator) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("migrator component is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.logger == nil {
		m.logger = slog.Default()
	}
	if m.closed {
		return errors.New("migrator component already stopped")
	}
	if m.started {
		return errors.New("migrator component already started")
	}
	if err := m.apply(ctx); err != nil {
		return err
	}
	m.started = true
	return nil
}

func (m *Migrator) apply(ctx context.Context) error {
	if !m.enabled {
		m.logger.Info("automatic migrations disabled")
		return nil
	}
	if len(m.migrations) == 0 {
		return fmt.Errorf("no migration sources configured")
	}
	if m.driver == "" {
		return fmt.Errorf("migration driver must not be empty")
	}
	if m.dialect == "" {
		return fmt.Errorf("migration dialect must not be empty")
	}
	if m.limit < 0 {
		return fmt.Errorf("migration identifier limit must not be negative")
	}

	type preparedSource struct {
		name string
		fs   fs.FS
	}
	prepared := make([]preparedSource, 0, len(m.migrations))
	seen := map[string]bool{}
	for _, source := range m.migrations {
		name := strings.TrimSpace(source.Name)
		if name == "" || isNilFS(source.FS) {
			return fmt.Errorf("migration source requires name and filesystem")
		}
		if err := validateMigrationSourceName(name); err != nil {
			return err
		}
		table := migrationTableName(name)
		if m.limit > 0 && len(table) > m.limit {
			return fmt.Errorf(
				"migration source name %q produces table %q with %d bytes; database identifiers are limited to %d bytes",
				name, table, len(table), m.limit,
			)
		}
		if seen[table] {
			return fmt.Errorf("duplicate migration source name %q (table %q)", name, table)
		}
		seen[table] = true
		prepared = append(prepared, preparedSource{name: name, fs: source.FS})
	}
	if m.url == "" {
		return fmt.Errorf("migration database URL must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	db, err := sql.Open(m.driver, m.url)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer db.Close()

	type readySource struct {
		name     string
		table    string
		provider *goose.Provider
	}
	ready := make([]readySource, 0, len(prepared))
	for _, source := range prepared {
		table := migrationTableName(source.name)
		provider, err := goose.NewProvider(
			m.dialect,
			db,
			source.fs,
			goose.WithTableName(table),
			goose.WithDisableGlobalRegistry(true),
			goose.WithSlog(m.logger),
		)
		if err != nil {
			return fmt.Errorf("create migration provider %q: %w", source.name, err)
		}
		ready = append(ready, readySource{name: source.name, table: table, provider: provider})
	}

	completed := make([]string, 0, len(ready))
	for _, source := range ready {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf(
				"apply migrations before source %q (completed sources: %s): %w",
				source.name, strings.Join(completed, ", "), err,
			)
		}
		results, err := source.provider.Up(ctx)
		if err != nil {
			return migrationApplyError(source.name, source.table, completed, err)
		}
		completed = append(completed, source.name)
		m.logger.Info("migrations applied", "source", source.name, "table", source.table, "count", len(results))
	}
	return nil
}

// migrationApplyError makes partial application explicit. Goose applies each
// version independently; earlier sources and earlier versions in the failing
// source may therefore remain applied when startup aborts. A NO TRANSACTION
// migration can also leave the failing version partially applied.
func migrationApplyError(source, table string, completed []string, err error) error {
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		message := fmt.Sprintf(
			"run migrations %q (table %s): failed at version %d after %d migration(s) completed in this source",
			source, table, partial.Failed.Source.Version, len(partial.Applied),
		)
		if len(completed) > 0 {
			message += fmt.Sprintf("; earlier sources already completed: %s", strings.Join(completed, ", "))
		}
		return fmt.Errorf("%s: %w", message, err)
	}
	if len(completed) > 0 {
		return fmt.Errorf(
			"run migrations %q (table %s) after sources %s completed: %w",
			source, table, strings.Join(completed, ", "), err,
		)
	}
	return fmt.Errorf("run migrations %q: %w", source, err)
}

const maxMigrationSourceNameBytes = 128

func validateMigrationSourceName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("migration source name must be valid UTF-8")
	}
	if len(name) > maxMigrationSourceNameBytes {
		return fmt.Errorf("migration source name must not exceed %d bytes", maxMigrationSourceNameBytes)
	}
	for _, char := range name {
		if !unicode.IsPrint(char) {
			return fmt.Errorf("migration source name must contain only printable characters")
		}
	}
	return nil
}

func isNilFS(files fs.FS) bool {
	if files == nil {
		return true
	}
	value := reflect.ValueOf(files)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (m *Migrator) Stop(context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return nil
}

func migrationTableName(name string) string {
	var table strings.Builder
	for _, char := range strings.ToLower(name) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
			table.WriteRune(char)
		default:
			table.WriteByte('_')
		}
	}
	normalized := utils.IfEmptyElse(strings.Trim(table.String(), "_"), "app")
	return "goose_version_" + normalized
}
