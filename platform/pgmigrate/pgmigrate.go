// Package pgmigrate provides the pgx-backed convenience constructor for
// database migrations.
package pgmigrate

import (
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/plinth-go/plinth/platform/migrate"
	"github.com/pressly/goose/v3"
)

// IdentifierLimit is the maximum identifier length supported by PostgreSQL.
const IdentifierLimit = 63

// New creates a migration component for PostgreSQL. Migration sources are
// applied in order, each with an independent Goose version table.
func New(url string, enabled bool, logger *slog.Logger, sources ...migrate.Migration) *migrate.Migrator {
	return migrate.NewWithConfig(migrate.Config{
		URL:             url,
		Enabled:         enabled,
		Logger:          logger,
		Driver:          "pgx",
		Dialect:         goose.DialectPostgres,
		IdentifierLimit: IdentifierLimit,
	}, sources...)
}
