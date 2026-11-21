package terminal

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type emitRecorder struct {
	mu     sync.Mutex
	frames []string
	times  []time.Time
	signal chan struct{}
}

func newEmitRecorder() *emitRecorder {
	return &emitRecorder{signal: make(chan struct{}, 64)}
}

func (r *emitRecorder) emit(data string) {
	r.mu.Lock()
	r.frames = append(r.frames, data)
	r.times = append(r.times, time.Now())
	r.mu.Unlock()
	r.signal <- struct{}{}
}

func (r *emitRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.frames...)
}

func (r *emitRecorder) waitFrames(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for len(r.snapshot()) < n {
		select {
		case <-r.signal:
		case <-deadline:
			t.Fatalf("timed out waiting for %d frames, got %q", n, r.snapshot())
		}
	}
}

func startCoalescer(in <-chan string, done <-chan struct{}, rec *emitRecorder, window time.Duration) <-chan struct{} {
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		coalesceOutput(in, done, (*outputBatch).push, rec.emit, window)
	}()
	return finished
}

func TestCoalesceOutputFirstChunkAfterIdleIsImmediate(t *testing.T) {
	window := 2 * time.Second
	in := make(chan string, 8)
	rec := newEmitRecorder()
	finished := startCoalescer(in, nil, rec, window)

	sent := time.Now()
	in <- "a"
	rec.waitFrames(t, 1, time.Second)
	rec.mu.Lock()
	elapsed := rec.times[0].Sub(sent)
	rec.mu.Unlock()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("keystroke echo was delayed by %v", elapsed)
	}
	close(in)
	<-finished
}

func TestCoalesceOutputMergesContinuousOutputWithinWindow(t *testing.T) {
	window := 150 * time.Millisecond
	in := make(chan string, 8)
	rec := newEmitRecorder()
	finished := startCoalescer(in, nil, rec, window)

	in <- "first"
	rec.waitFrames(t, 1, time.Second)
	in <- "b"
	time.Sleep(10 * time.Millisecond)
	in <- "c"
	time.Sleep(10 * time.Millisecond)
	in <- "d"
	rec.waitFrames(t, 2, time.Second)
	if got := rec.snapshot(); len(got) != 2 || got[1] != "bcd" {
		t.Fatalf("frames = %q, want [first bcd]", got)
	}
	rec.mu.Lock()
	gap := rec.times[1].Sub(rec.times[0])
	rec.mu.Unlock()
	if gap < 100*time.Millisecond {
		t.Fatalf("continuous output flushed after %v, before the coalescing window", gap)
	}
	close(in)
	<-finished
}

func TestCoalesceOutputFlushesFullBatchImmediately(t *testing.T) {
	window := 2 * time.Second
	in := make(chan string, 8)
	rec := newEmitRecorder()
	finished := startCoalescer(in, nil, rec, window)

	in <- "x"
	rec.waitFrames(t, 1, time.Second)
	big := strings.Repeat("y", flushSize)
	in <- big
	rec.waitFrames(t, 2, 500*time.Millisecond)
	if got := rec.snapshot(); got[1] != big {
		t.Fatalf("full batch was not flushed on its own: len=%d", len(got[1]))
	}
	close(in)
	<-finished
}

func TestCoalesceOutputFlushesPendingOnCloseAndDone(t *testing.T) {
	window := time.Hour
	in := make(chan string, 8)
	rec := newEmitRecorder()
	finished := startCoalescer(in, nil, rec, window)
	in <- "a"
	rec.waitFrames(t, 1, time.Second)
	in <- "tail"
	close(in)
	<-finished
	if got := rec.snapshot(); len(got) != 2 || got[1] != "tail" {
		t.Fatalf("close must flush pending output, got %q", got)
	}

	in = make(chan string, 8)
	done := make(chan struct{})
	rec = newEmitRecorder()
	finished = startCoalescer(in, done, rec, window)
	in <- "a"
	rec.waitFrames(t, 1, time.Second)
	in <- "pending"
	time.Sleep(20 * time.Millisecond)
	close(done)
	<-finished
	if got := rec.snapshot(); len(got) != 2 || got[1] != "pending" {
		t.Fatalf("done must flush pending output, got %q", got)
	}
}

func TestCoalesceOutputPreservesOrderUnderFlood(t *testing.T) {
	window := time.Millisecond
	in := make(chan string, 4)
	rec := newEmitRecorder()
	rec.signal = make(chan struct{}, 1<<16)
	finished := startCoalescer(in, nil, rec, window)
	var want strings.Builder
	for i := 0; i < 5000; i++ {
		chunk := strings.Repeat(string(rune('a'+i%26)), 1+i%97)
		want.WriteString(chunk)
		in <- chunk
	}
	close(in)
	<-finished
	frames := rec.snapshot()
	if got := strings.Join(frames, ""); got != want.String() {
		t.Fatal("coalesced output lost or reordered data")
	}
	if len(frames) >= 5000 {
		t.Fatalf("flood produced %d frames, expected coalescing", len(frames))
	}
}

func TestCoalesceOutputConcurrentWindowsAreIndependent(t *testing.T) {
	slowIn, fastIn := make(chan string, 8), make(chan string, 8)
	slow, fast := newEmitRecorder(), newEmitRecorder()
	slowFinished := startCoalescer(slowIn, nil, slow, time.Hour)
	var fastFinished <-chan struct{}
	slowClosed := false
	t.Cleanup(func() {
		if !slowClosed {
			close(slowIn)
		}
		close(fastIn)
		<-slowFinished
		if fastFinished != nil {
			<-fastFinished
		}
	})
	slowIn <- "first"
	select {
	case <-slow.signal: // consume the first emit before asserting no tail emit
	case <-time.After(time.Second):
		t.Fatal("first slow emit did not complete")
	}
	fastFinished = startCoalescer(fastIn, nil, fast, 0)
	fastIn <- "fast-first"
	fast.waitFrames(t, 1, time.Second)
	slowIn <- "a"
	slowIn <- "b"
	fastIn <- "fast-second"
	fast.waitFrames(t, 2, time.Second)
	// A bounded negative timing assertion, not a fixture-readiness sleep: even
	// while a zero-window pump progresses, the hour-window tail stays buffered.
	deadline := time.NewTimer(50 * time.Millisecond)
	defer deadline.Stop()
	select {
	case <-slow.signal:
		t.Fatalf("another pump flushed the slow window early: %q", slow.snapshot())
	case <-deadline.C:
	}
	if got := slow.snapshot(); len(got) != 1 {
		t.Fatalf("another pump changed the slow window: %q", got)
	}
	close(slowIn)
	<-slowFinished
	slowClosed = true
	if got := slow.snapshot(); len(got) != 2 || got[1] != "ab" {
		t.Fatalf("slow close must flush the combined tail: %q", got)
	}
	if got := fast.snapshot(); len(got) != 2 || got[0] != "fast-first" || got[1] != "fast-second" {
		t.Fatalf("fast window output: %q", got)
	}
}
