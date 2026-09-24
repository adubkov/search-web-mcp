// Package config loads search-web-mcp configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Routing selects how requests are distributed across enabled backends.
type Routing string

const (
	RoutingRoundRobin Routing = "round_robin"
	RoutingPriority   Routing = "priority"

	BraveName  = "brave"
	TavilyName = "tavily"
)

// Config is the fully resolved server configuration.
type Config struct {
	BraveAPIKey   string
	TavilyAPIKey  string
	BraveBaseURL  string
	TavilyBaseURL string

	Routing  Routing
	Priority []string // backend order for priority routing

	Cooldown  time.Duration // initial 429 cooldown
	Timeout   time.Duration // upstream request timeout
	StateFile string        // throttle state file
}

// Load reads and validates configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		BraveAPIKey:   strings.TrimSpace(os.Getenv("BRAVE_API_KEY")),
		TavilyAPIKey:  strings.TrimSpace(os.Getenv("TAVILY_API_KEY")),
		BraveBaseURL:  envOr("SEARCH_MCP_BRAVE_BASE_URL", "https://api.search.brave.com"),
		TavilyBaseURL: envOr("SEARCH_MCP_TAVILY_BASE_URL", "https://api.tavily.com"),
		Routing:       RoutingRoundRobin,
		Priority:      []string{BraveName, TavilyName},
		Cooldown:      time.Hour,
		Timeout:       20 * time.Second,
	}

	switch r := strings.ToLower(os.Getenv("SEARCH_MCP_ROUTING")); r {
	case "", string(RoutingRoundRobin):
	case string(RoutingPriority):
		cfg.Routing = RoutingPriority
	default:
		return nil, fmt.Errorf("invalid SEARCH_MCP_ROUTING %q (want %s or %s)", r, RoutingRoundRobin, RoutingPriority)
	}

	if v := os.Getenv("SEARCH_MCP_PRIORITY"); v != "" {
		priority := []string{}
		seen := map[string]bool{}
		for _, p := range strings.Split(v, ",") {
			p = strings.ToLower(strings.TrimSpace(p))
			switch p {
			case BraveName, TavilyName:
				if !seen[p] {
					seen[p] = true
					priority = append(priority, p)
				}
			default:
				return nil, fmt.Errorf("invalid backend %q in SEARCH_MCP_PRIORITY", p)
			}
		}
		if len(priority) == 0 {
			return nil, fmt.Errorf("SEARCH_MCP_PRIORITY must name at least one backend")
		}
		cfg.Priority = priority
	}

	if v := os.Getenv("SEARCH_MCP_COOLDOWN"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid SEARCH_MCP_COOLDOWN %q: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("SEARCH_MCP_COOLDOWN must be positive")
		}
		cfg.Cooldown = d
	}

	if v := os.Getenv("SEARCH_MCP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid SEARCH_MCP_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("SEARCH_MCP_TIMEOUT must be positive")
		}
		cfg.Timeout = d
	}

	if v := os.Getenv("SEARCH_MCP_STATE_FILE"); v != "" {
		cfg.StateFile = v
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		cfg.StateFile = filepath.Join(home, ".local", "state", "search-web-mcp", "state.json")
	}

	if cfg.BraveAPIKey == "" && cfg.TavilyAPIKey == "" {
		return nil, fmt.Errorf("no API keys configured: set BRAVE_API_KEY and/or TAVILY_API_KEY")
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
