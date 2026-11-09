// Package postgres provides the optional pgx PostgreSQL lifecycle component.
// Applications that do not import this package do not compile pgx support from
// Plinth.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	mu      sync.Mutex
	pool    *pgxpool.Pool
	started bool
	closed  bool
}

// New creates a Postgres lifecycle component from a standard libpq URL.
func New(ctx context.Context, databaseURL string) (*Postgres, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL must not be empty")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Name() string { return "postgres" }

func (p *Postgres) Start(ctx context.Context) error {
	if p == nil {
		return errors.New("postgres component is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pool == nil {
		return errors.New("postgres pool is nil")
	}
	if p.closed {
		return errors.New("postgres component already stopped")
	}
	if p.started {
		return errors.New("postgres component already started")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.pool.Ping(pingCtx); err != nil {
		p.pool.Close()
		p.closed = true
		return fmt.Errorf("ping postgres: %w", err)
	}
	p.started = true
	return nil
}

func (p *Postgres) Stop(context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.pool == nil {
		return nil
	}
	p.pool.Close()
	return nil
}

func (p *Postgres) Pool() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.pool
}

func (p *Postgres) Ping(ctx context.Context) error {
	if p == nil {
		return errors.New("postgres component is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pool == nil {
		return errors.New("postgres pool is nil")
	}
	if p.closed {
		return errors.New("postgres component is stopped")
	}
	return p.pool.Ping(ctx)
}
