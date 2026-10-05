package store

import (
	"context"
	"embed"
	"fmt"
	"path"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// schema is the PostgreSQL schema this service owns. Every table lives there
// instead of public, which deployments commonly restrict, and the pool's
// search_path points at it so the unqualified names in the store queries and
// migrations resolve into it.
const schema = "smartclass_webcam_server"

// ownedTables lists every table the service has ever created. ensureSchema
// moves them out of public when adopting a database written by a version that
// still used the default schema.
var ownedTables = []string{"schema_migrations", "devices", "streams", "stream_segments", "photos", "cameras"}

// NewPool opens a pgx connection pool for dbURL and verifies that the database
// is reachable. Every connection resolves unqualified names through the service
// schema.
func NewPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// ensureSchema creates the service schema and adopts the tables a database
// written by an older version still holds in public. Both steps are
// idempotent: the schema is created only when missing, and tables move only
// into a schema that is still empty, so a database already on the service
// schema is left alone.
func ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf(
			"create schema %s (the database role needs CREATE on the database, or a DBA can pre-create it with CREATE SCHEMA %s AUTHORIZATION <role>): %w",
			schema, schema, err)
	}

	var empty bool
	if err := pool.QueryRow(ctx,
		`SELECT NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = $1)`, schema,
	).Scan(&empty); err != nil {
		return fmt.Errorf("inspect schema %s: %w", schema, err)
	}
	if !empty {
		return nil
	}

	rows, err := pool.Query(ctx,
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename = ANY($1::text[])`,
		ownedTables)
	if err != nil {
		return fmt.Errorf("list tables left in public: %w", err)
	}
	legacy, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list tables left in public: %w", err)
	}
	for _, table := range legacy {
		// schema_migrations moves along with the rest, so migrations already
		// applied under public are not re-run.
		if _, err := pool.Exec(ctx, fmt.Sprintf("ALTER TABLE public.%s SET SCHEMA %s",
			pgx.Identifier{table}.Sanitize(), pgx.Identifier{schema}.Sanitize())); err != nil {
			return fmt.Errorf("move table %s into schema %s: %w", table, schema, err)
		}
	}
	return nil
}

// RunMigrations creates the service schema, adopts tables a previous version
// left in public, and applies every embedded migration that has not been
// applied yet. Each file runs in its own transaction and is recorded in
// schema_migrations, so calling RunMigrations more than once is safe.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if err := ensureSchema(ctx, pool); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		name := entry.Name()

		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		script, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		// With no arguments pgx uses the simple protocol, which allows the
		// multiple statements held by a single migration file.
		if _, err := tx.Exec(ctx, string(script)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
