package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTavilyBaseURL = "https://api.tavily.com"
	tavilySearchPath     = "/search"
)

// Tavily is a client for the Tavily search API.
type Tavily struct {
	key    string
	base   string
	client *http.Client
}

// NewTavily creates a Tavily backend. baseURL may be empty to use the default
// production endpoint.
func NewTavily(key string, timeout time.Duration, baseURL string) *Tavily {
	if baseURL == "" {
		baseURL = defaultTavilyBaseURL
	}
	return &Tavily{key: key, base: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: timeout}}
}

func (t *Tavily) Name() string { return "tavily" }

func (t *Tavily) Search(ctx context.Context, req SearchRequest) ([]SearchHit, error) {
	payload := map[string]any{
		"query":       req.Query,
		"max_results": req.MaxResults,
	}
	if req.TimeRange != "" {
		payload["time_range"] = req.TimeRange
	}
	if req.Country != "" {
		payload["country"] = req.Country
	}
	if req.Language != "" {
		payload["language"] = req.Language
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+tavilySearchPath, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+t.key)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var parsed struct {
			Results []struct {
				Title   string  `json:"title"`
				URL     string  `json:"url"`
				Content string  `json:"content"`
				Score   float64 `json:"score"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("tavily: decode response: %w", err)
		}
		hits := make([]SearchHit, 0, len(parsed.Results))
		for _, r := range parsed.Results {
			hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Content, Score: r.Score})
		}
		return hits, nil
	case http.StatusTooManyRequests:
		retryAfter := parseRetryAfter(resp, body)
		return nil, &ThrottledError{Backend: t.Name(), RetryAfter: retryAfter}
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &BadKeyError{Backend: t.Name()}
	default:
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, bodySnippet(body))
	}
}
