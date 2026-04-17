//go:build tools

package internal

// This file ensures core dependencies are tracked in go.mod.
// It is excluded from normal builds via the "tools" build tag.
import (
	_ "github.com/gin-gonic/gin"
	_ "github.com/hashicorp/golang-lru/v2"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/redis/go-redis/v9"
	_ "github.com/spf13/cobra"
	_ "github.com/spf13/viper"
	_ "go.uber.org/zap"
	_ "pgregory.net/rapid"
)
