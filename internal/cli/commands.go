package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// commandInfo describes a slash command.
type commandInfo struct {
	Name        string
	Description string
	Usage       string
	Handler     func(state *replState, args []string)
}

// commands is the registry of all slash commands.
var commands map[string]*commandInfo

func init() {
	commands = map[string]*commandInfo{
		"/admin":    {Name: "/admin", Description: "Manage admin users (list, create, update-password, disable, login)", Usage: "/admin [login|list|create|update-password|disable] [args...]", Handler: handleAdmin},
		"/provider": {Name: "/provider", Description: "Manage model providers (list, add, update, remove)", Usage: "/provider [list|add|update|remove] [args...]", Handler: handleProvider},
		"/model":    {Name: "/model", Description: "Manage models (list, add, enable, disable, default)", Usage: "/model [list|add|enable|disable|default] [args...]", Handler: handleModel},
		"/skill":    {Name: "/skill", Description: "Manage skills (list, add, info, enable, disable, remove)", Usage: "/skill [list|add|info|enable|disable|remove] [args...]", Handler: handleSkill},
		"/session":  {Name: "/session", Description: "Manage sessions (list, delete)", Usage: "/session [list|delete] [args...]", Handler: handleSession},
		"/memory":   {Name: "/memory", Description: "Manage memories (search, store, delete)", Usage: "/memory [search|store|delete] [args...]", Handler: handleMemory},
		"/search":   {Name: "/search", Description: "Search context memories", Usage: "/search <query>", Handler: handleSearch},
		"/apikey":   {Name: "/apikey", Description: "Manage API keys (list, create, revoke)", Usage: "/apikey [list|create|revoke] [args...]", Handler: handleAPIKey},
		"/migrate":  {Name: "/migrate", Description: "Run database migrations (up, down, status)", Usage: "/migrate [up|down|status]", Handler: handleMigrate},
		"/logs":     {Name: "/logs", Description: "View recent logs", Usage: "/logs", Handler: handleLogs},
		"/status":   {Name: "/status", Description: "Show system status (observer/system, observer/queue, tasks)", Usage: "/status [system|queue|tasks]", Handler: handleStatus},
		"/help":     {Name: "/help", Description: "Show available commands", Usage: "/help [command]", Handler: handleHelp},
		"/logout":   {Name: "/logout", Description: "Log out of the current admin session", Usage: "/logout", Handler: handleLogout},
	}
}

func dispatchCommand(state *replState, cmd string, args []string) {
	info, ok := commands[cmd]
	if !ok {
		fmt.Printf("Unknown command: %s. Type /help for available commands.\n", cmd)
		return
	}
	info.Handler(state, args)
}

func handleHelp(state *replState, args []string) {
	if len(args) > 0 {
		target := args[0]
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}
		info, ok := commands[target]
		if !ok {
			fmt.Printf("Unknown command: %s\n", target)
			return
		}
		printResult(map[string]string{
			"command":     info.Name,
			"description": info.Description,
			"usage":       info.Usage,
		})
		return
	}

	if outputFmt == "json" {
		var list []map[string]string
		for _, info := range commands {
			list = append(list, map[string]string{
				"command":     info.Name,
				"description": info.Description,
			})
		}
		printResult(list)
		return
	}

	fmt.Println("Available commands:")
	sorted := []string{"/admin", "/provider", "/model", "/skill", "/session", "/memory", "/search", "/apikey", "/migrate", "/logs", "/status", "/help", "/logout", "/exit"}
	for _, name := range sorted {
		info, ok := commands[name]
		if ok {
			fmt.Printf("  %-12s %s\n", info.Name, info.Description)
		} else if name == "/exit" {
			fmt.Printf("  %-12s Exit the REPL\n", "/exit")
		}
	}
}

func handleLogout(state *replState, args []string) {
	if !state.loggedIn {
		fmt.Println("Not logged in.")
		return
	}
	state.loggedIn = false
	state.username = ""
	state.token = ""
	fmt.Println("Logged out.")
}

func handleAdmin(state *replState, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/admin"].Usage)
		return
	}
	switch args[0] {
	case "login":
		if len(args) < 3 {
			fmt.Println("Usage: /admin login <username> <password>")
			return
		}
		if err := replLogin(state, args[1], args[2]); err != nil {
			fmt.Println("Login failed:", err)
			return
		}
		fmt.Println("Logged in as", args[1])
	case "list":
		getAndPrint(state, state.baseURL+"/api/v1/admin/users", adminHeaders(state))
	case "create":
		if len(args) < 3 {
			fmt.Println("Usage: /admin create <username> <password>")
			return
		}
		postAndPrint(state, state.baseURL+"/api/v1/admin/users", adminHeaders(state), map[string]string{"username": args[1], "password": args[2]})
	case "update-password":
		if len(args) < 3 {
			fmt.Println("Usage: /admin update-password <id> <password>")
			return
		}
		putAndPrint(state, state.baseURL+"/api/v1/admin/users/"+args[1]+"/password", adminHeaders(state), map[string]string{"password": args[2]})
	case "disable":
		if len(args) < 2 {
			fmt.Println("Usage: /admin disable <id>")
			return
		}
		putAndPrint(state, state.baseURL+"/api/v1/admin/users/"+args[1]+"/disable", adminHeaders(state), map[string]string{})
	default:
		fmt.Println("Usage:", commands["/admin"].Usage)
	}
}

