package pgdb

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolation(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505"}
	if !IsUniqueViolation(unique) {
		t.Fatal("direct unique violation was not detected")
	}
	if !IsUniqueViolation(fmt.Errorf("insert: %w", unique)) {
		t.Fatal("wrapped unique violation was not detected")
	}
	if IsUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("foreign key violation was treated as unique")
	}
	if IsUniqueViolation(errors.New("other")) || IsUniqueViolation(nil) {
		t.Fatal("non-Postgres error was treated as unique")
	}
}
