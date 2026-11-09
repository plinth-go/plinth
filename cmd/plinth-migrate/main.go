// Command plinth-migrate applies one or more Goose migration sources without
// starting the application. Each source keeps an independent version table.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/plinth-go/plinth/platform/migrate"
	"github.com/plinth-go/plinth/platform/pgmigrate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "plinth-migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("plinth-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection URL (default: DATABASE_URL)")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall migration deadline")
	var sources sourceFlags
	flags.Var(&sources, "source", "migration source as name=directory; repeat for multiple sources")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: plinth-migrate -source name=directory [-source ...]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*databaseURL) == "" {
		return errors.New("database URL is required through -database-url or DATABASE_URL")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if len(sources) == 0 {
		return errors.New("at least one -source name=directory is required")
	}
	migrations := make([]migrate.Migration, 0, len(sources))
	for _, source := range sources {
		absolute, err := filepath.Abs(source.directory)
		if err != nil {
			return fmt.Errorf("resolve migration source %q: %w", source.name, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return fmt.Errorf("stat migration source %q: %w", source.name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("migration source %q is not a directory: %s", source.name, absolute)
		}
		migrations = append(migrations, migrate.Migration{Name: source.name, FS: os.DirFS(absolute)})
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	migrator := pgmigrate.New(strings.TrimSpace(*databaseURL), true, logger, migrations...)
	if err := migrator.Start(ctx); err != nil {
		return err
	}
	if err := migrator.Stop(ctx); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied %d migration source(s)\n", len(migrations))
	return nil
}

type migrationSource struct {
	name      string
	directory string
}

type sourceFlags []migrationSource

func (s *sourceFlags) String() string {
	values := make([]string, 0, len(*s))
	for _, source := range *s {
		values = append(values, source.name+"="+source.directory)
	}
	return strings.Join(values, ",")
}

func (s *sourceFlags) Set(value string) error {
	name, directory, ok := strings.Cut(value, "=")
	name = strings.TrimSpace(name)
	directory = strings.TrimSpace(directory)
	if !ok || name == "" || directory == "" {
		return fmt.Errorf("source must use name=directory syntax")
	}
	for _, source := range *s {
		if source.name == name {
			return fmt.Errorf("source %q is repeated", name)
		}
	}
	*s = append(*s, migrationSource{name: name, directory: directory})
	return nil
}
