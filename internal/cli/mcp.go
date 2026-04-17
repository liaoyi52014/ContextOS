package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/contextos/contextos/internal/config"
	"github.com/contextos/contextos/internal/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the ContextOS MCP server over stdio",
	RunE:  runMCP,
}

func runMCP(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	env, err := bootstrapRuntime(cfg)
	if err != nil {
		return err
	}
	defer env.Close()

	server := mcp.NewMCPServer(env.engine, "")
	return server.ServeStdio(context.Background(), os.Stdin, os.Stdout)
}
