package cli

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/contextos/contextos/internal/config"
)

// replState holds the current REPL session state.
type replState struct {
	loggedIn      bool
	username      string
	token         string
	cfg           *config.Config
	baseURL       string
	client        *http.Client
	serviceAPIKey string
}

// runREPL starts the interactive REPL loop.
func runREPL() error {
	state := &replState{}

	// Try to load config for auto-login.
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load config: %v\n", err)
	} else {
		state.cfg = cfg
		state.baseURL = replBaseURL(cfg)
		state.client = &http.Client{}
		state.serviceAPIKey = os.Getenv("CONTEXTOS_API_KEY")
	}

	fmt.Println("Welcome to ContextOS interactive CLI")
	fmt.Println("Type /help for available commands, /exit to quit")
	fmt.Println()

	// Attempt auto-login using config admin credentials.
	if state.cfg != nil && state.cfg.Admin.Username != "" && state.cfg.Admin.Password != "" {
		fmt.Printf("Auto-login as %q... ", state.cfg.Admin.Username)
		if err := replLogin(state, state.cfg.Admin.Username, state.cfg.Admin.Password); err != nil {
			fmt.Println("failed")
		} else {
			fmt.Println("ok")
		}
	}

	if !state.loggedIn {
		fmt.Println("Auto-login failed or no credentials configured.")
		fmt.Println("Use /admin login <username> <password> to log in.")
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("ctx> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			fmt.Println("Unknown input. Commands start with /. Type /help for usage.")
			continue
		}

		parts := strings.Fields(line)
		cmd := strings.ToLower(parts[0])
		args := parts[1:]

		if cmd == "/exit" {
			fmt.Println("Goodbye!")
			return nil
		}

		dispatchCommand(state, cmd, args)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	return nil
}
