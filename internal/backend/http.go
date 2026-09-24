package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// parseRetryAfter extracts a wait time from a 429 response: the Retry-After
// header (delta-seconds or HTTP-date) first, then a retry_after_seconds field
// in the JSON body (Tavily). Returns 0 when nothing usable is present.
func parseRetryAfter(resp *http.Response, body []byte) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		if when, err := http.ParseTime(v); err == nil {
			if d := time.Until(when); d > 0 {
				return d
			}
		}
	}
	if len(body) > 0 {
		var payload struct {
			RetryAfterSeconds float64 `json:"retry_after_seconds"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && payload.RetryAfterSeconds > 0 {
			return time.Duration(payload.RetryAfterSeconds * float64(time.Second))
		}
	}
	return 0
}

// bodySnippet returns a trimmed snippet of a response body for error messages.
func bodySnippet(body []byte) string {
	s := string(body)
	const max = 300
	if len(s) > max {
		s = s[:max] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return fmt.Sprintf("%q", s)
}
