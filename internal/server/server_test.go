package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adubkov/search-web-mcp/internal/backend"
	"github.com/adubkov/search-web-mcp/internal/config"
	"github.com/adubkov/search-web-mcp/internal/throttle"
)

// fakeBackend is a scripted backend for routing tests.
type fakeBackend struct {
	name  string
	calls int
	errs  []error // one per call, then success
	hits  []backend.SearchHit
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) Search(_ context.Context, _ backend.SearchRequest) ([]backend.SearchHit, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) {
		return nil, f.errs[i]
	}
	return f.hits, nil
}

func newTestState(t *testing.T, routing config.Routing, backends ...backend.Backend) *state {
	t.Helper()
	tk, err := throttle.Load(filepath.Join(t.TempDir(), "state.json"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &state{
		opts: Options{
			Backends: backends,
			Tracker:  tk,
			Routing:  routing,
			Priority: []string{config.BraveName, config.TavilyName},
			Logger:   log.New(io.Discard, "", 0),
		},
		disabled: map[string]bool{},
	}
}

func callSearch(t *testing.T, s *state, in searchWebInput) *mcp.CallToolResult {
	t.Helper()
	res, _, err := s.searchWeb(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("searchWeb: %v", err)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func backendFromResult(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var out struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("result is not JSON: %v: %s", err, resultText(t, res))
	}
	return out.Backend
}

func hits() []backend.SearchHit {
	return []backend.SearchHit{{Title: "h", URL: "https://h.example"}}
}

func TestRoundRobinCycles(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)

	for i, want := range []string{"brave", "tavily", "brave"} {
		res := callSearch(t, s, searchWebInput{Query: "x"})
		if got := backendFromResult(t, res); got != want {
			t.Fatalf("call %d: backend = %s, want %s", i, got, want)
		}
	}
}

func TestPriorityOrder(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingPriority, b0, b1)

	for i := 0; i < 3; i++ {
		res := callSearch(t, s, searchWebInput{Query: "x"})
		if got := backendFromResult(t, res); got != "brave" {
			t.Fatalf("call %d: backend = %s, want brave", i, got)
		}
	}
}

func TestSkipThrottled(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)
	s.opts.Tracker.Note429("brave", time.Now(), time.Hour)

	res := callSearch(t, s, searchWebInput{Query: "x"})
	if got := backendFromResult(t, res); got != "tavily" {
		t.Fatalf("backend = %s, want tavily", got)
	}
	if b0.calls != 0 {
		t.Fatalf("throttled backend was called %d times", b0.calls)
	}
	if !strings.Contains(resultText(t, res), "brave") {
		t.Fatalf("result should report throttle state: %s", resultText(t, res))
	}
}

func TestAllUnavailable(t *testing.T) {
	b0 := &fakeBackend{name: "brave"}
	b1 := &fakeBackend{name: "tavily"}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)
	s.opts.Tracker.Note429("brave", time.Now(), time.Hour)
	s.opts.Tracker.Note429("tavily", time.Now(), time.Hour)

	res := callSearch(t, s, searchWebInput{Query: "x"})
	if !res.IsError {
		t.Fatal("want IsError")
	}
	text := resultText(t, res)
	for _, want := range []string{"search failed", "brave: throttled until", "tavily: throttled until"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error text %q should contain %q", text, want)
		}
	}
}

func TestFailoverOn429MidRequest(t *testing.T) {
	b0 := &fakeBackend{name: "brave", errs: []error{&backend.ThrottledError{Backend: "brave", RetryAfter: 30 * time.Second}}}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)

	res := callSearch(t, s, searchWebInput{Query: "x"})
	if res.IsError {
		t.Fatalf("want failover success, got error: %s", resultText(t, res))
	}
	if got := backendFromResult(t, res); got != "tavily" {
		t.Fatalf("backend = %s, want tavily", got)
	}
	ok, until := s.opts.Tracker.Throttled("brave", time.Now())
	if !ok {
		t.Fatal("brave should now be throttled")
	}
	if d := until.Sub(time.Now()); d <= 0 || d > 31*time.Second {
		t.Fatalf("throttle window = %s, want ~30s", d)
	}
	// The next search must skip brave entirely.
	res = callSearch(t, s, searchWebInput{Query: "x"})
	if got := backendFromResult(t, res); got != "tavily" {
		t.Fatalf("backend = %s, want tavily", got)
	}
	if b0.calls != 1 {
		t.Fatalf("brave calls = %d, want 1", b0.calls)
	}
}

func TestFailoverOnTransientError(t *testing.T) {
	b0 := &fakeBackend{name: "brave", errs: []error{errors.New("dial: connection refused")}}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)

	res := callSearch(t, s, searchWebInput{Query: "x"})
	if res.IsError {
		t.Fatalf("want failover success, got error: %s", resultText(t, res))
	}
	ok, _ := s.opts.Tracker.Throttled("brave", time.Now())
	if ok {
		t.Fatal("transient error must not throttle brave")
	}
}

func TestBadKeyDisablesBackend(t *testing.T) {
	b0 := &fakeBackend{name: "brave", errs: []error{&backend.BadKeyError{Backend: "brave"}}}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)

	res := callSearch(t, s, searchWebInput{Query: "x"})
	if res.IsError {
		t.Fatalf("want failover success, got error: %s", resultText(t, res))
	}
	res = callSearch(t, s, searchWebInput{Query: "x"})
	if got := backendFromResult(t, res); got != "tavily" {
		t.Fatalf("backend = %s, want tavily", got)
	}
	if b0.calls != 1 {
		t.Fatalf("disabled brave was called %d times, want 1", b0.calls)
	}
}

