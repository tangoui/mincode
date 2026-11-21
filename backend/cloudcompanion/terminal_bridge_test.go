package cloudcompanion

import (
	"testing"
	"time"

	"mincode/app/backend/terminal"
)

const testCloudTerminalSessionID = "cloud-terminal-test-session-0001"

func waitBindingStopped(t *testing.T, binding *cloudTerminalBinding) {
	t.Helper()
	select {
	case <-binding.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cloud terminal binding was not stopped")
	}
}

func TestCloudTerminalOutputQueueExitsWhenBindingStops(t *testing.T) {
	binding := newCloudTerminalBinding(testCloudTerminalSessionID, "device-source-000001")
	m := &cloudTerminalManager{bindings: map[string]*cloudTerminalBinding{}}
	m.setBinding(binding)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		m.runOutputQueue(binding)
	}()

	binding.stop()
	binding.stop()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("output queue goroutine leaked after binding stopped")
	}
	// 停止后仍可能有事件回调并发投递，不能因关闭通道而 panic。
	m.handleEvent(terminal.CloudShellEvent{SessionID: testCloudTerminalSessionID, Data: "late"})
}

func TestCloudTerminalSetBindingStopsReplacedBinding(t *testing.T) {
	m := &cloudTerminalManager{bindings: map[string]*cloudTerminalBinding{}}
	first := newCloudTerminalBinding(testCloudTerminalSessionID, "device-source-000001")
	second := newCloudTerminalBinding(testCloudTerminalSessionID, "device-source-000001")
	m.setBinding(first)
	m.setBinding(second)

	waitBindingStopped(t, first)
	select {
	case <-second.done:
		t.Fatal("current binding must stay active")
	default:
	}
	if m.bindings[testCloudTerminalSessionID] != second {
		t.Fatal("expected the newest binding to be current")
	}
}

func TestCloudTerminalStopAllStopsBindings(t *testing.T) {
	m := &cloudTerminalManager{bindings: map[string]*cloudTerminalBinding{}}
	binding := newCloudTerminalBinding(testCloudTerminalSessionID, "device-source-000001")
	m.setBinding(binding)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		m.runOutputQueue(binding)
	}()

	m.stopAll()

	waitBindingStopped(t, binding)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("output queue goroutine leaked after stopAll")
	}
	if len(m.bindings) != 0 {
		t.Fatalf("expected bindings to be cleared, got %d", len(m.bindings))
	}
}
