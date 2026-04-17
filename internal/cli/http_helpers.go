package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/contextos/contextos/internal/config"
)

func replBaseURL(cfg *config.Config) string {
	if cfg != nil {
		if cfg.Server.URL != "" {
			return strings.TrimRight(cfg.Server.URL, "/")
		}
		if cfg.Server.Port > 0 {
			return fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
		}
	}
	return "http://127.0.0.1:8080"
}

func replLogin(state *replState, username, password string) error {
	if state == nil {
		return fmt.Errorf("repl state is nil")
	}
	resp, err := replDoJSON(state.client, http.MethodPost, state.baseURL+"/api/v1/auth/login", nil, map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return err
	}
	state.loggedIn = true
	state.username = username
	if token, _ := resp["token"].(string); token != "" {
		state.token = token
	}
	return nil
}

func replDoJSON(client *http.Client, method, url string, headers map[string]string, body interface{}) (map[string]interface{}, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}
	if len(data) == 0 {
		return map[string]interface{}{}, nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]interface{}{"raw": string(data)}, nil
	}
	return out, nil
}

func adminHeaders(state *replState) map[string]string {
	headers := map[string]string{}
	if state != nil && state.token != "" {
		headers["Authorization"] = "Bearer " + state.token
	}
	return headers
}

func serviceHeaders(state *replState) map[string]string {
	headers := map[string]string{}
	if state != nil && state.serviceAPIKey != "" {
		headers["X-API-Key"] = state.serviceAPIKey
	}
	if tenantFlag != "" {
		headers["X-Tenant-ID"] = tenantFlag
	}
	if userFlag != "" {
		headers["X-User-ID"] = userFlag
	}
	return headers
}

func printResult(v interface{}) {
	if outputFmt == "json" {
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
		return
	}
	switch val := v.(type) {
	case string:
		fmt.Println(val)
	default:
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
	}
}

func requireServiceKey(state *replState) bool {
	if state != nil && state.serviceAPIKey != "" {
		return true
	}
	if key := os.Getenv("CONTEXTOS_API_KEY"); key != "" {
		state.serviceAPIKey = key
		return true
	}
	fmt.Println("Service API key is required. Set CONTEXTOS_API_KEY or create one with /apikey create.")
	return false
}
