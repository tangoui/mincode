package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPluginUISessionCopiesAreCanceled(t *testing.T) {
	for _, operation := range []string{"close", "revoke", "shutdown"} {
		t.Run(operation, func(t *testing.T) {
			host := newPluginUIHost()
			defer host.Shutdown()
			dto, err := host.Begin("visual", "view", 1, t.TempDir(), "ui/index.html", "tango://app", nil)
			if err != nil {
				t.Fatal(err)
			}
			session, err := host.Session(dto.SessionID, dto.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "close":
				host.CloseSession(dto.SessionID)
			case "revoke":
				host.RevokePlugin("visual")
			case "shutdown":
				host.Shutdown()
			}
			if !errors.Is(session.ctx.Err(), context.Canceled) {
				t.Fatalf("copied session remains live: %v", session.ctx.Err())
			}
			if _, err := host.Session(dto.SessionID, dto.Nonce); err == nil {
				t.Fatal("revoked session is available")
			}
		})
	}
}

func TestInvokeUIDoesNotWaitForLifecycleLock(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	record := &pluginRecord{id: "visual", enabled: true, state: pluginStateRunning, runtime: &processRuntime{}, manifest: &Manifest{}}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, uiHost: host}
	dto, err := host.Begin(record.id, "view", 0, t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	record.lifecycleMu.Lock()
	defer record.lifecycleMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := manager.InvokeUI(context.Background(), dto.SessionID, dto.Nonce, "forbidden", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("unexpected admission result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UI admission waited for lifecycle lock")
	}
	manager.CloseUI(dto.SessionID)
	if _, err := manager.InvokeUI(context.Background(), dto.SessionID, dto.Nonce, "forbidden", ""); err == nil {
		t.Fatal("closed session admitted")
	}
}

func TestInvokeUIHonorsExpiredEntryContext(t *testing.T) {
	manager := &pluginManager{}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := manager.InvokeUI(ctx, "", "", "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("entry deadline not preserved: %v", err)
	}
}

type startupExitPlugin struct {
	testLifecyclePlugin
	record *pluginRecord
}

func (plugin *startupExitPlugin) OnStartup(context.Context) error {
	plugin.record.mu.Lock()
	plugin.record.state = pluginStateError
	plugin.record.lastError = "exited after initialize"
	plugin.record.mu.Unlock()
	return nil
}

func TestManagerStartupPreservesExitError(t *testing.T) {
	plugin := &startupExitPlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	plugin.record = record
	err := manager.startRecord(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "exited after initialize") {
		t.Fatalf("startup lost exit error: %v", err)
	}
	if record.state != pluginStateError || record.lastError != "exited after initialize" {
		t.Fatalf("startup overwrote exit: %s, %s", record.state, record.lastError)
	}
}
