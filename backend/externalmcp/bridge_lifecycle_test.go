package externalmcp

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func useTestDataDir(t *testing.T) {
	t.Helper()
	original := dataDir
	dir := t.TempDir()
	dataDir = func() string { return dir }
	t.Cleanup(func() { Shutdown(); dataDir = original })
}

func listenerAddress(t *testing.T) string {
	t.Helper()
	bridge.mu.RLock()
	defer bridge.mu.RUnlock()
	if bridge.listener == nil {
		t.Fatal("bridge is not listening")
	}
	return bridge.listener.Addr().String()
}

func assertRefused(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err == nil {
		conn.Close()
		t.Fatalf("replaced listener %s is still accepting connections", address)
	}
}

func TestStartClosesReplacedListener(t *testing.T) {
	useTestDataDir(t)
	bridge.op.Lock()
	if err := bridge.start(nil); err != nil {
		bridge.op.Unlock()
		t.Fatal(err)
	}
	first := listenerAddress(t)
	if err := bridge.start(nil); err != nil {
		bridge.op.Unlock()
		t.Fatal(err)
	}
	bridge.op.Unlock()
	if second := listenerAddress(t); second == first {
		t.Fatal("expected a new listener")
	}
	assertRefused(t, first)
	c, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.URL, listenerAddress(t)) {
		t.Fatalf("rendezvous record %s does not match the live listener", c.URL)
	}
}

func TestConcurrentEnablesLeaveOneConsistentListener(t *testing.T) {
	useTestDataDir(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := command("external_mcp_enable", `{"projects":[]}`); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	c, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.URL, listenerAddress(t)) {
		t.Fatalf("rendezvous record %s does not match the live listener", c.URL)
	}
}

func TestDisableDuringIndexRefreshIsNotUndone(t *testing.T) {
	useTestDataDir(t)
	originalDispatch := dispatch
	t.Cleanup(func() { dispatch = originalDispatch })
	refreshing := make(chan struct{})
	release := make(chan struct{})
	dispatch = func(cmd, _ string) (string, error) {
		if cmd == "project_index_refresh" {
			close(refreshing)
			<-release
		}
		return "{}", nil
	}
	raw, _ := json.Marshal(map[string]any{"projects": []project{{Name: "Sample", Root: t.TempDir()}}})
	enableDone := make(chan error, 1)
	go func() {
		_, err := command("external_mcp_enable", string(raw))
		enableDone <- err
	}()
	<-refreshing
	disableDone := make(chan error, 1)
	go func() {
		_, err := command("external_mcp_disable", "{}")
		disableDone <- err
	}()
	select {
	case <-disableDone:
		close(release)
		t.Fatal("disable must wait for the in-flight enable instead of being undone by it")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-enableDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}
	bridge.mu.RLock()
	enabled := bridge.server != nil
	bridge.mu.RUnlock()
	if enabled {
		t.Fatal("bridge re-enabled after disable")
	}
	if _, err := readConfig(); err == nil {
		t.Fatal("rendezvous record left after disable")
	}
}