func handleProvider(state *replState, args []string) {
	handleProviderOrModelBase(state, args, "provider")
}

func handleModel(state *replState, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/model"].Usage)
		return
	}
	switch args[0] {
	case "list":
		getAndPrint(state, state.baseURL+"/api/v1/admin/models", adminHeaders(state))
	case "add":
		if len(args) < 5 {
			fmt.Println("Usage: /model add <name> <provider_id> <model_id> <type> [dimension]")
			return
		}
		payload := map[string]interface{}{
			"name":        args[1],
			"provider_id": args[2],
			"model_id":    args[3],
			"type":        args[4],
			"enabled":     true,
		}
		if len(args) > 5 {
			payload["dimension"] = parseIntArg(args[5])
		}
		postAndPrint(state, state.baseURL+"/api/v1/admin/models", adminHeaders(state), payload)
	case "enable", "disable", "default":
		if len(args) < 2 {
			fmt.Printf("Usage: /model %s <id>\n", args[0])
			return
		}
		putAndPrint(state, state.baseURL+"/api/v1/admin/models/"+args[1]+"/"+args[0], adminHeaders(state), map[string]string{})
	default:
		fmt.Println("Usage:", commands["/model"].Usage)
	}
}

func handleSkill(state *replState, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/skill"].Usage)
		return
	}
	switch args[0] {
	case "list":
		getAndPrint(state, state.baseURL+"/api/v1/skills/", adminHeaders(state))
	case "info":
		if len(args) < 2 {
			fmt.Println("Usage: /skill info <id>")
			return
		}
		getAndPrint(state, state.baseURL+"/api/v1/skills/"+args[1], adminHeaders(state))
	case "add":
		if len(args) < 2 {
			fmt.Println("Usage: /skill add <json-file>")
			return
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Println("Read skill file failed:", err)
			return
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(data, &payload); err != nil {
			fmt.Println("Skill file must be JSON:", err)
			return
		}
		postAndPrint(state, state.baseURL+"/api/v1/skills/", adminHeaders(state), payload)
	case "enable", "disable":
		if len(args) < 2 {
			fmt.Printf("Usage: /skill %s <id>\n", args[0])
			return
		}
		putAndPrint(state, state.baseURL+"/api/v1/skills/"+args[1]+"/"+args[0], adminHeaders(state), map[string]string{})
	case "remove":
		if len(args) < 2 {
			fmt.Println("Usage: /skill remove <id>")
			return
		}
		deleteAndPrint(state, state.baseURL+"/api/v1/skills/"+args[1], adminHeaders(state))
	default:
		fmt.Println("Usage:", commands["/skill"].Usage)
	}
}

func handleSession(state *replState, args []string) {
	if !requireServiceKey(state) {
		return
	}
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/session"].Usage)
		return
	}
	switch args[0] {
	case "list":
		getAndPrint(state, state.baseURL+"/api/v1/sessions", serviceHeaders(state))
	case "delete":
		if len(args) < 2 {
			fmt.Println("Usage: /session delete <id>")
			return
		}
		deleteAndPrint(state, state.baseURL+"/api/v1/sessions/"+args[1], serviceHeaders(state))
	default:
		fmt.Println("Usage:", commands["/session"].Usage)
	}
}

func handleMemory(state *replState, args []string) {
	if !requireServiceKey(state) {
		return
	}
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/memory"].Usage)
		return
	}
	switch args[0] {
	case "search":
		if len(args) < 2 {
			fmt.Println("Usage: /memory search <query>")
			return
		}
		postAndPrint(state, state.baseURL+"/api/v1/memory/search", serviceHeaders(state), map[string]interface{}{"query": strings.Join(args[1:], " "), "limit": 10})
	case "store":
		if len(args) < 2 {
			fmt.Println("Usage: /memory store <content>")
			return
		}
		postAndPrint(state, state.baseURL+"/api/v1/memory/store", serviceHeaders(state), map[string]interface{}{"content": strings.Join(args[1:], " ")})
	case "delete":
		if len(args) < 2 {
			fmt.Println("Usage: /memory delete <id>")
			return
		}
		deleteAndPrint(state, state.baseURL+"/api/v1/memory/"+args[1], serviceHeaders(state))
	default:
		fmt.Println("Usage:", commands["/memory"].Usage)
	}
}

