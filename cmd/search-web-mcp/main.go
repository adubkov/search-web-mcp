// Command search-web-mcp runs a stdio MCP server exposing a search_web tool that
// proxies to the Brave and/or Tavily search APIs.
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adubkov/search-web-mcp/internal/backend"
	"github.com/adubkov/search-web-mcp/internal/config"
	"github.com/adubkov/search-web-mcp/internal/server"
	"github.com/adubkov/search-web-mcp/internal/throttle"
)

func main() {
	// Logs must go to stderr: stdout is the MCP JSON-RPC channel.
	logger := log.New(os.Stderr, "search-web-mcp: ", log.LstdFlags)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal(err)
	}

	tracker, err := throttle.Load(cfg.StateFile, cfg.Cooldown)
	if err != nil {
		logger.Fatal(err)
	}

	var backends []backend.Backend
	if cfg.BraveAPIKey != "" {
		backends = append(backends, backend.NewBrave(cfg.BraveAPIKey, cfg.Timeout, cfg.BraveBaseURL))
	}
	if cfg.TavilyAPIKey != "" {
		backends = append(backends, backend.NewTavily(cfg.TavilyAPIKey, cfg.Timeout, cfg.TavilyBaseURL))
	}
	for _, name := range cfg.Priority {
		configured := false
		for _, b := range backends {
			if b.Name() == name {
				configured = true
			}
		}
		if !configured {
			logger.Printf("backend %q in SEARCH_MCP_PRIORITY is not configured; ignoring", name)
		}
	}

	srv := server.NewServer(server.Options{
		Backends: backends,
		Tracker:  tracker,
		Routing:  cfg.Routing,
		Priority: cfg.Priority,
		Logger:   logger,
	})

	logger.Printf("ready (routing=%s, backends=%s, state=%s)", cfg.Routing, backendNames(backends), cfg.StateFile)
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Fatal(err)
	}
}

func backendNames(backends []backend.Backend) string {
	names := make([]string, 0, len(backends))
	for _, b := range backends {
		names = append(names, b.Name())
	}
	return strings.Join(names, ",")
}
