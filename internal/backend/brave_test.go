package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const braveOKBody = `{
  "type": "search",
  "web": {
    "results": [
      {"title": "A", "url": "https://a.example", "description": "desc a", "age": "2026-01-02"},
      {"title": "B", "url": "https://b.example", "description": "desc b"}
    ]
  }
}`

func newBrave(t *testing.T, handler http.HandlerFunc) *Brave {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(braveOKBody))
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewBrave("test-key", 5*time.Second, srv.URL)
}

func TestBraveRequestShape(t *testing.T) {
	var got *http.Request
	b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write([]byte(`{"type":"search","web":{"results":[]}}`))
	})

	if _, err := b.Search(context.Background(), SearchRequest{
		Query: "go mcp", MaxResults: 10, TimeRange: "week", Country: "us", Language: "en",
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", got.Method)
	}
	if got.URL.Path != "/res/v1/web/search" {
		t.Fatalf("path = %s", got.URL.Path)
	}
	q := got.URL.Query()
	if q.Get("q") != "go mcp" {
		t.Fatalf("q = %q", q.Get("q"))
	}
	if q.Get("count") != "10" {
		t.Fatalf("count = %q", q.Get("count"))
	}
	if q.Get("freshness") != "pw" {
		t.Fatalf("freshness = %q, want pw", q.Get("freshness"))
	}
	if q.Get("country") != "us" {
		t.Fatalf("country = %q", q.Get("country"))
	}
	if q.Get("search_lang") != "en" {
		t.Fatalf("search_lang = %q", q.Get("search_lang"))
	}
	if got.Header.Get("X-Subscription-Token") != "test-key" {
		t.Fatalf("auth header = %q", got.Header.Get("X-Subscription-Token"))
	}
}

func TestBraveFreshnessMapping(t *testing.T) {
	cases := map[string]string{"day": "pd", "week": "pw", "month": "pm", "year": "py"}
	for tr, want := range cases {
		t.Run(tr, func(t *testing.T) {
			var got string
			b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("freshness")
				w.Write([]byte(`{"type":"search","web":{"results":[]}}`))
			})
			if _, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5, TimeRange: tr}); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("freshness = %q, want %q", got, want)
			}
		})
	}

	t.Run("none", func(t *testing.T) {
		var present bool
		b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
			present = r.URL.Query().Has("freshness")
			w.Write([]byte(`{"type":"search","web":{"results":[]}}`))
		})
		if _, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5}); err != nil {
			t.Fatal(err)
		}
		if present {
			t.Fatal("freshness must be omitted when time_range is empty")
		}
	})
}

func TestBraveHitsMapping(t *testing.T) {
	b := newBrave(t, nil)
	hits, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Title != "A" || hits[0].URL != "https://a.example" || hits[0].Snippet != "desc a" || hits[0].PageAge != "2026-01-02" {
		t.Fatalf("hit 0 = %+v", hits[0])
	}
	if hits[1].PageAge != "" {
		t.Fatalf("hit 1 page age = %q, want empty", hits[1].PageAge)
	}
}

func TestBrave429(t *testing.T) {
	b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"detail":"rate limit"}`))
	})
	_, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var te *ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ThrottledError", err)
	}
	if te.Backend != "brave" {
		t.Fatalf("backend = %q", te.Backend)
	}
	if te.RetryAfter != 30*time.Second {
		t.Fatalf("retry after = %s, want 30s", te.RetryAfter)
	}
}

func TestBrave429NoRetryAfter(t *testing.T) {
	b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var te *ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ThrottledError", err)
	}
	if te.RetryAfter != 0 {
		t.Fatalf("retry after = %s, want 0", te.RetryAfter)
	}
}

func TestBrave401(t *testing.T) {
	b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
	var be *BadKeyError
	if !errors.As(err, &be) {
		t.Fatalf("error = %v, want *BadKeyError", err)
	}
}

func TestBrave500(t *testing.T) {
	b := newBrave(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`"boom"`))
	})
	_, err := b.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 5})
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
