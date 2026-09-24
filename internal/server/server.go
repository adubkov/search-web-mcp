// Package server wires the search backends into an MCP server with routing,
// failover, and 429 cooldown handling.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adubkov/search-web-mcp/internal/backend"
	"github.com/adubkov/search-web-mcp/internal/config"
	"github.com/adubkov/search-web-mcp/internal/throttle"
)

const (
	version        = "0.1.0"
	defaultResults = 10
	maxResultsCap  = 20
)

// Options configures the MCP server.
type Options struct {
	Backends []backend.Backend // canonical order (brave, tavily)
	Tracker  *throttle.Tracker
	Routing  config.Routing
	Priority []string // backend name order for priority routing
	Logger   *log.Logger
}

// NewServer builds the MCP server exposing the search_web and status tools.
func NewServer(opts Options) *mcp.Server {
	if opts.Logger == nil {
		opts.Logger = log.New(io.Discard, "", 0)
	}
	s := &state{
		opts:     opts,
		disabled: map[string]bool{},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "search-web-mcp", Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_web",
		Description: "Search the web. Proxies the query to Brave and/or Tavily; with backend=auto the server routes and fails over between available backends.",
		InputSchema: searchWebSchema(),
	}, s.searchWeb)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "status",
		Description: "Report per-backend configuration and throttle state. Does not call any search API.",
	}, s.status)
	return srv
}

type state struct {
	opts Options

	mu       sync.Mutex
	disabled map[string]bool // bad-key backends, process lifetime
}

type searchWebInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
	Backend    string `json:"backend,omitempty"`
	TimeRange  string `json:"time_range,omitempty"`
	Country    string `json:"country,omitempty"`
	Language   string `json:"language,omitempty"`
}

func searchWebSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     maxResultsCap,
				"description": fmt.Sprintf("Maximum results to return (default %d, max %d).", defaultResults, maxResultsCap),
			},
			"backend": map[string]any{
				"type":        "string",
				"enum":        []any{"auto", "brave", "tavily"},
				"description": "Provider to use; auto lets the server route (default).",
			},
			"time_range": map[string]any{
				"type":        "string",
				"enum":        []any{"day", "week", "month", "year"},
				"description": "Restrict results to this recent time window.",
			},
			"country": map[string]any{
				"type":        "string",
				"description": "Boost results from this country.",
			},
			"language": map[string]any{
				"type":        "string",
				"description": "Boost results in this language (ISO 639-1).",
			},
		},
		"required": []string{"query"},
	}
}

// searchWeb implements the search_web tool.
func (s *state) searchWeb(ctx context.Context, _ *mcp.CallToolRequest, in searchWebInput) (*mcp.CallToolResult, any, error) {
	req := backend.SearchRequest{
		Query:      strings.TrimSpace(in.Query),
		MaxResults: in.MaxResults,
		TimeRange:  in.TimeRange,
		Country:    in.Country,
		Language:   in.Language,
	}
	if req.Query == "" {
		return errorResult("query is required"), nil, nil
	}
	if req.MaxResults <= 0 {
		req.MaxResults = defaultResults
	}
	if req.MaxResults > maxResultsCap {
		req.MaxResults = maxResultsCap
	}

	order, ok := s.route(in.Backend)
	if !ok {
		return errorResult(fmt.Sprintf("unknown backend %q (configured: %s)", in.Backend, s.configuredNames())), nil, nil
	}

	now := time.Now()
	failed := make([]string, 0, len(order))
	for _, b := range order {
		if s.isDisabled(b.Name()) {
			failed = append(failed, fmt.Sprintf("%s: disabled (API key rejected earlier in this run)", b.Name()))
			continue
		}
		if ok, until := s.opts.Tracker.Throttled(b.Name(), now); ok {
			failed = append(failed, fmt.Sprintf("%s: throttled until %s", b.Name(), formatTime(until)))
			continue
		}

		hits, err := b.Search(ctx, req)
		if err == nil {
			s.opts.Tracker.NoteSuccess(b.Name())
			s.persist()
			out := struct {
				Backend  string              `json:"backend"`
				Results  []backend.SearchHit `json:"results"`
				Throttle map[string]string   `json:"throttle"`
			}{
				Backend:  b.Name(),
				Results:  hits,
				Throttle: s.throttleStatus(now),
			}
			data, _ := json.Marshal(out)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
		}

		var te *backend.ThrottledError
		var be *backend.BadKeyError
		switch {
		case errors.As(err, &te):
			until := s.opts.Tracker.Note429(b.Name(), time.Now(), te.RetryAfter)
			s.persist()
			failed = append(failed, fmt.Sprintf("%s: throttled until %s", b.Name(), formatTime(until)))
			s.opts.Logger.Printf("%s returned 429; cooling down until %s", b.Name(), formatTime(until))
		case errors.As(err, &be):
			s.disable(b.Name())
			failed = append(failed, be.Error())
			s.opts.Logger.Printf("%s: %s", b.Name(), be.Error())
		default:
			failed = append(failed, fmt.Sprintf("%s: %v", b.Name(), err))
		}
	}
	return errorResult("search failed: " + strings.Join(failed, "; ")), nil, nil
}

