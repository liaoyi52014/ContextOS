package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

var migrateDSN string

// migrateCmd is the parent command for database migrations.
var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Manage database migrations",
}

var migrateUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Run all pending migrations",
	RunE:  runMigrateUp,
}

var migrateDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Rollback the last migration",
	RunE:  runMigrateDown,
}

var migrateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show migration status",
	RunE:  runMigrateStatus,
}

func init() {
	migrateCmd.PersistentFlags().StringVar(&migrateDSN, "dsn", "", "PostgreSQL DSN override")
	migrateCmd.AddCommand(migrateUpCmd)
	migrateCmd.AddCommand(migrateDownCmd)
	migrateCmd.AddCommand(migrateStatusCmd)
}

// connectPG establishes a connection pool to PostgreSQL using the DSN flag or config.
func connectPG(ctx context.Context) (*pgxpool.Pool, *config.MigrateConfig, error) {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	dsn := migrateDSN
	if dsn == "" {
		dsn = cfg.Postgres.DSN
	}
	if dsn == "" {
		return nil, nil, fmt.Errorf("PostgreSQL DSN not configured (use --dsn or set postgres.dsn in config)")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}

	return pool, &cfg.Migrate, nil
}

func runMigrateUp(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, migCfg, err := connectPG(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	m := migrate.NewMigrator(pool, migCfg)
	fmt.Println("Running migrations...")
	if err := m.Run(ctx); err != nil {
		return fmt.Errorf("migration up: %w", err)
	}
	fmt.Println("Migrations completed successfully.")
	return nil
}

func runMigrateDown(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, migCfg, err := connectPG(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	m := migrate.NewMigrator(pool, migCfg)
	fmt.Println("Rolling back last migration...")
	if err := m.Rollback(ctx); err != nil {
		return fmt.Errorf("migration down: %w", err)
	}
	fmt.Println("Rollback completed successfully.")
	return nil
}

func runMigrateStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, migCfg, err := connectPG(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	m := migrate.NewMigrator(pool, migCfg)
	records, err := m.Status(ctx)
	if err != nil {
		return fmt.Errorf("migration status: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("No migrations applied.")
		return nil
	}

	fmt.Printf("%-10s %-30s %s\n", "VERSION", "NAME", "APPLIED AT")
	fmt.Println("---------- ------------------------------ ----------------------------")
	for _, r := range records {
		fmt.Printf("%-10d %-30s %s\n", r.Version, r.Name, r.AppliedAt.Format(time.RFC3339))
	}
	return nil
}
