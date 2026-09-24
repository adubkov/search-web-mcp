package throttle

import (
	"path/filepath"
	"testing"
	"time"
)

const testBackoff = time.Hour

var testNow = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func newTracker(t *testing.T) *Tracker {
	t.Helper()
	tk, err := Load(filepath.Join(t.TempDir(), "state.json"), testBackoff)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tk
}

func TestNote429BackoffDoubling(t *testing.T) {
	tk := newTracker(t)

	until := tk.Note429("brave", testNow, 0)
	if !until.Equal(testNow.Add(testBackoff)) {
		t.Fatalf("first 429: until = %s, want %s", until, testNow.Add(testBackoff))
	}

	until = tk.Note429("brave", testNow.Add(testBackoff), 0)
	if !until.Equal(testNow.Add(3 * testBackoff)) {
		t.Fatalf("second 429: until = %s, want %s", until, testNow.Add(3*testBackoff))
	}

	// Drive the ladder up to the cap.
	for i := 0; i < 10; i++ {
		until = tk.Note429("brave", until, 0)
	}
	last := tk.Note429("brave", until, 0)
	if d := last.Sub(until); d != MaxBackoff {
		t.Fatalf("backoff cap: step = %s, want %s", d, MaxBackoff)
	}
}

func TestNote429RetryAfterWins(t *testing.T) {
	tk := newTracker(t)

	until := tk.Note429("tavily", testNow, 30*time.Second)
	if !until.Equal(testNow.Add(30 * time.Second)) {
		t.Fatalf("retry-after 429: until = %s, want %s", until, testNow.Add(30*time.Second))
	}

	// A provider-supplied wait must not advance the backoff ladder.
	until = tk.Note429("tavily", testNow.Add(time.Minute), 0)
	if !until.Equal(testNow.Add(time.Minute + testBackoff)) {
		t.Fatalf("ladder should still be at initial: until = %s, want %s", until, testNow.Add(time.Minute+testBackoff))
	}
}

func TestThrottledWindow(t *testing.T) {
	tk := newTracker(t)
	tk.Note429("brave", testNow, time.Hour)

	if ok, until := tk.Throttled("brave", testNow.Add(time.Minute)); !ok || !until.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("inside window: ok=%v until=%s", ok, until)
	}
	if ok, _ := tk.Throttled("brave", testNow.Add(time.Hour+time.Second)); ok {
		t.Fatal("outside window should not be throttled")
	}
	if ok, _ := tk.Throttled("tavily", testNow); ok {
		t.Fatal("untracked backend should not be throttled")
	}
}

func TestNoteSuccessClears(t *testing.T) {
	tk := newTracker(t)
	tk.Note429("brave", testNow, time.Hour)
	tk.NoteSuccess("brave")

	if ok, _ := tk.Throttled("brave", testNow.Add(time.Minute)); ok {
		t.Fatal("success should clear throttle")
	}
	// The backoff ladder resets too.
	until := tk.Note429("brave", testNow, 0)
	if !until.Equal(testNow.Add(testBackoff)) {
		t.Fatalf("ladder reset: until = %s, want %s", until, testNow.Add(testBackoff))
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	tk, err := Load(path, testBackoff)
	if err != nil {
		t.Fatal(err)
	}
	tk.Note429("brave", testNow, time.Hour)
	tk.Note429("tavily", testNow, 30*time.Second)
	for i := 0; i < 3; i++ {
		tk.NextRotation(2)
	}
	if err := tk.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tk2, err := Load(path, testBackoff)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ok, until := tk2.Throttled("brave", testNow.Add(time.Minute)); !ok || !until.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("brave not restored: ok=%v until=%s", ok, until)
	}
	// The 30s tavily window ends at +30s; check inside it.
	if ok, until := tk2.Throttled("tavily", testNow.Add(15*time.Second)); !ok || !until.Equal(testNow.Add(30*time.Second)) {
		t.Fatalf("tavily not restored: ok=%v until=%s", ok, until)
	}
	// Rotation pointer: 3 advances % 2 == 1.
	if got := tk2.NextRotation(2); got != 1 {
		t.Fatalf("rotation = %d, want 1", got)
	}
}

func TestNextRotationWraps(t *testing.T) {
	tk := newTracker(t)
	// The rotation pointer is monotonic: N(n) returns pointer%n and increments.
	// Steps (n, want): (2,0) rot->1, (2,1) rot->2, (2,0) rot->3, (2,1) rot->4, (3,1) rot->5.
	steps := []struct{ n, want int }{{2, 0}, {2, 1}, {2, 0}, {2, 1}, {3, 1}}
	for i, s := range steps {
		if got := tk.NextRotation(s.n); got != s.want {
			t.Fatalf("step %d: got %d, want %d", i, got, s.want)
		}
	}
}