// route resolves the ordered backend list for a request.
func (s *state) route(pinned string) ([]backend.Backend, bool) {
	if pinned == "" || pinned == "auto" {
		switch s.opts.Routing {
		case config.RoutingPriority:
			return orderByNames(s.opts.Backends, s.opts.Priority), true
		default:
			idx := s.opts.Tracker.NextRotation(len(s.opts.Backends))
			return rotate(s.opts.Backends, idx), true
		}
	}
	for _, b := range s.opts.Backends {
		if b.Name() == pinned {
			return []backend.Backend{b}, true
		}
	}
	return nil, false
}

// status implements the status tool.
func (s *state) status(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	now := time.Now()
	out := struct {
		Routing  string               `json:"routing"`
		Backends map[string]backendSt `json:"backends"`
	}{Routing: string(s.opts.Routing), Backends: map[string]backendSt{}}
	for _, b := range s.opts.Backends {
		st := backendSt{Configured: true}
		if s.isDisabled(b.Name()) {
			st.Disabled = true
		}
		if ok, until := s.opts.Tracker.Throttled(b.Name(), now); ok {
			st.Throttled = true
			st.Until = formatTime(until)
		}
		out.Backends[b.Name()] = st
	}
	data, _ := json.Marshal(out)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}

type backendSt struct {
	Configured bool   `json:"configured"`
	Disabled   bool   `json:"disabled,omitempty"`
	Throttled  bool   `json:"throttled"`
	Until      string `json:"until,omitempty"`
}

func (s *state) throttleStatus(now time.Time) map[string]string {
	m := make(map[string]string, len(s.opts.Backends))
	for _, b := range s.opts.Backends {
		if ok, until := s.opts.Tracker.Throttled(b.Name(), now); ok {
			m[b.Name()] = "throttled until " + formatTime(until)
		} else {
			m[b.Name()] = "ok"
		}
	}
	return m
}

func (s *state) configuredNames() string {
	names := make([]string, 0, len(s.opts.Backends))
	for _, b := range s.opts.Backends {
		names = append(names, b.Name())
	}
	return strings.Join(names, ", ")
}

func (s *state) isDisabled(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disabled[name]
}

func (s *state) disable(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled[name] = true
}

// persist saves the throttle tracker to disk, logging (not failing) on error.
func (s *state) persist() {
	if err := s.opts.Tracker.Save(); err != nil {
		s.opts.Logger.Printf("persist throttle state: %v", err)
	}
}

func orderByNames(backends []backend.Backend, order []string) []backend.Backend {
	out := make([]backend.Backend, 0, len(backends))
	for _, name := range order {
		for _, b := range backends {
			if b.Name() == name {
				out = append(out, b)
			}
		}
	}
	return out
}

func rotate(backends []backend.Backend, start int) []backend.Backend {
	if len(backends) == 0 {
		return backends
	}
	start %= len(backends)
	out := make([]backend.Backend, 0, len(backends))
	for i := 0; i < len(backends); i++ {
		out = append(out, backends[(start+i)%len(backends)])
	}
	return out
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}
