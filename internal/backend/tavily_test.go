package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const tavilyOKBody = `{
  "query": "go mcp",
  "results": [
    {"title": "A", "url": "https://a.example", "content": "snippet a", "score": 0.9},
    {"title": "B", "url": "https://b.example", "content": "snippet b", "score": 0.7}
  ],
  "request_id": "req-1"
}`

func newTavily(t *testing.T, handler http.HandlerFunc) (*Tavily, *map[string]any, **http.Request) {
	t.Helper()
	var body map[string]any
	var got *http.Request
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			got = r
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(tavilyOKBody))
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewTavily("test-key", 5*time.Second, srv.URL), &body, &got
}

func TestTavilyRequestShape(t *testing.T) {
	tv, body, got := newTavily(t, nil)

	if _, err := tv.Search(context.Background(), SearchRequest{
		Query: "go mcp", MaxResults: 10, TimeRange: "week", Country: "us", Language: "en",
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if (*got).Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", (*got).Method)
	}
	if (*got).URL.Path != "/search" {
		t.Fatalf("path = %s", (*got).URL.Path)
	}
	if auth := (*got).Header.Get("Authorization"); auth != "Bearer test-key" {
		t.Fatalf("auth header = %q", auth)
	}
	if ct := (*got).Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if (*body)["query"] != "go mcp" {
		t.Fatalf("query = %v", (*body)["query"])
	}
	if (*body)["max_results"] != float64(10) {
		t.Fatalf("max_results = %v", (*body)["max_results"])
	}
	if (*body)["time_range"] != "week" {
		t.Fatalf("time_range = %v", (*body)["time_range"])
	}
	if (*body)["country"] != "us" {
		t.Fatalf("country = %v", (*body)["country"])
	}
	if (*body)["language"] != "en" {
		t.Fatalf("language = %v", (*body)["language"])
	}
}

func TestTavilyOmitsEmptyOptionals(t *testing.T) {
	tv, body, _ := newTavily(t, nil)
	if _, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"time_range", "country", "language"} {
		if _, ok := (*body)[key]; ok {
			t.Fatalf("body should omit %q when empty", key)
		}
	}
}

func TestTavilyHitsMapping(t *testing.T) {
	tv, _, _ := newTavily(t, nil)
	hits, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Title != "A" || hits[0].URL != "https://a.example" || hits[0].Snippet != "snippet a" || hits[0].Score != 0.9 {
		t.Fatalf("hit 0 = %+v", hits[0])
	}
}

func TestTavily429Header(t *testing.T) {
	tv, _, _ := newTavily(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"detail":"limit"}`))
	})
	_, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var te *ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ThrottledError", err)
	}
	if te.Backend != "tavily" || te.RetryAfter != time.Minute {
		t.Fatalf("throttled = %+v", te)
	}
}

func TestTavily429BodyFallback(t *testing.T) {
	tv, _, _ := newTavily(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"code":"limit","retry_after_seconds":45}`))
	})
	_, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var te *ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ThrottledError", err)
	}
	if te.RetryAfter != 45*time.Second {
		t.Fatalf("retry after = %s, want 45s", te.RetryAfter)
	}
}

func TestTavily401(t *testing.T) {
	tv, _, _ := newTavily(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var be *BadKeyError
	if !errors.As(err, &be) {
		t.Fatalf("error = %v, want *BadKeyError", err)
	}
}

func TestTavily500(t *testing.T) {
	tv, _, _ := newTavily(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`"boom"`))
	})
	_, err := tv.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	if err == nil {
		t.Fatal("want error")
	}
	var te *ThrottledError
	var be *BadKeyError
	if errors.As(err, &te) || errors.As(err, &be) {
		t.Fatalf("error = %v, want plain error", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error %q should mention status", err)
	}
}
