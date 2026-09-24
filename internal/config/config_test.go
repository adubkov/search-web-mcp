package config

import (
	"path/filepath"
	"testing"
	"time"
)

// allEnvKeys are cleared before each test so ambient shell keys (e.g. a real
// TAVILY_API_KEY) cannot leak into the results.
var allEnvKeys = []string{
	"BRAVE_API_KEY",
	"TAVILY_API_KEY",
	"SEARCH_MCP_ROUTING",
	"SEARCH_MCP_PRIORITY",
	"SEARCH_MCP_COOLDOWN",
	"SEARCH_MCP_TIMEOUT",
	"SEARCH_MCP_STATE_FILE",
	"SEARCH_MCP_BRAVE_BASE_URL",
	"SEARCH_MCP_TAVILY_BASE_URL",
}

func loadWith(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	for _, k := range allEnvKeys {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

func TestLoadRequiresAtLeastOneKey(t *testing.T) {
	if _, err := loadWith(t, nil); err == nil {
		t.Fatal("want error when no keys are set")
	}
	// Whitespace-only keys count as unset.
	if _, err := loadWith(t, map[string]string{"BRAVE_API_KEY": "   "}); err == nil {
		t.Fatal("want error when the only key is blank")
	}
}

func TestLoadDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := loadWith(t, map[string]string{"BRAVE_API_KEY": "bk"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BraveAPIKey != "bk" || cfg.TavilyAPIKey != "" {
		t.Fatalf("keys = %q/%q", cfg.BraveAPIKey, cfg.TavilyAPIKey)
	}
	if cfg.Routing != RoutingRoundRobin {
		t.Fatalf("routing = %s, want round_robin", cfg.Routing)
	}
	if len(cfg.Priority) != 2 || cfg.Priority[0] != BraveName || cfg.Priority[1] != TavilyName {
		t.Fatalf("priority = %v", cfg.Priority)
	}
	if cfg.Cooldown != time.Hour {
		t.Fatalf("cooldown = %s", cfg.Cooldown)
	}
	if cfg.Timeout != 20*time.Second {
		t.Fatalf("timeout = %s", cfg.Timeout)
	}
	if cfg.BraveBaseURL != "https://api.search.brave.com" {
		t.Fatalf("brave base = %s", cfg.BraveBaseURL)
	}
	if cfg.TavilyBaseURL != "https://api.tavily.com" {
		t.Fatalf("tavily base = %s", cfg.TavilyBaseURL)
	}
	if want := filepath.Join(home, ".local", "state", "search-web-mcp", "state.json"); cfg.StateFile != want {
		t.Fatalf("state file = %s, want %s", cfg.StateFile, want)
	}
}

func TestLoadSingleBackend(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{"TAVILY_API_KEY": "tk"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TavilyAPIKey != "tk" || cfg.BraveAPIKey != "" {
		t.Fatalf("keys = %q/%q", cfg.BraveAPIKey, cfg.TavilyAPIKey)
	}
}

func TestLoadRouting(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_ROUTING": "priority"})
	if err != nil || cfg.Routing != RoutingPriority {
		t.Fatalf("priority: cfg=%+v err=%v", cfg, err)
	}
	cfg, err = loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_ROUTING": "ROUND_ROBIN"})
	if err != nil || cfg.Routing != RoutingRoundRobin {
		t.Fatalf("round_robin (case-insensitive): cfg=%+v err=%v", cfg, err)
	}
	if _, err = loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_ROUTING": "chaos"}); err == nil {
		t.Fatal("want error for invalid routing")
	}
}

func TestLoadPriority(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_PRIORITY": "tavily, brave ,brave"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Priority) != 2 || cfg.Priority[0] != TavilyName || cfg.Priority[1] != BraveName {
		t.Fatalf("priority = %v, want [tavily brave] (trimmed, deduped)", cfg.Priority)
	}
	if _, err = loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_PRIORITY": ","}); err == nil {
		t.Fatal("want error for empty tokens in priority")
	}
	if _, err = loadWith(t, map[string]string{"BRAVE_API_KEY": "k", "SEARCH_MCP_PRIORITY": "nope"}); err == nil {
		t.Fatal("want error for unknown backend in priority")
	}
}

func TestLoadDurations(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"BRAVE_API_KEY": "k", "SEARCH_MCP_COOLDOWN": "30m", "SEARCH_MCP_TIMEOUT": "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cooldown != 30*time.Minute || cfg.Timeout != 5*time.Second {
		t.Fatalf("cooldown/timeout = %s/%s", cfg.Cooldown, cfg.Timeout)
	}
	for _, env := range []map[string]string{
		{"SEARCH_MCP_COOLDOWN": "soon"},
		{"SEARCH_MCP_COOLDOWN": "-5s"},
		{"SEARCH_MCP_TIMEOUT": "soon"},
		{"SEARCH_MCP_TIMEOUT": "0"},
	} {
		full := map[string]string{"BRAVE_API_KEY": "k"}
		for k, v := range env {
			full[k] = v
		}
		if _, err := loadWith(t, full); err == nil {
			t.Fatalf("want error for %v", env)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	state := filepath.Join(t.TempDir(), "custom-state.json")
	cfg, err := loadWith(t, map[string]string{
		"BRAVE_API_KEY":              "bk",
		"TAVILY_API_KEY":             "tk",
		"SEARCH_MCP_STATE_FILE":      state,
		"SEARCH_MCP_BRAVE_BASE_URL":  "http://127.0.0.1:1/brave",
		"SEARCH_MCP_TAVILY_BASE_URL": "http://127.0.0.1:2/tavily/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateFile != state {
		t.Fatalf("state file = %s", cfg.StateFile)
	}
	if cfg.BraveBaseURL != "http://127.0.0.1:1/brave" || cfg.TavilyBaseURL != "http://127.0.0.1:2/tavily/" {
		t.Fatalf("bases = %s / %s", cfg.BraveBaseURL, cfg.TavilyBaseURL)
	}
}
