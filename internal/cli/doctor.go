package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/contextos/contextos/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
)

// doctorCmd checks system health and configuration.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health and configuration",
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("ContextOS Doctor")
	fmt.Println("================")
	fmt.Println()

	// Check 1: Config file readable.
	cfg, cfgErr := config.LoadConfig(cfgFile)
	if cfgErr != nil {
		printCheck(false, "Config file", fmt.Sprintf("failed to load: %v", cfgErr))
		fmt.Println("  Suggestion: create a config file at ~/.ctx/config.yaml or specify --config")
		// Continue with nil config for remaining checks.
	} else {
		printCheck(true, "Config file", "loaded successfully")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check 2: PostgreSQL connectable.
	if cfg != nil && cfg.Postgres.DSN != "" {
		pool, err := pgxpool.New(ctx, cfg.Postgres.DSN)
		if err != nil {
			printCheck(false, "PostgreSQL", fmt.Sprintf("connection failed: %v", err))
			fmt.Println("  Suggestion: verify postgres.dsn in config and ensure PostgreSQL is running")
		} else {
			err = pool.Ping(ctx)
			pool.Close()
			if err != nil {
				printCheck(false, "PostgreSQL", fmt.Sprintf("ping failed: %v", err))
				fmt.Println("  Suggestion: check PostgreSQL connectivity and credentials")
			} else {
				printCheck(true, "PostgreSQL", "connected")
			}
		}
	} else {
		printCheck(false, "PostgreSQL", "DSN not configured")
		fmt.Println("  Suggestion: set postgres.dsn in config file")
	}

	// Check 3: Redis connectable.
	if cfg != nil && cfg.Redis.Addr != "" {
		rdb := redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		err := rdb.Ping(ctx).Err()
		rdb.Close()
		if err != nil {
			printCheck(false, "Redis", fmt.Sprintf("ping failed: %v", err))
			fmt.Println("  Suggestion: verify redis.addr in config and ensure Redis is running")
		} else {
			printCheck(true, "Redis", "connected")
		}
	} else {
		printCheck(false, "Redis", "address not configured")
		fmt.Println("  Suggestion: set redis.addr in config file")
	}

	// Check 4: Model config present.
	if cfg != nil && cfg.LLM.APIBase != "" && cfg.LLM.Model != "" {
		printCheck(true, "LLM config", fmt.Sprintf("model=%s", cfg.LLM.Model))
	} else {
		printCheck(false, "LLM config", "LLM API base or model not configured")
		fmt.Println("  Suggestion: set llm.api_base and llm.model in config file")
	}

	fmt.Println()
	return nil
}

// printCheck prints a check result with ✓ or ✗.
func printCheck(ok bool, name, detail string) {
	if ok {
		fmt.Printf("  ✓ %-20s %s\n", name, detail)
	} else {
		fmt.Printf("  ✗ %-20s %s\n", name, detail)
	}
}
