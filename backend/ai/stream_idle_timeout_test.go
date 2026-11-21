package ai

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestIdleTimeoutReadCloserStopsStalledStream(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()

	stream := newIdleTimeoutReadCloser(reader, 20*time.Millisecond)
	defer stream.Close()

	startedAt := time.Now()
	buffer := make([]byte, 1)
	_, err := stream.Read(buffer)
	if !errors.Is(err, errAIStreamIdleTimeout) {
		t.Fatalf("expected AI stream idle timeout, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("idle timeout took too long: %s", elapsed)
	}
}

func TestIdleTimeoutReadCloserResetsAfterData(t *testing.T) {
	reader, writer := io.Pipe()
	stream := newIdleTimeoutReadCloser(reader, 80*time.Millisecond)
	defer stream.Close()
	defer writer.Close()

	go func() {
		_, _ = writer.Write([]byte("a"))
		time.Sleep(30 * time.Millisecond)
		_, _ = writer.Write([]byte("b"))
	}()

	buffer := make([]byte, 1)
	if _, err := stream.Read(buffer); err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	if _, err := stream.Read(buffer); err != nil {
		t.Fatalf("activity should reset idle timeout: %v", err)
	}
}

func TestAIStreamErrorMessagePreservesIdleTimeoutIdentity(t *testing.T) {
	wrapped := errors.Join(errors.New("read failed"), errAIStreamIdleTimeout)
	if message := aiStreamErrorMessage(wrapped); message != errAIStreamIdleTimeout.Error() {
		t.Fatalf("expected canonical idle timeout message, got %q", message)
	}
}
