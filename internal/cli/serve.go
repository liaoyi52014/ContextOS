package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/contextos/contextos/internal/cluster"
	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/store"
	"github.com/contextos/contextos/internal/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the ContextOS HTTP server",
	RunE:  runServe,
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	env, err := bootstrapRuntime(cfg)
	if err != nil {
		return err
	}
	defer env.Close()

	port := cfg.Server.Port
	if port == 0 {
		port = 8080
	}
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: env.server.SetupRouter(),
	}

	shutdown := cluster.NewGracefulShutdown()
	shutdown.RegisterHTTPServer(httpServer)
	shutdown.RegisterSyncQueue(env.sessionManager.Stop)

	fmt.Printf("ContextOS server listening on :%d\n", port)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return shutdown.Shutdown(shutdownCtx)
}

func connectRedis(cfg config.RedisConfig) (redis.UniversalClient, error) {
	switch cfg.Mode {
	case config.RedisModeSentinel:
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       cfg.SentinelMaster,
			SentinelAddrs:    cfg.SentinelAddrs,
			SentinelPassword: cfg.SentinelPassword,
			Password:         cfg.Password,
			DB:               cfg.DB,
			PoolSize:         cfg.PoolSize,
			MaxRetries:       cfg.MaxRetries,
		}), nil
	case config.RedisModeCluster:
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:      cfg.ClusterAddrs,
			Password:   cfg.Password,
			PoolSize:   cfg.PoolSize,
			MaxRetries: cfg.MaxRetries,
		}), nil
	default:
		return redis.NewClient(&redis.Options{
			Addr:       cfg.Addr,
			Password:   cfg.Password,
			DB:         cfg.DB,
			PoolSize:   cfg.PoolSize,
			MaxRetries: cfg.MaxRetries,
		}), nil
	}
}

func buildVectorStore(backend string, db *pgxpool.Pool) (types.VectorStore, error) {
	switch backend {
	case "", "pgvector":
		return store.NewPGVectorStore(db), nil
	case "elasticsearch":
		return store.NewESVectorStore(), nil
	case "milvus":
		return store.NewMilvusVectorStore(), nil
	default:
		return nil, fmt.Errorf("unsupported vector backend %q", backend)
	}
}
