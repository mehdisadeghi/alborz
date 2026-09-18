package alborz

import (
	"testing"
	"time"
)

func TestMemoRefreshCannotOverwriteConfirmedWrite(t *testing.T) {
	m := NewBackgroundMemo[int](time.Hour)
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
	m := NewBackgroundMemo[int](time.Hour)
	v, err := m.Get("u", func() (int, error) { m.Stale("u"); return 1, nil })
	if err != nil || v != 1 {
		t.Fatalf("first load answered %d, %v", v, err)
	}
	v, _ = m.Get("u", func() (int, error) { return 2, nil })
	if v != 2 {
		t.Fatalf("the overtaken value was kept: %d", v)
	}
}