func TestPinnedBackend(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)

	res := callSearch(t, s, searchWebInput{Query: "x", Backend: "tavily"})
	if got := backendFromResult(t, res); got != "tavily" {
		t.Fatalf("backend = %s, want tavily", got)
	}
	if b0.calls != 0 {
		t.Fatalf("brave should not be called when tavily is pinned, got %d", b0.calls)
	}
}

func TestPinnedThrottledErrors(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	b1 := &fakeBackend{name: "tavily", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)
	s.opts.Tracker.Note429("tavily", time.Now(), time.Hour)

	res := callSearch(t, s, searchWebInput{Query: "x", Backend: "tavily"})
	if !res.IsError {
		t.Fatal("want IsError for pinned throttled backend")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "tavily: throttled until") {
		t.Fatalf("error text %q should name the backend and window", text)
	}
	if b0.calls != 0 {
		t.Fatal("explicit pin must not silently fall back")
	}
}

func TestValidation(t *testing.T) {
	b0 := &fakeBackend{name: "brave", hits: hits()}
	s := newTestState(t, config.RoutingRoundRobin, b0)

	res := callSearch(t, s, searchWebInput{})
	if !res.IsError || !strings.Contains(resultText(t, res), "query is required") {
		t.Fatalf("empty query: %s", resultText(t, res))
	}

	res = callSearch(t, s, searchWebInput{Query: "x", Backend: "nope"})
	if !res.IsError || !strings.Contains(resultText(t, res), "unknown backend") {
		t.Fatalf("bad pin: %s", resultText(t, res))
	}
}

func TestStatus(t *testing.T) {
	b0 := &fakeBackend{name: "brave"}
	b1 := &fakeBackend{name: "tavily"}
	s := newTestState(t, config.RoutingRoundRobin, b0, b1)
	s.opts.Tracker.Note429("brave", time.Now(), time.Hour)

	res, _, err := s.status(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Routing  string `json:"routing"`
		Backends map[string]struct {
			Configured bool   `json:"configured"`
			Throttled  bool   `json:"throttled"`
			Until      string `json:"until"`
		} `json:"backends"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Routing != "round_robin" {
		t.Fatalf("routing = %s", out.Routing)
	}
	if !out.Backends["brave"].Throttled || out.Backends["brave"].Until == "" {
		t.Fatalf("brave status = %+v", out.Backends["brave"])
	}
	if out.Backends["tavily"].Throttled {
		t.Fatalf("tavily should not be throttled: %+v", out.Backends["tavily"])
	}
}

const braveE2EBody = `{"type":"search","web":{"results":[{"title":"B","url":"https://b.example","description":"brave hit"}]}}`

// TestE2EStdio wires a real MCP client to the server over an in-process pipe:
// brave 429s once (Retry-After: 300), the server must fail over to tavily,
// record the cooldown, and report it via the status tool.
func TestE2EStdio(t *testing.T) {
	var braveCalls int
	braveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		braveCalls++
		if braveCalls == 1 {
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"detail":"limit"}`))
			return
		}
		w.Write([]byte(braveE2EBody))
	}))
	t.Cleanup(braveSrv.Close)

	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"T","url":"https://t.example","content":"tavily hit","score":0.5}]}`))
	}))
	t.Cleanup(tavilySrv.Close)

	backends := []backend.Backend{
		backend.NewBrave("k", 5*time.Second, braveSrv.URL),
		backend.NewTavily("k", 5*time.Second, tavilySrv.URL),
	}
	tk, err := throttle.Load(filepath.Join(t.TempDir(), "e2e-state.json"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		Backends: backends,
		Tracker:  tk,
		Routing:  config.RoutingRoundRobin,
		Priority: []string{config.BraveName, config.TavilyName},
		Logger:   log.New(io.Discard, "", 0),
	})

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- srv.Run(context.Background(), &mcp.IOTransport{Reader: serverReader, Writer: serverWriter})
	}()
	t.Cleanup(func() {
		clientWriter.Close()
		clientReader.Close()
		serverReader.Close()
		serverWriter.Close()
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_web",
		Arguments: map[string]any{"query": "go mcp", "max_results": 3},
	})
	if err != nil {
		t.Fatalf("search_web: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", e2eText(t, res))
	}
	if !strings.Contains(e2eText(t, res), `"backend":"tavily"`) {
		t.Fatalf("expected failover to tavily, got: %s", e2eText(t, res))
	}
	if !strings.Contains(e2eText(t, res), "tavily hit") {
		t.Fatalf("expected tavily hit content: %s", e2eText(t, res))
	}
	if braveCalls != 1 {
		t.Fatalf("brave calls = %d, want 1 (throttled by first 429)", braveCalls)
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	text := e2eText(t, res)
	if !strings.Contains(text, `"brave"`) || !strings.Contains(text, `"throttled":true`) {
		t.Fatalf("status should show brave throttled: %s", text)
	}
	if !strings.Contains(text, `"routing":"round_robin"`) {
		t.Fatalf("status should report routing mode: %s", text)
	}
}

func e2eText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "(no text content)"
}
