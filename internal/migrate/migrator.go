package migrate

import (
	"context"
	"embed"
	"fmt"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/contextos/contextos/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var migrationFS embed.FS

const advisoryLockKey = 42424242

const createSchemaMigrationsSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INT PRIMARY KEY,
    name VARCHAR(256) NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

// MigrationRecord represents a single applied migration.
type MigrationRecord struct {
	Version   int       `json:"version"`
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}

// migration holds a parsed migration file pair.
type migration struct {
	Version int
	Name    string
	UpSQL   string
	DownSQL string
}

// Migrator manages database schema migrations.
type Migrator struct {
	db     *pgxpool.Pool
	config *config.MigrateConfig
}

// NewMigrator creates a new Migrator instance.
func NewMigrator(db *pgxpool.Pool, cfg *config.MigrateConfig) *Migrator {
	return &Migrator{db: db, config: cfg}
}

// parseMigrations reads and parses all embedded SQL migration files.
func parseMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("sql")
	if err != nil {
		return nil, fmt.Errorf("reading migration directory: %w", err)
	}

	upFiles := make(map[int]string)
	downFiles := make(map[int]string)
	names := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fname := entry.Name()

		// Parse naming convention: {version}_{name}.up.sql or {version}_{name}.down.sql
		var isUp bool
		var baseName string
		if strings.HasSuffix(fname, ".up.sql") {
			isUp = true
			baseName = strings.TrimSuffix(fname, ".up.sql")
		} else if strings.HasSuffix(fname, ".down.sql") {
			isUp = false
			baseName = strings.TrimSuffix(fname, ".down.sql")
		} else {
			continue
		}

		parts := strings.SplitN(baseName, "_", 2)
		if len(parts) != 2 {
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		content, err := migrationFS.ReadFile(path.Join("sql", fname))
		if err != nil {
			return nil, fmt.Errorf("reading migration file %s: %w", fname, err)
		}

		if isUp {
			upFiles[version] = string(content)
			names[version] = parts[1]
		} else {
			downFiles[version] = string(content)
		}
	}

	var migrations []migration
	for version, upSQL := range upFiles {
		m := migration{
			Version: version,
			Name:    names[version],
			UpSQL:   upSQL,
			DownSQL: downFiles[version],
		}
		migrations = append(migrations, m)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// Run executes all pending migrations in order.
func (m *Migrator) Run(ctx context.Context) error {
	conn, err := m.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	// Acquire advisory lock to prevent concurrent migrations.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquiring advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if err := ensureTargetSchemas(ctx, conn); err != nil {
		return err
	}

	// Create schema_migrations table if not exists.
	if _, err := conn.Exec(ctx, createSchemaMigrationsSQL); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Query already-applied migration versions.
	applied, err := m.getAppliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	// Parse all embedded migrations.
	migrations, err := parseMigrations()
	if err != nil {
		return err
	}

	// Execute each unapplied migration in order.
	for _, mig := range migrations {
		if applied[mig.Version] {
			continue
		}

		if err := m.applyMigration(ctx, conn, mig); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration executes a single migration and records it.
func (m *Migrator) applyMigration(ctx context.Context, conn *pgxpool.Conn, mig migration) error {
	// Handle pgvector extension creation gracefully.
	if strings.Contains(mig.UpSQL, "CREATE EXTENSION") && strings.Contains(mig.UpSQL, "vector") {
		if err := m.applyVectorMigration(ctx, conn, mig); err != nil {
			return err
		}
		return nil
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction for migration %d: %w", mig.Version, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, mig.UpSQL); err != nil {
		return fmt.Errorf("executing migration %d (%s): %w", mig.Version, mig.Name, err)
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		mig.Version, mig.Name,
	); err != nil {
		return fmt.Errorf("recording migration %d: %w", mig.Version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing migration %d: %w", mig.Version, err)
	}

	return nil
}

// applyVectorMigration handles the vector extension migration gracefully.
// If the extension creation fails, it logs a warning and applies the remaining
// SQL statements (table creation) without the extension.
func (m *Migrator) applyVectorMigration(ctx context.Context, conn *pgxpool.Conn, mig migration) error {
	// Try to create the extension first, outside the main transaction.
	_, extErr := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	if extErr != nil {
		log.Printf("WARNING: failed to create pgvector extension (may require superuser): %v", extErr)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction for migration %d: %w", mig.Version, err)
	}
	defer tx.Rollback(ctx)

	// Execute the remaining SQL (table creation) regardless of extension status.
	// Strip the CREATE EXTENSION statement and execute the rest.
	remainingSQL := stripExtensionStatement(mig.UpSQL)
	if strings.TrimSpace(remainingSQL) != "" {
		if _, err := tx.Exec(ctx, remainingSQL); err != nil {
			return fmt.Errorf("executing migration %d (%s) tables: %w", mig.Version, mig.Name, err)
		}
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		mig.Version, mig.Name,
	); err != nil {
		return fmt.Errorf("recording migration %d: %w", mig.Version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing migration %d: %w", mig.Version, err)
	}

	return nil
}

// stripExtensionStatement removes CREATE EXTENSION lines from SQL.
func stripExtensionStatement(sql string) string {
	var lines []string
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(strings.ToUpper(line))
		if strings.HasPrefix(trimmed, "CREATE EXTENSION") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func ensureTargetSchemas(ctx context.Context, conn *pgxpool.Conn) error {
	var searchPath string
	if err := conn.QueryRow(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
		return fmt.Errorf("reading search_path: %w", err)
	}

	for _, schema := range parseSearchPath(searchPath) {
		if !shouldEnsureSchema(schema) {
			continue
		}
		sql := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("creating schema %q: %w", schema, err)
		}
	}
	return nil
}

func parseSearchPath(searchPath string) []string {
	parts := strings.Split(searchPath, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		name = strings.Trim(name, `"`)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func shouldEnsureSchema(schema string) bool {
	switch schema {
	case "", "$user", "public":
		return false
	}
	return !strings.HasPrefix(schema, "pg_")
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// getAppliedVersions returns a set of already-applied migration versions.
func (m *Migrator) getAppliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("querying applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scanning migration version: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// Status returns the list of applied migrations.
func (m *Migrator) Status(ctx context.Context) ([]MigrationRecord, error) {
	conn, err := m.db.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	if err := ensureTargetSchemas(ctx, conn); err != nil {
		return nil, err
	}

	// Ensure schema_migrations table exists before querying.
	if _, err := conn.Exec(ctx, createSchemaMigrationsSQL); err != nil {
		return nil, fmt.Errorf("creating schema_migrations table: %w", err)
	}

	rows, err := conn.Query(ctx,
		"SELECT version, name, applied_at FROM schema_migrations ORDER BY version",
	)
	if err != nil {
		return nil, fmt.Errorf("querying migration status: %w", err)
	}
	defer rows.Close()

	var records []MigrationRecord
	for rows.Next() {
		var r MigrationRecord
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt); err != nil {
			return nil, fmt.Errorf("scanning migration record: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Rollback rolls back the last applied migration by executing its .down.sql.
func (m *Migrator) Rollback(ctx context.Context) error {
	conn, err := m.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	// Acquire advisory lock.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquiring advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if err := ensureTargetSchemas(ctx, conn); err != nil {
		return err
	}

	// Ensure schema_migrations table exists.
	if _, err := conn.Exec(ctx, createSchemaMigrationsSQL); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Find the last applied migration version.
	var lastVersion int
	err = conn.QueryRow(ctx,
		"SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1",
	).Scan(&lastVersion)
	if err != nil {
		return fmt.Errorf("querying last migration: %w", err)
	}

	// Parse migrations to find the corresponding down SQL.
	migrations, err := parseMigrations()
	if err != nil {
		return err
	}

	var downSQL string
	for _, mig := range migrations {
		if mig.Version == lastVersion {
			downSQL = mig.DownSQL
			break
		}
	}

	if downSQL == "" {
		return fmt.Errorf("no down migration found for version %d", lastVersion)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning rollback transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, downSQL); err != nil {
		return fmt.Errorf("executing rollback for version %d: %w", lastVersion, err)
	}

	if _, err := tx.Exec(ctx,
		"DELETE FROM schema_migrations WHERE version = $1", lastVersion,
	); err != nil {
		return fmt.Errorf("removing migration record %d: %w", lastVersion, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing rollback: %w", err)
	}

	return nil
}
