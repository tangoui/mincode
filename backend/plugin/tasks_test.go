package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func taskTestRuntime(t *testing.T) *processRuntime {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	sdk, err := filepath.Abs("../../packages/plugin-sdk/node/runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal("file://" + filepath.ToSlash(sdk))
	root := t.TempDir()
	source := "#!/usr/bin/env node\nimport { start } from " + string(quoted) + `;start(host => ({
 work: async (p, ctx) => {
   ctx.reportProgress({step:1});
   if (p.waitForCancel) {
     await new Promise(resolve => {
       if (ctx.signal.aborted) resolve();
       else ctx.signal.addEventListener('abort', resolve, {once:true});
     });
   } else {
     await new Promise(resolve => setTimeout(resolve, p.delay ?? 250));
   }
   if (!p.ignore) ctx.signal.throwIfAborted();
   return {ok:true};
 },
 describe: () => host('host.api.describe'),
 big: () => 'x'.repeat(262145),
 exit: () => process.exit(1),
 }));`
	if err := os.WriteFile(filepath.Join(root, "plugin.mjs"), []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(`{"id":"task.test","apiVersion":2,"runtime":{"type":"process","entry":"plugin.mjs"}}`), &manifest); err != nil {
		t.Fatal(err)
	}
	r := newProcessRuntime(root, manifest, nil, nil)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Stop(ctx)
	})
	return r
}
func waitTask(t *testing.T, r *processRuntime, id, state string) taskSnapshot {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		s := r.tasks[id].snapshot
		r.mu.Unlock()
		if s.State == state {
			return s
		}
		if s.State != "running" {
			t.Fatalf("unexpected task: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task wait timed out")
	return taskSnapshot{}
}
func TestTaskPollInterval(t *testing.T) {
	// Assert the cadence without wall-clock thresholds that race the scheduler.
	if taskPollInterval != time.Second {
		t.Fatalf("task polling interval = %s, want 1s", taskPollInterval)
	}
}

func TestTasksRuntimeLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "session-close", "ignored-cancel", "unacknowledged", "exit", "bounds"} {
		t.Run(mode, func(t *testing.T) {
			r := taskTestRuntime(t)
			session, cancel := context.WithCancel(context.Background())
			defer cancel()
			action, payload := "work", `{"delay":350}`
			timeout := 10 * time.Second
			// Keep cooperative work running until cancellation, regardless of polling cadence.
			if mode == "cancel" || mode == "session-close" {
				payload = `{"waitForCancel":true}`
			}
			if mode == "ignored-cancel" {
				payload = `{"ignore":true,"waitForCancel":true}`
			}
			if mode == "unacknowledged" {
				payload = `{"ignore":true,"delay":7000}`
				timeout = time.Second
			}
			if mode == "exit" {
				action = "exit"
			}
			if mode == "bounds" {
				action = "big"
			}
			s, err := r.startTask(session, taskOwner{SessionID: "session", Generation: r.Generation()}, action, payload, timeout)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" || mode == "ignored-cancel" || mode == "session-close" {
				waitTaskProgress(t, r, s.TaskID)
			}
			if mode == "cancel" || mode == "ignored-cancel" {
				r.mu.Lock()
				r.tasks[s.TaskID].snapshot.CancelRequested = true
				r.tasks[s.TaskID].cancel()
				r.mu.Unlock()
			}
			if mode == "session-close" {
				cancel()
			}
			expected := "succeeded"
			if mode == "cancel" || mode == "session-close" {
				expected = "cancelled"
			}
			if mode == "exit" || mode == "bounds" || mode == "unacknowledged" {
				expected = "failed"
			}
			final := waitTask(t, r, s.TaskID, expected)
			if mode == "success" && string(final.Progress) != `{"step":1}` {
				t.Fatalf("missing progress: %+v", final)
			}
			if mode == "ignored-cancel" && !final.CancelRequested {
				t.Fatal("lost cancellation request")
			}
		})
	}
}
func TestTaskStartRejectsHostHandledResults(t *testing.T) {
	ui := newPluginUIHost()
	defer ui.Shutdown()
	record := &pluginRecord{
		id: "host-result", enabled: true, state: pluginStateRunning,
		manifest: &Manifest{Contributes: ManifestContributions{Actions: []ManifestUIAction{{
			ID: "export", Risk: "write", UICallable: true, MaxPayloadBytes: 1024, ResultHandling: "save-text-file",
		}}}},
	}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, uiHost: ui}
	session, err := ui.Begin(record.id, "view", 1, t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.uiTask(context.Background(), "start", taskArgs{
		SessionID: session.SessionID, Nonce: session.Nonce, Action: "export", Payload: `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed for tasks") {
		t.Fatalf("expected host-handled task rejection, got %v", err)
	}
}

func TestTasksUnsupportedAndRetention(t *testing.T) {
	r := newProcessRuntime("", Manifest{}, nil, nil)
	started := time.Now()
	if _, err := r.startTask(context.Background(), taskOwner{SessionID: "s"}, "a", "{}", time.Second); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("unsupported runtime waited")
	}
	r.tasksSupported = true
	r.tasks = map[string]*hostTask{}
	for i := 0; i < taskLimit; i++ {
		r.tasks[string(rune(i))] = &hostTask{finished: time.Now()}
	}
	if _, err := r.startTask(context.Background(), taskOwner{SessionID: "s"}, "a", "{}", time.Second); err == nil {
		t.Fatal("count bound bypassed")
	}
}
func TestTaskUISessionOwnershipAndRevocation(t *testing.T) {
	r := taskTestRuntime(t)
	ui := newPluginUIHost()
	defer ui.Shutdown()
	record := &pluginRecord{id: r.pluginID, enabled: true, state: pluginStateRunning, runtime: r, manifest: &Manifest{}}
	m := &pluginManager{records: map[string]*pluginRecord{record.id: record}, uiHost: ui}
	first, err := ui.Begin(record.id, "view", r.Generation(), t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ui.Begin(record.id, "view", r.Generation(), t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ui.Session(first.SessionID, first.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.startTask(session.ctx, taskOwner{SessionID: session.ID, Generation: session.Generation}, "work", `{"waitForCancel":true}`, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.uiTask(context.Background(), "get", taskArgs{SessionID: second.SessionID, Nonce: second.Nonce, TaskID: s.TaskID}); err == nil {
		t.Fatal("cross-session access")
	}
	if _, err := m.uiTask(context.Background(), "get", taskArgs{SessionID: first.SessionID, Nonce: "wrong", TaskID: s.TaskID}); err == nil {
		t.Fatal("wrong nonce accepted")
	}
	waitTaskProgress(t, r, s.TaskID)
	ui.RevokePlugin(record.id)
	waitTask(t, r, s.TaskID, "cancelled")
	if _, err := m.uiTask(context.Background(), "get", taskArgs{SessionID: first.SessionID, Nonce: first.Nonce, TaskID: s.TaskID}); err == nil {
		t.Fatal("revoked session accepted")
	}
}

func waitTaskProgress(t *testing.T, r *processRuntime, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		s := r.tasks[id].snapshot
		r.mu.Unlock()
		if len(s.Progress) > 0 {
			return
		}
		if s.State != "running" {
			t.Fatalf("task ended before progress: %+v", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task progress timed out")
}

func TestTaskCanceledBeforeDispatchSendsNoRPC(t *testing.T) {
	r := newProcessRuntime("", Manifest{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	task := &hostTask{snapshot: taskSnapshot{TaskID: "queued", State: "running"}, generation: 1, cancel: cancel}
	r.tasks = map[string]*hostTask{"queued": task}
	cancel()
	r.runTask(ctx, task, "work", "{}")
	if calls := r.requestID.Load(); calls != 0 {
		t.Fatalf("revoked task attempted %d RPCs", calls)
	}
	if task.snapshot.State != "cancelled" || !task.snapshot.CancelRequested || task.finished.IsZero() {
		t.Fatalf("unexpected pre-dispatch cancellation: %+v", task)
	}
}

func TestTaskCancellationDuringStartSendsBoundedCleanup(t *testing.T) {
	r := newProcessRuntime("", Manifest{}, nil, nil)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	r.cmd = &exec.Cmd{}
	r.stdin = writer
	r.generation = 1
	r.runContext = runCtx
	r.writes = make(chan rpcWriteRequest)
	r.pending = map[string]chan rpcEnvelope{}
	task := &hostTask{snapshot: taskSnapshot{TaskID: "uncertain", State: "running"}, generation: 1, cancel: cancel}
	r.tasks = map[string]*hostTask{"uncertain": task}
	done := make(chan struct{})
	go func() { r.runTask(ctx, task, "work", "{}"); close(done) }()
	for _, method := range []string{"plugin.task.start", "plugin.task.cancel"} {
		select {
		case request := <-r.writes:
			var message rpcEnvelope
			if err := json.Unmarshal(request.data, &message); err != nil {
				t.Fatal(err)
			}
			if message.Method != method {
				t.Fatalf("got %s, want %s", message.Method, method)
			}
			if err := request.ctx.Err(); err != nil {
				t.Fatalf("cleanup inherited canceled context: %v", err)
			}
			if _, bounded := request.ctx.Deadline(); !bounded {
				t.Fatal("RPC has no deadline")
			}
			request.result <- nil
			if method == "plugin.task.start" {
				cancel()
			} else {
				r.mu.Lock()
				response := r.pending[rpcID(message.ID)]
				delete(r.pending, rpcID(message.ID))
				r.mu.Unlock()
				response <- rpcEnvelope{Result: json.RawMessage(`{"taskId":"uncertain","state":"running","cancelRequested":true}`)}
			}
		case <-time.After(time.Second):
			t.Fatalf("missing %s", method)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish")
	}
	if task.snapshot.State != "failed" || !task.snapshot.OutcomeUnconfirmed || !task.snapshot.CancelRequested || !strings.Contains(task.snapshot.Error, "unconfirmed") {
		t.Fatalf("false cancellation acknowledgement: %+v", task.snapshot)
	}
}

func TestTaskGenerationChangeDoesNotDispatch(t *testing.T) {
	r := taskTestRuntime(t)
	task := &hostTask{snapshot: taskSnapshot{TaskID: "stale", State: "running"}, generation: r.Generation() + 1, cancel: func() {}}
	r.mu.Lock()
	r.tasks = map[string]*hostTask{"stale": task}
	r.mu.Unlock()
	r.runTask(context.Background(), task, "exit", "{}")
	if task.snapshot.State != "failed" || !strings.Contains(task.snapshot.Error, "generation changed") {
		t.Fatalf("unexpected stale task: %+v", task.snapshot)
	}
	if _, err := r.Invoke(context.Background(), "describe", "{}"); err != nil {
		t.Fatalf("stale task reached current process: %v", err)
	}
}

func TestTaskMonitoringFailureCancelsRemote(t *testing.T) {
	for _, phase := range []string{"start", "status", "cancel"} {
		for _, failure := range []string{"rpc", "invalid-state", "invalid-identity"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				r := newProcessRuntime("", Manifest{}, nil, nil)
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				defer writer.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r.cmd = &exec.Cmd{}
				r.stdin = writer
				r.generation = 7
				r.runContext = context.Background()
				r.writes = make(chan rpcWriteRequest)
				r.pending = map[string]chan rpcEnvelope{}
				task := &hostTask{snapshot: taskSnapshot{TaskID: "monitored", State: "running"}, generation: 7, cancel: cancel}
				r.tasks = map[string]*hostTask{"monitored": task}
				done := make(chan struct{})
				go func() { r.runTask(ctx, task, "work", "{}"); close(done) }()
				methods := []string{"start"}
				if phase != "start" {
					methods = append(methods, phase)
				}
				if phase != "cancel" {
					methods = append(methods, "cancel")
				}
				for index, method := range methods {
					select {
					case request := <-r.writes:
						var message rpcEnvelope
						if err := json.Unmarshal(request.data, &message); err != nil {
							t.Fatal(err)
						}
						if message.Method != "plugin.task."+method {
							t.Fatalf("unexpected method %s", message.Method)
						}
						if err := request.validate(); err != nil {
							t.Fatalf("generation validation: %v", err)
						}
						if request.ctx.Err() != nil {
							t.Fatal("cleanup context already cancelled")
						}
						if deadline, ok := request.ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
							t.Fatal("unbounded RPC")
						}
						request.result <- nil
						r.mu.Lock()
						response := r.pending[rpcID(message.ID)]
						delete(r.pending, rpcID(message.ID))
						r.mu.Unlock()
						reply := rpcEnvelope{Result: json.RawMessage(`{"taskId":"monitored","state":"running"}`)}
						if method == phase {
							switch failure {
							case "rpc":
								reply.Error = &rpcError{Code: -32000, Message: "response unavailable"}
							case "invalid-state":
								reply.Result = json.RawMessage(`{"taskId":"monitored","state":"unknown"}`)
							case "invalid-identity":
								reply.Result = json.RawMessage(`{"taskId":"other","state":"succeeded"}`)
							}
						} else if index == len(methods)-1 {
							// Even a successful cleanup reply must not erase prior uncertainty.
							reply.Result = json.RawMessage(`{"taskId":"monitored","state":"cancelled","cancelRequested":true}`)
						}
						if phase == "cancel" && method == "start" {
							cancel()
						}
						response <- reply
					case <-time.After(taskPollInterval + 3*time.Second):
						t.Fatalf("missing %s", method)
					}
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("worker did not finish")
				}
				if task.snapshot.State != "failed" || !task.snapshot.OutcomeUnconfirmed || !task.snapshot.CancelRequested {
					t.Fatalf("uncertainty lost: %+v", task.snapshot)
				}
			})
		}
	}
}

func TestTaskOutcomeUnconfirmedJSON(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		raw, err := json.Marshal(taskSnapshot{OutcomeUnconfirmed: uncertain})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"outcomeUnconfirmed":true`) != uncertain {
			t.Fatalf("unexpected encoding: %s", raw)
		}
		if !uncertain && strings.Contains(string(raw), "outcomeUnconfirmed") {
			t.Fatalf("false field must be omitted: %s", raw)
		}
	}
}

