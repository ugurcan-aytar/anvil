package commands

import (
	"sync"
	"testing"
	"time"
)

// TestWatchDebouncerCollapsesBurst: multiple Touch calls within the
// window produce a single flush, with every path deduped.
func TestWatchDebouncerCollapsesBurst(t *testing.T) {
	var gotPaths []string
	var callCount int
	var mu sync.Mutex
	d := newWatchDebouncer(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		callCount++
		gotPaths = append(gotPaths, paths...)
		mu.Unlock()
	})
	defer d.Close()

	d.Touch("/a")
	d.Touch("/a") // dup
	d.Touch("/b")
	d.Touch("/a") // dup
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Errorf("flush called %d times, want 1", callCount)
	}
	if len(gotPaths) != 2 {
		t.Errorf("got %d paths, want 2 deduped; paths=%v", len(gotPaths), gotPaths)
	}
}

// TestWatchDebouncerFiresPerBurst: spaced-out Touches each trigger
// their own flush.
func TestWatchDebouncerFiresPerBurst(t *testing.T) {
	var count int
	var mu sync.Mutex
	d := newWatchDebouncer(30*time.Millisecond, func(paths []string) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	defer d.Close()

	d.Touch("/a")
	time.Sleep(60 * time.Millisecond)
	d.Touch("/b")
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Errorf("flush count = %d, want 2", count)
	}
}

// TestWatchDebouncerFlushDrains: Flush collapses pending + fires
// synchronously, even before the timer would have.
func TestWatchDebouncerFlushDrains(t *testing.T) {
	var got []string
	var mu sync.Mutex
	d := newWatchDebouncer(1*time.Hour, func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	})
	defer d.Close()

	d.Touch("/x")
	d.Touch("/y")
	d.Flush()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Errorf("got %d paths, want 2 (synchronous flush); paths=%v", len(got), got)
	}
}

// TestWatchDebouncerCloseSilencesTouch: after Close, Touch is a no-op.
func TestWatchDebouncerCloseSilencesTouch(t *testing.T) {
	var called bool
	var mu sync.Mutex
	d := newWatchDebouncer(10*time.Millisecond, func(paths []string) {
		mu.Lock()
		called = true
		mu.Unlock()
	})
	d.Close()
	d.Touch("/a")
	time.Sleep(30 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Error("Touch after Close should not schedule a flush")
	}
}
