// Package backend defines the search backend abstraction and the Brave and
// Tavily API clients.
package backend

import (
	"context"
	"fmt"
	"time"
)

// SearchRequest is a backend-agnostic web search query.
type SearchRequest struct {
	Query      string
	MaxResults int
	TimeRange  string // "", "day", "week", "month", "year"
	Country    string
	Language   string // ISO 639-1
}

// SearchHit is a single normalized search result.
type SearchHit struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
	PageAge string  `json:"page_age,omitempty"`
}

// Backend is a single search API provider.
type Backend interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]SearchHit, error)
}

// ThrottledError reports that the provider rejected the request with HTTP 429
// (rate limit or monthly quota exhausted). RetryAfter is zero when the
// provider did not communicate a wait time.
type ThrottledError struct {
	Backend    string
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s: rate limited (retry after %s)", e.Backend, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%s: rate limited or quota exhausted", e.Backend)
}

// BadKeyError reports that the provider rejected the API key (HTTP 401/403).
type BadKeyError struct {
	Backend string
}

func (e *BadKeyError) Error() string {
	return e.Backend + ": API key rejected (401/403); check the configured key"
}
