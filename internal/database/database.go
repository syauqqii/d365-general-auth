package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"

	"general-auth/internal/config"
)

// DB wraps sqlx with the dialect it was opened with. Queries are written with
// "?" placeholders and passed through Rebind for the active driver.
type DB struct {
	*sqlx.DB
	Dialect *Dialect
}

func Open(ctx context.Context, cfg config.Database, resolve func(string) string) (*DB, error) {
	d, err := Lookup(cfg.Driver)
	if err != nil {
		return nil, err
	}
	dsn := cfg.DSN
	if dsn == "" {
		if dsn, err = d.buildDSN(cfg, nonEmpty(cfg.Params[d.Name]), resolve); err != nil {
			return nil, err
		}
	}

	conn, err := sqlx.Open(d.Driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.Name, err)
	}
	conn.SetMaxOpenConns(cfg.MaxOpenConns)
	conn.SetMaxIdleConns(cfg.MaxIdleConns)
	conn.SetConnMaxLifetime(cfg.ConnMaxLifetime.Duration)
	conn.SetConnMaxIdleTime(cfg.ConnMaxIdleTime.Duration)
	if d.Name == "sqlite" {
		// SQLite allows a single writer; serialising avoids SQLITE_BUSY. The
		// connection is kept open so a :memory: database is not lost.
		conn.SetMaxOpenConns(1)
		conn.SetMaxIdleConns(1)
		conn.SetConnMaxLifetime(0)
		conn.SetConnMaxIdleTime(0)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect %s: %w", d.Name, err)
	}
	return &DB{DB: conn, Dialect: d}, nil
}

// Migrate applies every migration version not yet recorded in
// schema_migrations, each in its own transaction (MySQL commits DDL
// implicitly, so there a failed version may be partly applied).
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, db.Dialect.migrationsTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var applied []int
	if err := db.SelectContext(ctx, &applied, "SELECT version FROM schema_migrations"); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	done := make(map[int]bool, len(applied))
	for _, v := range applied {
		done[v] = true
	}

	for i, stmts := range db.Dialect.migrations {
		version := i + 1
		if done[version] {
			continue
		}
		tx, err := db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w\n%s", version, err, stmt)
			}
		}
		if _, err := tx.ExecContext(ctx, db.Rebind("INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)"), version, Now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: record version: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
		slog.Info("migration applied", "version", version, "db", db.Dialect.Name)
	}
	return nil
}

// Paginate returns the LIMIT/OFFSET clause for the active dialect. The
// query must already have an ORDER BY.
func (db *DB) Paginate(limit, offset int) string { return db.Dialect.paginate(limit, offset) }

// Now returns the current UTC time truncated to what every supported
// database can store.
func Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// nonEmpty drops parameters whose value is empty, so "${VAR:-}" in the
// config leaves the driver default in place.
func nonEmpty(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}
