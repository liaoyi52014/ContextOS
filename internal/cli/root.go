package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	cfgFile    string
	outputFmt  string
	tenantFlag string
	userFlag   string
)

// rootCmd is the base command for the ctx CLI.
var rootCmd = &cobra.Command{
	Use:   "ctx",
	Short: "ContextOS — AI Agent context engine middleware CLI",
	Long:  "ContextOS CLI provides management commands and an interactive REPL for the context engine middleware.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// No subcommand provided — enter REPL mode.
		return runREPL()
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path (default: ~/.ctx/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&outputFmt, "output", "table", "output format: table or json")
	rootCmd.PersistentFlags().StringVar(&tenantFlag, "tenant", "", "tenant ID override")
	rootCmd.PersistentFlags().StringVar(&userFlag, "user", "", "user ID override")

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
}

// versionCmd prints the CLI version.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the ContextOS CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ctx version 0.1.0")
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
