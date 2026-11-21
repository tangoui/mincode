//go:build !windows

package main

import (
	"errors"
	"io"
	"sync"
	"time"
)

// delayedWriter forwards every chunk written to it to target no earlier than
// delay after the Write call, preserving order. The e2e harness uses it to
// simulate a high-latency link on the PTY→channel direction only.
type delayedWriter struct {
	target io.Writer
	delay  time.Duration
	chunks chan delayedChunk
	done   chan struct{}

	mu     sync.Mutex // guards closed and the channel send/close
	closed bool

	errMu sync.Mutex // separate from mu: run() must never wait on a blocked Write
	err   error
}

type delayedChunk struct {
	data []byte
	due  time.Time
}

var errDelayedWriterClosed = errors.New("delayed writer closed")

func newDelayedWriter(target io.Writer, delay time.Duration) *delayedWriter {
	writer := &delayedWriter{
		target: target,
		delay:  delay,
		chunks: make(chan delayedChunk, 1024),
		done:   make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (writer *delayedWriter) run() {
	defer close(writer.done)
	for chunk := range writer.chunks {
		if writer.failure() != nil {
			continue // a failed session drains at once so Write never blocks
		}
		if wait := time.Until(chunk.due); wait > 0 {
			time.Sleep(wait)
		}
		if _, err := writer.target.Write(chunk.data); err != nil {
			writer.errMu.Lock()
			writer.err = err
			writer.errMu.Unlock()
		}
	}
}

func (writer *delayedWriter) failure() error {
	writer.errMu.Lock()
	defer writer.errMu.Unlock()
	return writer.err
}

// Write copies p (the caller may reuse it) and schedules it for delivery.
func (writer *delayedWriter) Write(p []byte) (int, error) {
	if err := writer.failure(); err != nil {
		return 0, err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, errDelayedWriterClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	data := append([]byte(nil), p...)
	// Sending under mu keeps Close from closing the channel mid-send; run()
	// only takes errMu, so a Write blocked on a full channel cannot stall it.
	writer.chunks <- delayedChunk{data: data, due: time.Now().Add(writer.delay)}
	return len(p), nil
}

// Close stops accepting writes and waits until every pending chunk was
// delivered (each still honouring its delay).
func (writer *delayedWriter) Close() error {
	writer.mu.Lock()
	if !writer.closed {
		writer.closed = true
		close(writer.chunks)
	}
	writer.mu.Unlock()
	<-writer.done
	return writer.failure()
}
