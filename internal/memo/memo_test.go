package memo

import (
	"errors"
	"testing"
	"time"
)

func TestMemoRefreshCannotOverwriteConfirmedWrite(t *testing.T) {
	m := NewBackground[int](time.Hour)
	started, finish := make(chan struct{}), make(chan struct{})
	m.Warm("u", func() (int, error) { close(started); <-finish; return 1, nil })
	<-started
	m.Put("u", 2)
	m.mu.Lock()
	e := m.entries["u"]
	m.mu.Unlock()
	e.mu.Lock()
	done := e.loading
	e.mu.Unlock()
	close(finish)
	<-done
	v, err := m.Get("u", func() (int, error) { t.Error("unexpected reload"); return 0, nil })
	if err != nil || v != 2 {
		t.Fatalf("confirmed value replaced: %d, %v", v, err)
	}
}

func TestMemoFirstLoadOvertakenByStaleAnswersWhatItRead(t *testing.T) {
	m := NewBackground[int](time.Hour)
	v, err := m.Get("u", func() (int, error) { m.Stale("u"); return 1, nil })
	if err != nil || v != 1 {
		t.Fatalf("first load answered %d, %v", v, err)
	}
	v, _ = m.Get("u", func() (int, error) { return 2, nil })
	if v != 2 {
		t.Fatalf("the overtaken value was kept: %d", v)
	}
}

// throttledError is a failure naming when to ask again, as a server's 429 with a Retry-After does.
type throttledError struct{ at time.Time }

func (e throttledError) Error() string         { return "asked again later" }
func (e throttledError) RetryAfter() time.Time { return e.at }

func TestMemoBackoffDoublesToCapWithJitter(t *testing.T) {
	e := &memoEntry[int]{}
	for n := 1; n <= 6; n++ {
		d := backoff(e, errors.New("down"))
		base := 30 * time.Second << uint(min(n-1, 4))
		if base > memoRetryCap {
			base = memoRetryCap
		}
		if lo, hi := base*4/5, base*6/5; d < lo || d > hi {
			t.Fatalf("failure %d: delay %s, want %s with a fifth of jitter", n, d, base)
		}
	}
}

func TestMemoRetryAfterHonoredOverBackoff(t *testing.T) {
	m := NewBackground[int](time.Hour)
	at := time.Now().Add(time.Hour)
	probes := 0
	load := func() (int, error) { probes++; return 0, throttledError{at: at} }
	if _, err := m.Get("u", load); err == nil {
		t.Fatal("the failure was swallowed")
	}
	m.mu.Lock()
	e := m.entries["u"]
	delay := e.delay
	m.mu.Unlock()
	if delay < 59*time.Minute || delay > time.Hour {
		t.Fatalf("the server's Retry-After was not the delay: %s", delay)
	}
	// With nothing cached, the stored failure is answered again without a probe while the delay holds.
	if _, err := m.Get("u", load); err == nil {
		t.Fatal("the stored failure was dropped")
	}
	if probes != 1 {
		t.Fatalf("the throttled server was probed %d times", probes)
	}
}

func TestMemoRetryAfterPastFallsBackToBackoff(t *testing.T) {
	e := &memoEntry[int]{}
	if d := backoff(e, throttledError{at: time.Now().Add(-time.Minute)}); d < 30*time.Second*4/5 {
		t.Fatalf("a past Retry-After was honored: %s", d)
	}
}

func TestMemoRetrySetsServerCooldown(t *testing.T) {
	m := NewBackground[int](time.Hour)
	m.Put("u", 1)
	until := time.Now().Add(2 * time.Minute)
	m.Retry("u", until)
	m.mu.Lock()
	e := m.entries["u"]
	after, fetched := e.retryAfter, e.fetched
	m.mu.Unlock()
	if !after.Equal(until) {
		t.Fatalf("cooldown %s, want %s", after, until)
	}
	// The value went stale, but no reload starts before the cooldown ends.
	if fetched.After(until.Add(-m.ttl)) {
		t.Fatalf("the value did not go stale: fetched %s", fetched)
	}
	m.Get("u", func() (int, error) { t.Error("unexpected reload"); return 2, nil })
}

func TestMemoSuccessClearsBackoff(t *testing.T) {
	m := NewBackground[int](time.Hour)
	m.Get("u", func() (int, error) { return 0, throttledError{at: time.Now().Add(50 * time.Millisecond)} })
	m.mu.Lock()
	e := m.entries["u"]
	after := e.retryAfter
	m.mu.Unlock()
	if after.IsZero() {
		t.Fatal("the failure earned no cooldown")
	}
	// While the cooldown holds, the stored failure is answered without a probe.
	if _, err := m.Get("u", func() (int, error) { t.Error("unexpected probe"); return 1, nil }); err == nil {
		t.Fatal("the cooldown did not hold")
	}
	time.Sleep(60 * time.Millisecond)
	if v, _ := m.Get("u", func() (int, error) { return 1, nil }); v != 1 {
		t.Fatalf("the value did not reload: %d", v)
	}
	m.mu.Lock()
	e = m.entries["u"]
	after, failures := e.retryAfter, e.failures
	m.mu.Unlock()
	if !after.IsZero() || failures != 0 {
		t.Fatalf("a load that answered kept the backoff: %s, %d", after, failures)
	}
}
