// Package pgtest contains integration-test helpers for optional Postgres
// components. It is internal to the framework and is not part of the SDK.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// RequireEnv returns a trimmed environment variable or skips the test.
func RequireEnv(t testing.TB, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Skipf("%s is not set", name)
	}
	return value
}

// CreateSchema creates an isolated schema and returns a URL whose search_path
// points at it. The cleanup function drops the schema.
func CreateSchema(t testing.TB, ctx context.Context, rawURL, prefix string) (string, func()) {
	t.Helper()
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	admin, err := sql.Open("pgx", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	quoted := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	cleanup := func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		_ = admin.Close()
	}
	return databaseURLWithSearchPath(t, rawURL, schema), cleanup
}

func databaseURLWithSearchPath(t testing.TB, rawURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