func handleSearch(state *replState, args []string) {
	handleMemory(state, append([]string{"search"}, args...))
}

func handleAPIKey(state *replState, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/apikey"].Usage)
		return
	}
	switch args[0] {
	case "list":
		getAndPrint(state, state.baseURL+"/api/v1/admin/apikeys", adminHeaders(state))
	case "create":
		if len(args) < 2 {
			fmt.Println("Usage: /apikey create <name>")
			return
		}
		resp, err := replDoJSON(state.client, http.MethodPost, state.baseURL+"/api/v1/admin/apikeys", adminHeaders(state), map[string]string{"name": args[1]})
		if err != nil {
			fmt.Println("API key create failed:", err)
			return
		}
		if key, _ := resp["key"].(string); key != "" {
			state.serviceAPIKey = key
		}
		printResult(resp)
	case "revoke":
		if len(args) < 2 {
			fmt.Println("Usage: /apikey revoke <id>")
			return
		}
		deleteAndPrint(state, state.baseURL+"/api/v1/admin/apikeys/"+args[1], adminHeaders(state))
	default:
		fmt.Println("Usage:", commands["/apikey"].Usage)
	}
}

func handleMigrate(state *replState, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage:", commands["/migrate"].Usage)
		return
	}
	var err error
	switch args[0] {
	case "up":
		err = runMigrateUp(nil, nil)
	case "down":
		err = runMigrateDown(nil, nil)
	case "status":
		err = runMigrateStatus(nil, nil)
	default:
		fmt.Println("Usage:", commands["/migrate"].Usage)
		return
	}
	if err != nil {
		fmt.Println("Migrate failed:", err)
	}
}

func handleLogs(state *replState, args []string) {
	fmt.Println("Remote log query is not supported by the current server API.")
}

func handleStatus(state *replState, args []string) {
	target := "system"
	if len(args) > 0 {
		target = args[0]
	}
	switch target {
	case "system":
		getAndPrint(state, state.baseURL+"/api/v1/observer/system", adminHeaders(state))
	case "queue", "tasks":
		getAndPrint(state, state.baseURL+"/api/v1/observer/queue", adminHeaders(state))
	default:
		fmt.Println("Usage:", commands["/status"].Usage)
	}
}

func handleProviderOrModelBase(state *replState, args []string, resource string) {
	if len(args) == 0 {
		fmt.Printf("Usage: /%s [list|add|update|remove]\n", resource)
		return
	}
	basePath := state.baseURL + "/api/v1/admin/" + resource + "s"
	switch args[0] {
	case "list":
		getAndPrint(state, basePath, adminHeaders(state))
	case "add":
		if len(args) < 4 {
			fmt.Printf("Usage: /%s add <name> <api_base> <api_key>\n", resource)
			return
		}
		postAndPrint(state, basePath, adminHeaders(state), map[string]interface{}{"name": args[1], "api_base": args[2], "api_key": args[3], "enabled": true})
	case "update":
		if len(args) < 5 {
			fmt.Printf("Usage: /%s update <id> <name> <api_base> <api_key>\n", resource)
			return
		}
		putAndPrint(state, basePath+"/"+args[1], adminHeaders(state), map[string]interface{}{"name": args[2], "api_base": args[3], "api_key": args[4], "enabled": true})
	case "remove":
		if len(args) < 2 {
			fmt.Printf("Usage: /%s remove <id>\n", resource)
			return
		}
		deleteAndPrint(state, basePath+"/"+args[1], adminHeaders(state))
	default:
		fmt.Printf("Usage: /%s [list|add|update|remove]\n", resource)
	}
}

func getAndPrint(state *replState, url string, headers map[string]string) {
	resp, err := replDoJSON(state.client, http.MethodGet, url, headers, nil)
	if err != nil {
		fmt.Println("Request failed:", err)
		return
	}
	printResult(resp)
}

func postAndPrint(state *replState, url string, headers map[string]string, body interface{}) {
	resp, err := replDoJSON(state.client, http.MethodPost, url, headers, body)
	if err != nil {
		fmt.Println("Request failed:", err)
		return
	}
	printResult(resp)
}

func putAndPrint(state *replState, url string, headers map[string]string, body interface{}) {
	resp, err := replDoJSON(state.client, http.MethodPut, url, headers, body)
	if err != nil {
		fmt.Println("Request failed:", err)
		return
	}
	printResult(resp)
}

func deleteAndPrint(state *replState, url string, headers map[string]string) {
	resp, err := replDoJSON(state.client, http.MethodDelete, url, headers, nil)
	if err != nil {
		fmt.Println("Request failed:", err)
		return
	}
	printResult(resp)
}

func parseIntArg(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
