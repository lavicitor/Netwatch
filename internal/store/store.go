// Package store wraps Postgres persistence for Netwatch. It is entirely
// optional at runtime: Open is only ever called when the caller has
// already checked config.DBConfig.Configured(), so a non-nil error here
// always means "configured, but the connection or migration failed" --
// see cmd/netwatch/main.go for how that's logged and handled.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, used by goose only
	"github.com/pressly/goose/v3"

	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies any pending migrations.
func Open(ctx context.Context, cfg config.DBConfig) (*Store, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name,
	)

	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(connectCtx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if err := runMigrations(dsn); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{pool: pool}, nil
}

func runMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// TODO: add the real read/write methods once the scanner has concrete
// results to persist, e.g.:
//   UpsertHost(ctx, model.Host) error
//   RecordScan(ctx, model.ScanResult) error
//   RecentEvents(ctx, limit int) ([]Event, error)
// This is a good place to implement "new device / port changed since last
// scan" diffing.
