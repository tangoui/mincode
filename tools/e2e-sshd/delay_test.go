//go:build !windows

package main

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

type recordingWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	first time.Time
	done  chan struct{}
	want  int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.data.Len() == 0 && len(p) > 0 {
		w.first = time.Now()
	}
	w.data.Write(p)
	if w.data.Len() >= w.want {
		select {
		case <-w.done:
		default:
			close(w.done)
		}
	}
	return len(p), nil
}

func TestDelayedWriterKeepsOrderAndDelay(t *testing.T) {
	target := &recordingWriter{done: make(chan struct{}), want: 3}
	const delay = 100 * time.Millisecond
	writer := newDelayedWriter(target, delay)
	defer writer.Close()
	start := time.Now()
	for _, chunk := range []string{"a", "b", "c"} {
		if n, err := writer.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	select {
	case <-target.done:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed output never arrived")
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if got := target.data.String(); got != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
	if elapsed := target.first.Sub(start); elapsed < delay {
		t.Fatalf("first byte after %v, want >= %v", elapsed, delay)
	}
}

func TestDelayedWriterCopiesCallerBuffer(t *testing.T) {
	target := &recordingWriter{done: make(chan struct{}), want: 2}
	writer := newDelayedWriter(target, 20*time.Millisecond)
	defer writer.Close()
	buffer := []byte("x")
	_, _ = writer.Write(buffer)
	buffer[0] = 'y'
	_, _ = writer.Write(buffer)
	select {
	case <-target.done:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed output never arrived")
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if got := target.data.String(); got != "xy" {
		t.Fatalf("got %q, want %q", got, "xy")
	}
}

func TestDelayedWriterCloseFlushesPending(t *testing.T) {
	target := &recordingWriter{done: make(chan struct{}), want: 1}
	writer := newDelayedWriter(target, 30*time.Millisecond)
	_, _ = writer.Write([]byte("z"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if got := target.data.String(); got != "z" {
		t.Fatalf("got %q after Close, want %q", got, "z")
	}
}

var _ io.WriteCloser = (*delayedWriter)(nil)

type failingWriter struct{ writes int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

func TestDelayedWriterFailureDrainsWithoutWaiting(t *testing.T) {
	target := &failingWriter{}
	writer := newDelayedWriter(target, 200*time.Millisecond)
	start := time.Now()
	_, _ = writer.Write([]byte("a")) // due at 200 ms, fails then
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 5; i++ {
		_, _ = writer.Write([]byte("b")) // due at ~300 ms, queued before the failure surfaces
	}
	if err := writer.Close(); err == nil {
		t.Fatal("Close should report the target failure")
	}
	// After the first chunk fails the rest drain at once instead of waiting until ~300 ms.
	if elapsed := time.Since(start); elapsed > 270*time.Millisecond {
		t.Fatalf("Close returned after %v, pending chunks waited out their delay after the failure", elapsed)
	}
	if target.writes != 1 {
		t.Fatalf("target written %d times after failing, want 1", target.writes)
	}
}