func TestTaskDiscovery(t *testing.T) {
	r := taskTestRuntime(t)
	result, err := r.Invoke(context.Background(), "describe", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "host.storage.get") || !strings.Contains(result, "host.shell.exec") || !strings.Contains(result, "host.secrets.get") || strings.Contains(result, "host.network") {
		t.Fatal(result)
	}
}

func TestTaskPollBackoffSequenceAndReset(t *testing.T) {
	var backoff taskPollBackoff
	want := []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, time.Second, time.Second}
	for i, expected := range want {
		if got := backoff.next(); got != expected {
			t.Fatalf("poll %d delay = %s, want %s", i, got, expected)
		}
	}
	backoff.reset()
	if got := backoff.next(); got != 100*time.Millisecond {
		t.Fatalf("delay after reset = %s, want 100ms", got)
	}
	running := taskSnapshot{TaskID: "t", State: "running", Progress: json.RawMessage(`{"done":1}`)}
	progressed := running
	progressed.Progress = json.RawMessage(`{"done":2}`)
	if taskStatusFingerprint(running) == taskStatusFingerprint(progressed) {
		t.Fatal("progress change not detected")
	}
	cancelFlag := running
	cancelFlag.CancelRequested = true
	if taskStatusFingerprint(running) != taskStatusFingerprint(cancelFlag) {
		t.Fatal("host-side cancel flag must not count as a plugin status change")
	}
}
