package plugin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"mincode/app/backend/core"
	"time"
)

func approvalForRecord(t *testing.T, record *pluginRecord) aiPluginTicket {
	t.Helper()
	record.mu.Lock()
	defer record.mu.Unlock()
	action, err := validateAIActionLocked(host(), record, "run", "{}")
	if err != nil {
		t.Fatal(err)
	}
	return aiPluginTicket{Record: record, ApprovalEpoch: record.approvalEpoch, ID: record.id, Action: "run", Payload: "{}", Contract: aiContractFingerprint(action), Expires: time.Now().Add(time.Minute)}
}

func TestApprovalLifecycleInvalidation(t *testing.T) {
	for _, operation := range []string{"disable-enable", "reload", "remove", "replacement"} {
		t.Run(operation, func(t *testing.T) {
			manager, record := activationManager(t, "")
			if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
				t.Fatal(err)
			}
			ticket := approvalForRecord(t, record)
			switch operation {
			case "disable-enable":
				if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
					t.Fatal(err)
				}
				if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
					t.Fatal(err)
				}
			case "reload":
				if err := manager.Reload(context.Background(), record.id); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if err := manager.Uninstall(context.Background(), record.id, false); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				manager.mu.Lock()
				manager.records[record.id] = &pluginRecord{id: record.id, source: record.source, enabled: true, state: pluginStateStopped, manifest: record.manifest, runtime: record.runtime}
				manager.mu.Unlock()
			}
			generation := record.runtime.Generation()
			_, err := manager.invokeValidated(context.Background(), record.id, "run", "{}", func(current *pluginRecord) error {
				_, err := ticket.validateLocked(current)
				return err
			})
			if err == nil {
				t.Fatal("stale approval accepted")
			}
			if record.runtime.Generation() != generation {
				t.Fatal("stale approval activated runtime")
			}
		})
	}
}

func TestApprovalUpgradeInvalidatesEpoch(t *testing.T) {
	manager, id, source := upgradeFixture(t)
	record, _ := manager.record(id)
	epoch := record.approvalEpoch
	oldRuntime := record.runtime
	if _, err := manager.UpgradeLocal(context.Background(), id, source); err != nil {
		t.Fatal(err)
	}
	if record.approvalEpoch == epoch || record.runtime == oldRuntime {
		t.Fatal("package replacement retained approval identity")
	}
}

func TestApprovalRevalidatedAfterLazyActivation(t *testing.T) {
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	ticket := approvalForRecord(t, record)
	calls := 0
	_, err := manager.invokeValidated(context.Background(), record.id, "run", "{}", func(current *pluginRecord) error {
		calls++
		if calls == 2 {
			current.approvalEpoch++
		}
		_, err := ticket.validateLocked(current)
		return err
	})
	if err == nil || calls != 2 {
		t.Fatalf("post-activation validation: calls=%d err=%v", calls, err)
	}
}

func TestStopLifecycleAdmissionDeadline(t *testing.T) {
	manager, record := activationManager(t, "")
	record.lifecycleMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := manager.stopRecord(ctx, record, true)
	record.lifecycleMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected admission deadline: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("stop admission was not bounded")
	}
	if record.state != pluginStateError {
		t.Fatal("unconfirmed stop presented as success")
	}
}

func TestWarmStartDoesNotEmitUnchangedState(t *testing.T) {
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	var emissions atomic.Int32
	var locked atomic.Bool
	core.SetEmitter(func(name string, _ any) {
		if name != pluginStateChangedEvent {
			return
		}
		emissions.Add(1)
		if !record.lifecycleMu.TryLock() {
			locked.Store(true)
		} else {
			record.lifecycleMu.Unlock()
		}
	})
	defer core.SetEmitter(nil)
	if err := manager.ensureRunning(context.Background(), record.id); err != nil {
		t.Fatal(err)
	}
	if emissions.Load() != 1 || locked.Load() {
		t.Fatalf("start emissions=%d lifecycle locked=%v", emissions.Load(), locked.Load())
	}
	for range 3 {
		if err := manager.ensureRunning(context.Background(), record.id); err != nil {
			t.Fatal(err)
		}
	}
	if emissions.Load() != 1 {
		t.Fatal("warm start emitted unchanged state")
	}
	if err := manager.stopRecord(context.Background(), record, false); err != nil {
		t.Fatal(err)
	}
	if emissions.Load() != 2 || locked.Load() {
		t.Fatalf("stop emissions=%d lifecycle locked=%v", emissions.Load(), locked.Load())
	}
}

func TestRecordExitRejectsStaleRuntimeAndGeneration(t *testing.T) {
	manager, record := activationManager(t, "")
	record.state = pluginStateRunning
	oldRuntime := record.runtime
	replacement := manager.newRecordRuntime(record, record.root, *record.manifest)
	record.runtime = replacement
	manager.recordRuntimeExited(record, oldRuntime, oldRuntime.Generation(), errors.New("old exit"))
	manager.recordRuntimeExited(record, replacement, replacement.Generation()+1, errors.New("wrong generation"))
	if record.state != pluginStateRunning {
		t.Fatal("stale callback poisoned replacement")
	}
	manager.recordRuntimeExited(record, replacement, replacement.Generation(), errors.New("current exit"))
	if record.state != pluginStateError || record.lastError != "current exit" {
		t.Fatal("current exit was ignored")
	}
}
