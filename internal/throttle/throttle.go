// Package throttle tracks per-backend 429 cooldowns. The state (cooldown
// windows, backoff step, round-robin pointer) is persisted to a small JSON
// file so a server restart does not re-hammer a throttled backend.
package throttle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MaxBackoff caps the exponential backoff step for repeated 429s.
const MaxBackoff = 7 * 24 * time.Hour

// Entry is the tracked throttle state for one backend.
type Entry struct {
	ThrottledUntil time.Time
	Backoff        time.Duration // current backoff step; zero = not started
}

type fileBackend struct {
	ThrottledUntil time.Time `json:"throttled_until,omitempty"`
	BackoffSeconds int64     `json:"backoff_seconds,omitempty"`
}

type fileState struct {
	Backends map[string]fileBackend `json:"backends,omitempty"`
	Rotation int                    `json:"rotation,omitempty"`
}

// Tracker records cooldowns per backend name.
type Tracker struct {
	mu       sync.Mutex
	path     string
	initial  time.Duration
	backends map[string]Entry
	rotation int
	now      func() time.Time
}

// Load reads a tracker from path (a missing file is fine). initialBackoff is
// the first cooldown applied on a 429 without a Retry-After value.
func Load(path string, initialBackoff time.Duration) (*Tracker, error) {
	t := &Tracker{
		path:     path,
		initial:  initialBackoff,
		backends: map[string]Entry{},
		now:      time.Now,
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read throttle state %s: %w", path, err)
	}
	var st fileState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse throttle state %s: %w", path, err)
	}
	for name, fb := range st.Backends {
		t.backends[name] = Entry{ThrottledUntil: fb.ThrottledUntil, Backoff: time.Duration(fb.BackoffSeconds) * time.Second}
	}
	t.rotation = st.Rotation
	return t, nil
}

// Save atomically writes the current state to disk.
func (t *Tracker) Save() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saveLocked()
}

func (t *Tracker) saveLocked() error {
	st := fileState{Backends: map[string]fileBackend{}}
	for name, e := range t.backends {
		if e.ThrottledUntil.IsZero() && e.Backoff == 0 {
			continue
		}
		st.Backends[name] = fileBackend{
			ThrottledUntil: e.ThrottledUntil,
			BackoffSeconds: int64(e.Backoff / time.Second),
		}
	}
	st.Rotation = t.rotation
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

// Throttled reports whether name is inside a cooldown window as of now.
func (t *Tracker) Throttled(name string, now time.Time) (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.backends[name]
	if !e.ThrottledUntil.IsZero() && now.Before(e.ThrottledUntil) {
		return true, e.ThrottledUntil
	}
	return false, time.Time{}
}

// Note429 records a provider 429 for name as of now. If retryAfter > 0 it
// becomes the cooldown; otherwise the current backoff step is used and the
// step is doubled (capped at MaxBackoff) for the next 429. Returns the new
// throttled-until time. Caller must call Save to persist.
func (t *Tracker) Note429(name string, now time.Time, retryAfter time.Duration) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.backends[name]
	step := e.Backoff
	if step == 0 {
		step = t.initial
	}
	if retryAfter > 0 {
		e.ThrottledUntil = now.Add(retryAfter)
	} else {
		e.ThrottledUntil = now.Add(step)
		e.Backoff = min(step*2, MaxBackoff)
	}
	t.backends[name] = e
	return e.ThrottledUntil
}

// NoteSuccess clears any tracked state for name (a successful probe after a
// cooldown resets the backoff ladder). Caller must call Save to persist.
func (t *Tracker) NoteSuccess(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.backends, name)
}

// NextRotation returns the next round-robin index in [0, n) and advances the
// pointer. Caller must call Save to persist.
func (t *Tracker) NextRotation(n int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n <= 0 {
		return 0
	}
	idx := t.rotation % n
	t.rotation++
	return idx
}
