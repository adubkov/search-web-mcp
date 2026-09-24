package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBraveBaseURL = "https://api.search.brave.com"
	braveSearchPath     = "/res/v1/web/search"
)

// timeRangeToFreshness maps our unified time_range values to Brave's
// freshness parameter values.
var timeRangeToFreshness = map[string]string{
	"day":   "pd",
	"week":  "pw",
	"month": "pm",
	"year":  "py",
}

// Brave is a client for the Brave Search API web search endpoint.
type Brave struct {
	key    string
	base   string
	client *http.Client
}

// NewBrave creates a Brave backend. baseURL may be empty to use the default
// production endpoint.
func NewBrave(key string, timeout time.Duration, baseURL string) *Brave {
	if baseURL == "" {
		baseURL = defaultBraveBaseURL
	}
	return &Brave{key: key, base: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: timeout}}
}

func (b *Brave) Name() string { return "brave" }

func (b *Brave) Search(ctx context.Context, req SearchRequest) ([]SearchHit, error) {
	q := url.Values{}
	q.Set("q", req.Query)
	q.Set("count", strconv.Itoa(req.MaxResults))
	if req.Country != "" {
		q.Set("country", req.Country)
	}
	if req.Language != "" {
		q.Set("search_lang", req.Language)
	}
	if fr := timeRangeToFreshness[req.TimeRange]; fr != "" {
		q.Set("freshness", fr)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+braveSearchPath+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("X-Subscription-Token", b.key)

	resp, err := b.client.Do(httpReq)
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
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
					Age         string `json:"age"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("brave: decode response: %w", err)
		}
		hits := make([]SearchHit, 0, len(parsed.Web.Results))
		for _, r := range parsed.Web.Results {
			hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Description, PageAge: r.Age})
		}
		return hits, nil
	case http.StatusTooManyRequests:
		return nil, &ThrottledError{Backend: b.Name(), RetryAfter: parseRetryAfter(resp, nil)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &BadKeyError{Backend: b.Name()}
	default:
		return nil, fmt.Errorf("brave: HTTP %d: %s", resp.StatusCode, bodySnippet(body))
	}
}
