package pgdb

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

const uniqueViolationCode = "23505"

// IsUniqueViolation reports whether err wraps a PostgreSQL unique constraint
// violation. Applications typically map this to their own conflict error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
