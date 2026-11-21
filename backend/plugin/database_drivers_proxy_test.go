package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/db"
	"mincode/app/plugins/api"
)

const (
	databaseDriverTestPlugin = "sample-driver"
	databaseDriverTestDriver = "sample"
	databaseDriverTestConn   = "7d0f3c1e-1111-4a2b-9c3d-000000000001"
	databaseDriverTestSecret = "s3cr3t-value"
)

// ---------------------------------------------------------------------------
// Fake driver process.

type databaseDriverTranscriptLine struct {
	Dir     string          `json:"dir"`
	Message json.RawMessage `json:"message"`
}

// TestDatabaseDriverHelperProcess is the fake driver plugin. Its mode comes
// from the "mode" file next to the executable:
//   - replay: answers exactly as transcript.jsonl (ids remapped), replying a
//     -32099 error to any host message that differs from the transcript.
//   - script: negotiates the driver; execute "crash" exits, "oversize"
//     returns more rows than requested, anything else echoes an error that
//     contains the connect password.
//   - nonegotiate: ignores the databaseDrivers offer.
//   - v11: like script, but negotiates protocol 1.1 page limits (5000 rows /
//     2 MiB); execute "large" requires a 1000-row / 2 MiB request and answers
//     one page of 1000 wide rows (about 1.5 MiB).
func TestDatabaseDriverHelperProcess(t *testing.T) {
	if os.Getenv("MINCODE_PLUGIN_ID") != databaseDriverTestPlugin {
		return
	}
	executable, _ := os.Executable()
	dir := filepath.Dir(executable)
	mode, _ := os.ReadFile(filepath.Join(dir, "mode"))
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var writeMu sync.Mutex
	encoder := json.NewEncoder(os.Stdout)
	write := func(value any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = encoder.Encode(value)
	}
	switch strings.TrimSpace(string(mode)) {
	case "replay":
		databaseDriverReplay(scanner, dir, write)
	default:
		databaseDriverScript(scanner, strings.TrimSpace(string(mode)), write)
	}
	os.Exit(0)
}

func databaseDriverReplay(scanner *bufio.Scanner, dir string, write func(any)) {
	data, _ := os.ReadFile(filepath.Join(dir, "transcript.jsonl"))
	var lines []databaseDriverTranscriptLine
	for index, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if index == 0 {
			continue // header
		}
		var line databaseDriverTranscriptLine
		_ = json.Unmarshal([]byte(raw), &line)
		lines = append(lines, line)
	}
	ids := map[string]json.RawMessage{} // transcript id -> wire id
	next := 0
	emitPlugin := func() {
		for next < len(lines) && lines[next].Dir == "plugin" {
			var message rpcEnvelope
			_ = json.Unmarshal(lines[next].Message, &message)
			if wire, ok := ids[string(message.ID)]; ok {
				message.ID = wire
			}
			message.JSONRPC = "2.0"
			write(message)
			next++
		}
	}
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method == "" {
			continue
		}
		switch request.Method {
		case "plugin.ping":
			write(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`true`)})
			continue
		case "plugin.shutdown":
			write(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`true`)})
			return
		}
		if next >= len(lines) || lines[next].Dir != "host" {
			write(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32099, Message: "transcript exhausted at " + request.Method}})
			continue
		}
		var expected rpcEnvelope
		_ = json.Unmarshal(lines[next].Message, &expected)
		if problem := databaseDriverMessageMismatch(expected, request); problem != "" {
			write(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32099, Message: "transcript mismatch: " + problem}})
			continue
		}
		ids[string(expected.ID)] = request.ID
		next++
		emitPlugin()
	}
}

// databaseDriverMessageMismatch compares a host request semantically. The
// initialize offer (platform, other features) is pinned by T0's
// negotiation test; here only the databaseDrivers offer matters.
func databaseDriverMessageMismatch(expected, actual rpcEnvelope) string {
	if expected.Method != actual.Method {
		return fmt.Sprintf("method %s, want %s", actual.Method, expected.Method)
	}
	if expected.Method == "plugin.initialize" {
		var params struct {
			Features map[string]int `json:"features"`
		}
		if json.Unmarshal(actual.Params, &params) != nil || params.Features["databaseDrivers"] != 1 {
			return "initialize does not offer databaseDrivers 1"
		}
		return ""
	}
	var want, got any
	_ = json.Unmarshal(expected.Params, &want)
	_ = json.Unmarshal(actual.Params, &got)
	if !reflect.DeepEqual(want, got) {
		return fmt.Sprintf("%s params %s, want %s", actual.Method, actual.Params, expected.Params)
	}
	return ""
}

// databaseDriverV11LargeRows rows of databaseDriverV11WideRow bytes make a
// page of ~1.5 MiB: at most 1000 rows, but above the 1 MiB default line cap.
const (
	databaseDriverV11LargeRows = 1000
	databaseDriverV11WideRow   = 1500
)

func databaseDriverScript(scanner *bufio.Scanner, mode string, write func(any)) {
	password := ""
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method == "" {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize":
			if mode == "nonegotiate" {
				response.Result = json.RawMessage(`{"features":{"tasks":1}}`)
			} else if mode == "v11" {
				response.Result = json.RawMessage(`{"features":{"databaseDrivers":1},"databaseDrivers":{"protocol":1,"minor":1,"drivers":["sample"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}}`)
			} else {
				response.Result = json.RawMessage(`{"features":{"databaseDrivers":1},"databaseDrivers":{"protocol":1,"drivers":["sample"]}}`)
			}
		case "plugin.ping", "plugin.db.close", "plugin.db.closeCursor":
			response.Result = json.RawMessage(`true`)
		case "plugin.shutdown":
			write(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`true`)})
			return
		case "plugin.db.connect":
			var params struct {
				Config map[string]any `json:"config"`
			}
			_ = json.Unmarshal(request.Params, &params)
			password, _ = params.Config["password"].(string)
			// A careless driver logs its params; the redactor must hide the secret.
			fmt.Fprintf(os.Stderr, "connecting with password=%s\n", password)
			response.Result = json.RawMessage(`{"handle":"h1","info":{"serverVersion":"1.0"}}`)
		case "plugin.db.execute":
			var params struct {
				Statement string `json:"statement"`
				MaxRows   int    `json:"maxRows"`
				MaxBytes  int    `json:"maxBytes"`
			}
			_ = json.Unmarshal(request.Params, &params)
			switch params.Statement {
			case "large":
				// Protocol 1.1: 5000 rows were negotiated, but the host asks
				// for 1000-row pages and uses the 2 MiB budget for wide rows.
				if params.MaxRows != 1000 || params.MaxBytes != 2<<20 {
					response.Error = &rpcError{Code: -32000, Message: fmt.Sprintf("page budget %d/%d", params.MaxRows, params.MaxBytes), Data: map[string]any{"kind": "invalid"}}
					break
				}
				rows := make([][]any, databaseDriverV11LargeRows)
				for index := range rows {
					rows[index] = []any{strings.Repeat("x", databaseDriverV11WideRow)}
				}
				result, _ := json.Marshal(map[string]any{"columns": []any{map[string]any{"name": "s"}}, "rows": rows, "done": true})
				response.Result = result
			case "crash":
				os.Exit(3)
			case "oversize":
				rows := make([][]any, params.MaxRows+1)
				for index := range rows {
					rows[index] = []any{index}
				}
				result, _ := json.Marshal(map[string]any{"columns": []any{map[string]any{"name": "n"}}, "rows": rows, "done": true})
				response.Result = result
			default:
				response.Error = &rpcError{Code: -32000, Message: "auth failed for password " + password, Data: map[string]any{"kind": "connection"}}
			}
		case "plugin.db.snapshotWrite":
			var params struct {
				Steps []struct {
					Write *struct {
						Statement string `json:"statement"`
						Args      []any  `json:"args"`
					} `json:"write"`
				} `json:"steps"`
			}
			_ = json.Unmarshal(request.Params, &params)
			switch {
			case len(params.Steps) == 0 || params.Steps[0].Write == nil || params.Steps[0].Write.Args == nil:
				response.Error = &rpcError{Code: -32602, Message: "bad params", Data: map[string]any{"kind": "invalid"}}
			case params.Steps[0].Write.Statement == "limit":
				response.Error = &rpcError{Code: -32000, Message: "snapshot results exceed the reply limit", Data: map[string]any{"kind": "limit"}}
			case params.Steps[0].Write.Statement == "committed":
				response.Error = &rpcError{Code: -32000, Message: "snapshotWrite committed, but its result cannot be sent", Data: map[string]any{"kind": "committed"}}
			case params.Steps[0].Write.Statement == "garbled":
				response.Result = json.RawMessage(`{"steps":7}`)
			case params.Steps[0].Write.Statement == "short":
				response.Result = json.RawMessage(`{"steps":[],"exceeded":false,"exceededStep":0}`)
			default:
				response.Result = json.RawMessage(`{"steps":[{"before":{"columns":[{"name":"id","type":"INT"}],"rows":[[{"t":"int","v":"7"}]]},"affected":1}],"exceeded":false,"exceededStep":0}`)
			}
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		write(response)
	}
}

// ---------------------------------------------------------------------------
// Fixture.

type databaseDriverFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
}

func databaseDriverTestContribution() DatabaseDriverContribution {
	driver := DatabaseDriverContribution{
		ID: databaseDriverTestDriver, Title: "Sample", Category: "analytics", SQLDialect: "clickhouse",
		Connection: DatabaseDriverConnection{
			Fields:   []string{"host", "port", "database", "username", "password"},
			Required: []string{"host"},
			Options: map[string]DatabaseDriverOption{
				"compression": {Type: databaseDriverOptionString, Enum: []any{"lz4", "none"}, Default: "lz4"},
			},
		},
		Features: []string{"cancel", "metadata.tables"},
	}
	driver.SpecFingerprint = databaseDriverSpecFingerprint(driver)
	return driver
}

func enableDatabaseDriverPointForTest(t *testing.T) {
	t.Helper()
	previous := databaseDriverPointEnabled
	databaseDriverPointEnabled = func() bool { return true }
	t.Cleanup(func() { databaseDriverPointEnabled = previous })
}

// newDatabaseDriverFixture registers a stopped local process plugin that
// contributes the sample driver. mode selects the helper behaviour ("" for
// a record without a runnable process).
func newDatabaseDriverFixture(t *testing.T, mode string) *databaseDriverFixture {
	t.Helper()
	useTemporaryPluginData(t)
	enableDatabaseDriverPointForTest(t)
	root := t.TempDir()
	manifest := Manifest{
		ID: databaseDriverTestPlugin, Name: "Sample Driver", Version: "0.1.0", APIVersion: api.ManifestAPIVersionV2,
		Capabilities: []api.Capability{api.CapabilityDatabaseDriver, api.CapabilityNetwork},
	}
	manifest.contributions = map[string]*resolvedContribution{
		databaseDriverPointKey: {Entries: []any{databaseDriverTestContribution()}},
	}
	record := &pluginRecord{
		id: databaseDriverTestPlugin, root: root, source: pluginSourceLocal, enabled: true, state: pluginStateStopped,
		capabilities: append([]api.Capability(nil), manifest.Capabilities...),
	}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}}
	if mode != "" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		entry := "driver-helper"
		if runtime.GOOS == "windows" {
			entry += ".exe"
		}
		if err := os.WriteFile(filepath.Join(root, entry), data, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "mode"), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
		manifest.Runtime = ProcessRuntimeManifest{Type: "process", Entry: entry, Args: []string{"-test.run=TestDatabaseDriverHelperProcess"}}
		record.manifest = &manifest
		record.runtime = manager.newRecordRuntime(record, root, manifest)
		t.Cleanup(func() {
			if record.runtime != nil {
				_ = record.runtime.forceStop(context.Background())
			}
		})
	} else {
		manifest.Runtime = ProcessRuntimeManifest{Type: "process", Entry: "missing"}
		record.manifest = &manifest
		record.state = pluginStateRunning
	}
	t.Cleanup(func() {
		for _, conn := range detachDatabaseDriverSessions(databaseDriverTestPlugin) {
			conn.discard()
		}
	})
	return &databaseDriverFixture{manager: manager, record: record, root: root}
}

func (fixture *databaseDriverFixture) grant(t *testing.T) {
	t.Helper()
	if err := fixture.manager.grantDatabaseDriver(databaseDriverTestPlugin, databaseDriverTestDriver, databaseDriverTestConn); err != nil {
		t.Fatal(err)
	}
}

func databaseDriverTestRequest() db.ExternalConnectRequest {
	text := func(value string) *string { return &value }
	return db.ExternalConnectRequest{
		Ref:          db.ExternalDriverRef{PluginID: databaseDriverTestPlugin, DriverID: databaseDriverTestDriver},
		ConnectionID: databaseDriverTestConn,
		Config: db.DbConnectionConfig{
			DbType: "plugin", Host: "127.0.0.1", Port: 9000,
			Database: text("default"), Username: text("reader"), Password: text(databaseDriverTestSecret),
			// Not declared by the driver: never sent.
			Token: text("undeclared-token"), Org: text("undeclared-org"),
		},
		Options: map[string]any{"compression": "lz4"},
	}
}

func expectDatabaseDriverCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), code+":") {
		t.Fatalf("expected %s error, got %v", code, err)
	}
}

func collectDatabaseDriverRows(rows *[][]any) func(db.ExternalPage) error {
	return func(page db.ExternalPage) error {
		*rows = append(*rows, page.Rows...)
		return nil
	}
}

// ---------------------------------------------------------------------------
// Golden transcript.

func TestDatabaseDriverGoldenTranscriptReplay(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "replay")
	transcript, err := os.ReadFile(filepath.Join("..", "..", "tools", "verify", "fixtures", "database-driver-protocol", "basic-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "transcript.jsonl"), transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Without the credential-release grant nothing starts and nothing is sent.
	_, err = fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	expectDatabaseDriverCode(t, err, databaseDriverErrUntrusted)
	if fixture.record.state != pluginStateStopped {
		t.Fatalf("untrusted connect started the plugin: %s", fixture.record.state)
	}

	fixture.grant(t)
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	info := conn.Info()
	if info.PluginID != databaseDriverTestPlugin || info.DriverID != databaseDriverTestDriver || info.ServerVersion != "24.8.1" ||
		info.DefaultDatabase != "default" || info.Dialect != "clickhouse" {
		t.Fatalf("info = %+v", info)
	}
	if got := redactPluginMessage(databaseDriverTestPlugin, "pw="+databaseDriverTestSecret); got != "pw=[redacted]" {
		t.Fatalf("live session secret not redacted: %q", got)
	}

	var rows [][]any
	summary, err := conn.Execute(ctx, db.ExternalExecute{
		RequestID: "q1", Statement: "SELECT id, name, payload FROM users ORDER BY id", Database: "default",
		Mode: db.ExternalExecuteAuto, MaxRows: 2, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
	}, collectDatabaseDriverRows(&rows))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 3 || len(rows) != 3 || len(summary.Columns) != 3 || rows[0][0] != "9007199254740993" {
		t.Fatalf("summary %+v rows %v", summary, rows)
	}

	metadata, err := conn.Metadata(ctx, db.ExternalMetadataRequest{Kind: db.ExternalMetadataNavigation})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `"users"`) {
		t.Fatalf("metadata = %s", metadata)
	}

	// q2 blocks in the plugin until the cancel arrives.
	executeDone := make(chan error, 1)
	go func() {
		_, err := conn.Execute(ctx, db.ExternalExecute{
			RequestID: "q2", Statement: "SELECT sleep(3)", Mode: db.ExternalExecuteQuery, MaxRows: 5000, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
		}, func(db.ExternalPage) error { return nil })
		executeDone <- err
	}()
	waitDatabaseDriverPending(t, fixture.record.runtime, "plugin.db.execute")
	state, err := conn.Cancel(ctx, "q2")
	if err != nil || state != db.CancelConfirmed {
		t.Fatalf("cancel = %s %v", state, err)
	}
	expectDatabaseDriverCode(t, <-executeDone, databaseDriverErrCancelled)

	_, err = conn.Execute(ctx, db.ExternalExecute{RequestID: "q3", Statement: "SELEC 1", MaxRows: 5000, MaxBytes: 16 << 20, Timeout: 120 * time.Second},
		func(db.ExternalPage) error { return nil })
	if err == nil || err.Error() != "driver_error: Syntax error at position 1" {
		t.Fatalf("q3 error = %v", err)
	}

	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := redactPluginMessage(databaseDriverTestPlugin, databaseDriverTestSecret); got != databaseDriverTestSecret {
		t.Fatal("secrets outlived the session")
	}
	if databaseDriverSessionCount(databaseDriverTestPlugin) != 0 {
		t.Fatal("closed session still registered")
	}
	if _, err := conn.Execute(ctx, db.ExternalExecute{RequestID: "q4", Statement: "SELECT 1"}, func(db.ExternalPage) error { return nil }); err == nil {
		t.Fatal("closed session still executes")
	}
}

// waitDatabaseDriverPending waits until a host request is in flight; the
// transcript orders execute before cancel.
func waitDatabaseDriverPending(t *testing.T, runtimeHost *processRuntime, _ string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runtimeHost.mu.Lock()
		pending := len(runtimeHost.pending)
		runtimeHost.mu.Unlock()
		if pending > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("request never reached the plugin")
}

func TestDatabaseDriverNegotiationMissing(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "nonegotiate")
	fixture.grant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err == nil || err.Error() != "unavailable: plugin does not implement database driver sample" {
		t.Fatalf("error = %v", err)
	}
	if reason := fixture.manager.databaseDriverUnavailableReason(databaseDriverTestPlugin, databaseDriverTestDriver); reason != "protocol" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDatabaseDriverRedactsSecretsAndRejectsOversizePages(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "script")
	fixture.grant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Execute(ctx, db.ExternalExecute{RequestID: "r1", Statement: "SELECT 1", MaxRows: 10, MaxBytes: 1 << 20, Timeout: 5 * time.Second},
		func(db.ExternalPage) error { return nil })
	expectDatabaseDriverCode(t, err, databaseDriverErrUnavailable)
	if strings.Contains(err.Error(), databaseDriverTestSecret) || !strings.Contains(err.Error(), databaseDriverRedactedMarker) {
		t.Fatalf("plugin error leaked the secret: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		fixture.record.mu.Lock()
		logs := fmt.Sprint(fixture.record.logs)
		fixture.record.mu.Unlock()
		if strings.Contains(logs, "connecting with password=") {
			if strings.Contains(logs, databaseDriverTestSecret) {
				t.Fatalf("stderr leaked the secret: %s", logs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stderr line never logged: %s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, err = conn.Execute(ctx, db.ExternalExecute{RequestID: "r2", Statement: "oversize", MaxRows: 2, MaxBytes: 1 << 20, Timeout: 5 * time.Second},
		func(db.ExternalPage) error { return nil })
	expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
	_ = conn.Close(ctx)
}

func TestDatabaseDriverSnapshotWrite(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "script")
	fixture.grant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	sw, ok := conn.(db.ExternalSnapshotConnection)
	if !ok {
		t.Fatal("connection does not send plugin.db.snapshotWrite")
	}
	step := func(write string) []db.ExternalSnapshotStep {
		return []db.ExternalSnapshotStep{{Snapshot: &db.ExternalSnapshotStatement{Statement: "SELECT id FROM t", Args: []any{int64(7)}},
			Write: &db.ExternalSnapshotStatement{Statement: write}, MaxRows: 5}}
	}
	res, err := sw.SnapshotWrite(ctx, db.ExternalSnapshotRequest{RequestID: "s1", Database: "main", Steps: step("DELETE FROM t")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 1 || res.Steps[0].Affected != 1 || res.Steps[0].Before == nil || string(res.Steps[0].Before.Rows[0][0]) != `{"t":"int","v":"7"}` {
		t.Fatalf("result = %+v", res)
	}
	_, err = sw.SnapshotWrite(ctx, db.ExternalSnapshotRequest{RequestID: "s2", Steps: step("limit")})
	if err == nil || !strings.HasPrefix(err.Error(), "limit:") {
		t.Fatalf("limit error = %v", err)
	}
	// Shape errors in a successful reply are left to backend/db, which knows
	// the driver committed; an undecodable reply says so through the sentinel.
	res, err = sw.SnapshotWrite(ctx, db.ExternalSnapshotRequest{RequestID: "s3", Steps: step("short")})
	if err != nil || len(res.Steps) != 0 {
		t.Fatalf("short reply = %+v %v", res, err)
	}
	_, err = sw.SnapshotWrite(ctx, db.ExternalSnapshotRequest{RequestID: "s4", Steps: step("garbled")})
	if !errors.Is(err, db.ErrExternalSnapshotReply) {
		t.Fatalf("garbled reply error = %v", err)
	}
	// The SDK answers "committed" when the driver succeeded but its reply
	// could not be sent: applied without a backup, never resubmitted.
	_, err = sw.SnapshotWrite(ctx, db.ExternalSnapshotRequest{RequestID: "s5", Steps: step("committed")})
	if !errors.Is(err, db.ErrExternalSnapshotReply) {
		t.Fatalf("committed error = %v", err)
	}
}

func TestDatabaseDriverCrashClosesSessions(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "script")
	fixture.grant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if pid, ok := fixture.manager.databaseDriverProcessID(databaseDriverTestPlugin); !ok || pid <= 0 {
		t.Fatalf("pid = %d %v", pid, ok)
	}
	_, err = conn.Execute(ctx, db.ExternalExecute{RequestID: "r1", Statement: "crash", MaxRows: 10, MaxBytes: 1 << 20, Timeout: 5 * time.Second},
		func(db.ExternalPage) error { return nil })
	if err == nil {
		t.Fatal("crash not reported")
	}
	if !strings.HasPrefix(err.Error(), databaseDriverErrDriver+":") && !strings.HasPrefix(err.Error(), databaseDriverErrUnavailable+":") {
		t.Fatalf("crash error = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for databaseDriverSessionCount(databaseDriverTestPlugin) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("crash did not close the sessions")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := redactPluginMessage(databaseDriverTestPlugin, databaseDriverTestSecret); got != databaseDriverTestSecret {
		t.Fatal("crashed session kept its secrets registered")
	}
	// No fallback: the dead connection stays unavailable.
	_, err = conn.Execute(ctx, db.ExternalExecute{RequestID: "r2", Statement: "SELECT 1"}, func(db.ExternalPage) error { return nil })
	expectDatabaseDriverCode(t, err, databaseDriverErrUnavailable)
	// A failed plugin is not restarted implicitly.
	_, err = fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	expectDatabaseDriverCode(t, err, databaseDriverErrUnavailable)
}

// ---------------------------------------------------------------------------
// Gates, config and limits.

func TestDatabaseDriverGatesAndStatus(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "")
	fixture.grant(t)
	status := func() DatabaseDriverStatus {
		t.Helper()
		items, err := fixture.manager.databaseDriverStatuses([]databaseDriverRef{{ConnectionID: databaseDriverTestConn, PluginID: databaseDriverTestPlugin, DriverID: databaseDriverTestDriver}})
		if err != nil || len(items) != 1 {
			t.Fatalf("status %v %v", items, err)
		}
		return items[0]
	}
	if got := status(); !got.Available || !got.Granted {
		t.Fatalf("status = %+v", got)
	}
	if list := fixture.manager.databaseDrivers(); len(list) != 1 || list[0].PluginVersion != "0.1.0" || list[0].ID != databaseDriverTestDriver {
		t.Fatalf("list = %+v", list)
	}
	cases := []struct {
		name   string
		mutate func(*pluginRecord)
		reason string
		code   string
	}{
		{"disabled", func(record *pluginRecord) { record.enabled = false }, "disabled", databaseDriverErrUnavailable},
		{"failed", func(record *pluginRecord) { record.state = pluginStateError }, "error", databaseDriverErrUnavailable},
		{"capability", func(record *pluginRecord) { record.capabilities = []api.Capability{api.CapabilityNetwork} }, "capability", databaseDriverErrForbidden},
		{"no entry", func(record *pluginRecord) { record.manifest.contributions = map[string]*resolvedContribution{"x": {}} }, "noDriver", databaseDriverErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := fixture.record
			record.mu.Lock()
			saved := *record.manifest
			savedEnabled, savedState, savedCaps := record.enabled, record.state, record.capabilities
			tc.mutate(record)
			record.mu.Unlock()
			defer func() {
				record.mu.Lock()
				*record.manifest = saved
				record.enabled, record.state, record.capabilities = savedEnabled, savedState, savedCaps
				record.mu.Unlock()
			}()
			if got := status(); got.Available || got.Reason != tc.reason || !got.Granted {
				t.Fatalf("status = %+v", got)
			}
			_, err := fixture.manager.connectDatabaseDriver(context.Background(), databaseDriverTestRequest())
			expectDatabaseDriverCode(t, err, tc.code)
			if len(fixture.manager.databaseDrivers()) != 0 {
				t.Fatal("gated driver listed")
			}
		})
	}
	missing, _ := fixture.manager.databaseDriverStatuses([]databaseDriverRef{{ConnectionID: databaseDriverTestConn, PluginID: "missing.plugin", DriverID: "x"}})
	if missing[0].Reason != "notInstalled" || missing[0].Available {
		t.Fatalf("missing = %+v", missing[0])
	}
	// A point the contract does not enable keeps everything off.
	databaseDriverPointEnabled = func() bool { return false }
	if got := status(); got.Available {
		t.Fatal("driver available while the point is disabled")
	}
	if len(fixture.manager.databaseDrivers()) != 0 {
		t.Fatal("drivers listed while the point is disabled")
	}
}

func TestDatabaseDriverConfigSendsOnlyDeclaredFields(t *testing.T) {
	driver := databaseDriverTestContribution()
	request := databaseDriverTestRequest()
	config, secrets, err := buildDatabaseDriverConfig(driver, request.Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(config)
	want := `{"database":"default","host":"127.0.0.1","options":{"compression":"lz4"},"password":"s3cr3t-value","port":9000,"ssl":false,"username":"reader"}`
	if string(encoded) != want {
		t.Fatalf("config = %s", encoded)
	}
	if !reflect.DeepEqual(secrets, []string{databaseDriverTestSecret}) {
		t.Fatalf("secrets = %v", secrets)
	}
	for name, options := range map[string]map[string]any{
		"unknown option": {"other": "x"},
		"not in enum":    {"compression": "zstd"},
		"wrong type":     {"compression": 1.0},
	} {
		if _, _, err := buildDatabaseDriverConfig(driver, request.Config, options); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	missingHost := request.Config
	missingHost.Host = ""
	if _, _, err := buildDatabaseDriverConfig(driver, missingHost, nil); err == nil {
		t.Fatal("missing required field accepted")
	}
}

func TestDatabaseDriverCallAndSessionLimits(t *testing.T) {
	const plugin = "limits.plugin"
	var releases []func()
	for range databaseDriverCallsPerPlugin {
		release, err := acquireDatabaseDriverCall(plugin)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	_, err := acquireDatabaseDriverCall(plugin)
	expectDatabaseDriverCode(t, err, databaseDriverErrBusy)
	releases[0]()
	releases[0]() // idempotent
	release, err := acquireDatabaseDriverCall(plugin)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, release := range releases[1:] {
		release()
	}

	var conns []*databaseDriverConnection
	for range databaseDriverSessionsMax {
		conn := &databaseDriverConnection{pluginID: plugin}
		if err := reserveDatabaseDriverSession(conn); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	expectDatabaseDriverCode(t, reserveDatabaseDriverSession(&databaseDriverConnection{pluginID: plugin}), databaseDriverErrBusy)
	closeDatabaseDriverSessions(plugin, "stopped")
	for _, conn := range conns {
		if !conn.closed.Load() {
			t.Fatal("lifecycle close left a session open")
		}
	}
	databaseDriverRegistry.mu.Lock()
	_, leaked := databaseDriverRegistry.plugins[plugin]
	databaseDriverRegistry.mu.Unlock()
	if leaked {
		t.Fatal("registry kept an empty plugin entry")
	}
}

func TestDatabaseDriverPageValidation(t *testing.T) {
	cases := map[string]string{
		"too many rows":          `{"columns":[{"name":"a"}],"rows":[[1],[2],[3]],"done":true}`,
		"no cursor":              `{"columns":[{"name":"a"}],"rows":[[1]],"done":false}`,
		"long cursor":            `{"rows":[],"done":false,"cursor":"` + strings.Repeat("c", 129) + `"}`,
		"rows not array":         `{"rows":{"a":1},"done":true}`,
		"row not array":          `{"rows":[1],"done":true}`,
		"long nextPageToken":     `{"rows":[],"done":true,"nextPageToken":"` + strings.Repeat("t", 1025) + `"}`,
		"too many bytes":         `{"rows":[["` + strings.Repeat("x", 64) + `"]],"done":true}`,
		"continuation + columns": ``,
	}
	for name, raw := range cases {
		if raw == "" {
			_, err := decodeDatabaseDriverPage("plugin.db.fetch", json.RawMessage(`{"columns":[{"name":"a"}],"rows":[],"done":true}`), 2, 1024, false)
			expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
			continue
		}
		_, err := decodeDatabaseDriverPage("plugin.db.execute", json.RawMessage(raw), 2, 32, true)
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
	}
	page, err := decodeDatabaseDriverPage("plugin.db.execute", json.RawMessage(`{"columns":[{"name":"a"}],"rows":[[9007199254740993]],"done":false,"cursor":"c1"}`), 2, 1024, true)
	if err != nil || len(page.decodedRows) != 1 || page.decodedRows[0][0] != json.Number("9007199254740993") {
		t.Fatalf("page = %+v %v", page, err)
	}
	if _, err := decodeDatabaseDriverConnectResult(json.RawMessage(`{"handle":"h","info":{"serverVersion":"` + strings.Repeat("v", 129) + `"}}`)); err == nil {
		t.Fatal("long serverVersion accepted")
	}
}

func TestDatabaseDriverPageSplitMatchesUnmarshal(t *testing.T) {
	type outcome struct {
		ok      bool
		page    string
		decoded string
	}
	decode := func(raw string, split bool) outcome {
		var page *databaseDriverPageResult
		var err error
		if split {
			page, err = decodeDatabaseDriverPage("plugin.db.execute", json.RawMessage(raw), 10, 1024, true)
		} else {
			var full databaseDriverPageResult
			if err = json.Unmarshal([]byte(raw), &full); err == nil {
				page = &full
				page.Rows = bytes.TrimSpace(page.Rows)
				if len(page.Rows) == 0 || string(page.Rows) == "null" {
					page.Rows = nil
				}
				if !page.Done && !validDatabaseDriverOpaqueID(page.Cursor) {
					err = errors.New("cursor")
				} else if page.Done {
					page.Cursor = ""
				}
				if err == nil {
					err = page.decodeRows("plugin.db.execute", 10)
				}
			}
		}
		if err != nil {
			return outcome{}
		}
		rows, _ := json.Marshal(page.decodedRows)
		page.decodedRows = nil
		header, _ := json.Marshal(page)
		return outcome{true, string(header), string(rows)}
	}
	accepted := 0
	for _, raw := range []string{
		`{"columns":[{"name":"a","type":"UInt64"}],"rows":[[1,"x"],["}]\"",null]],"done":false,"cursor":"c1","notice":"n"}`,
		` { "rows" : [ [ 1 ] ] , "done" : true } `,
		`{"done":true,"rows":null}`,
		`{"done":true}`,
		`{}`,
		`{"Rows":[[1]],"done":true}`,
		`{"rows":[[1]],"rows":[[2]],"done":true}`,
		`{"rows":[[1]],"done":true}`,
		`{"Done":true,"rows":[[1]],"extra":{"a":[1,{"b":"]"}]}}`,
		`{"rows":[[1]],"done":true}x`,
		`{"rows":[[1]],"done":true`,
		`{"rows":[[1]] "done":true}`,
		`{"rows":[[1}],"done":true}`,
		`{"rows":[[1]],"done":tru}`,
		`{"rows":[["a\u0001"]],"done":true}`,
		"{\"rows\":[[\"a\x01\"]],\"done\":true}",
		`{"rows":[[1]],"done":true,}`,
		`{"rows":,"done":true}`,
		`[1]`,
		`{"cursor":"c","done":false,"rows":[[1],[2]]}`,
	} {
		got, want := decode(raw, true), decode(raw, false)
		if got != want {
			t.Errorf("%s: split %+v, unmarshal %+v", raw, got, want)
		}
		if got.ok {
			accepted++
		}
	}
	if accepted < 6 {
		t.Fatalf("only %d pages accepted; the cases no longer cover valid input", accepted)
	}
}

func TestDatabaseDriverErrorMapping(t *testing.T) {
	registerDatabaseDriverSecrets(&databaseDriverConnection{pluginID: "map.plugin"}, []string{"hunter22"})
	t.Cleanup(func() {
		databaseDriverSecrets.mu.Lock()
		delete(databaseDriverSecrets.values, "map.plugin")
		databaseDriverSecrets.mu.Unlock()
	})
	rpc := func(kind, message string) error {
		data := map[string]any{}
		if kind != "" {
			data["kind"] = kind
		}
		return safeProcessRPCError("plugin.db.execute", &rpcError{Code: -32000, Message: message, Data: data})
	}
	cases := []struct {
		err  error
		want string
	}{
		{rpc("database", "bad hunter22\nline"), "driver_error: bad [redacted] line"},
		{rpc("connection", "refused"), "unavailable: refused"},
		{rpc("cancelled", "stop"), "cancelled: stop"},
		{rpc("timeout", "slow"), "timeout: slow"},
		{rpc("busy", "full"), "busy: full"},
		{rpc("limit", "too big"), "limit: too big"},
		{rpc("weird", "x"), "driver_error: x"},
		{rpc("", "plugin process exited"), "driver_error: plugin process exited"},
		{errors.New("plugin process generation changed"), "unavailable: database driver plugin map.plugin is no longer running"},
	}
	for _, tc := range cases {
		if got := mapDatabaseDriverCallError("map.plugin", "plugin.db.execute", tc.err); got == nil || got.Error() != tc.want {
			t.Fatalf("%v -> %v, want %s", tc.err, got, tc.want)
		}
	}
	if got := mapDatabaseDriverCallError("map.plugin", "x", context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("context error rewritten: %v", got)
	}
	if err := parseDatabaseDriverNegotiation(json.RawMessage(`{"protocol":2,"drivers":["sample"]}`), "sample"); err == nil {
		t.Fatal("protocol 2 accepted")
	}
	if err := parseDatabaseDriverNegotiation(json.RawMessage(`{"protocol":1,"drivers":["sample"]}`), "sample"); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Grants.

func TestDatabaseDriverGrants(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "")
	manager := fixture.manager
	if err := manager.grantDatabaseDriver(databaseDriverTestPlugin, "other", databaseDriverTestConn); err == nil {
		t.Fatal("grant for an undeclared driver accepted")
	}
	for _, id := range []string{"", " padded", strings.Repeat("x", 129), "bad\nid"} {
		if err := manager.grantDatabaseDriver(databaseDriverTestPlugin, databaseDriverTestDriver, id); err == nil {
			t.Fatalf("connection id %q accepted", id)
		}
	}
	fixture.grant(t)
	if ok, _ := databaseDriverGranted(databaseDriverTestPlugin, databaseDriverTestDriver, databaseDriverTestConn); !ok {
		t.Fatal("grant not stored")
	}
	if ok, _ := databaseDriverGranted(databaseDriverTestPlugin, "other", databaseDriverTestConn); ok {
		t.Fatal("grant is not per driver")
	}

	// A connection is bound to one plugin driver: another plugin's grant for
	// the same connection is dropped.
	other := databaseDriverGrantStore{Version: 1, Connections: map[string]databaseDriverGrant{databaseDriverTestConn: {DriverID: "x"}, "keep": {DriverID: "x"}}}
	databaseDriverGrantsMu.Lock()
	_ = writeDatabaseDriverGrantsLocked("other.plugin", other)
	databaseDriverGrantsMu.Unlock()
	fixture.grant(t)
	if ok, _ := databaseDriverGranted("other.plugin", "x", databaseDriverTestConn); ok {
		t.Fatal("rebinding kept the other plugin's grant")
	}
	if ok, _ := databaseDriverGranted("other.plugin", "x", "keep"); !ok {
		t.Fatal("rebinding dropped an unrelated grant")
	}

	removed, err := pruneDatabaseDriverGrants([]string{databaseDriverTestConn})
	if err != nil || removed != 1 {
		t.Fatalf("prune = %d %v", removed, err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, databaseDriverGrantKey("other.plugin")); found {
		t.Fatal("empty grant record not deleted")
	}
	revoked, err := revokeDatabaseDriverGrant(databaseDriverTestConn, "")
	if err != nil || revoked != 1 {
		t.Fatalf("revoke = %d %v", revoked, err)
	}
	if ok, _ := databaseDriverGranted(databaseDriverTestPlugin, databaseDriverTestDriver, databaseDriverTestConn); ok {
		t.Fatal("revoke kept the grant")
	}

	// Commands round-trip the documented shapes.
	host()
	previousHost := managerInst
	managerInst = manager
	t.Cleanup(func() { managerInst = previousHost })
	if out, err := pluginHostDatabaseDriverGrantHandler("", `{"pluginId":"sample-driver","driverId":"sample","connectionId":"`+databaseDriverTestConn+`"}`); err != nil || !strings.Contains(out, `"granted":true`) {
		t.Fatalf("grant command = %s %v", out, err)
	}
	if out, err := pluginHostDatabaseDriverStatusHandler("", `{"refs":[{"connectionId":"`+databaseDriverTestConn+`","pluginId":"sample-driver","driverId":"sample"}]}`); err != nil ||
		!strings.Contains(out, `"available":true`) || !strings.Contains(out, `"granted":true`) {
		t.Fatalf("status command = %s %v", out, err)
	}
	if out, err := pluginHostDatabaseDriversListHandler("", "{}"); err != nil || !strings.Contains(out, `"pluginName":"Sample Driver"`) {
		t.Fatalf("list command = %s %v", out, err)
	}
	if _, err := pluginHostDatabaseDriverPruneHandler("", `{}`); err == nil {
		t.Fatal("prune without liveConnectionIds accepted")
	}
	if out, err := pluginHostDatabaseDriverRevokeHandler("", `{"connectionId":"`+databaseDriverTestConn+`","pluginId":"sample-driver"}`); err != nil || !strings.Contains(out, `"revoked":1`) {
		t.Fatalf("revoke command = %s %v", out, err)
	}
}

func TestDatabaseDriverGrantsNeverExportedAndRemovedWithPlugin(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "")
	fixture.grant(t)
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	if _, err := writeConfigExportWithPlugins(target, config); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); strings.Contains(string(raw), "databaseDrivers") || strings.Contains(string(raw), databaseDriverTestConn) {
		t.Fatalf("configuration export contains driver grants: %s", raw)
	}
	if values, err := pluginKVNamespaceRepository(databaseDriverTestPlugin); err != nil || len(values) != 0 {
		t.Fatalf("plugin namespace holds grants: %v %v", values, err)
	}
	for _, reason := range []removalReason{removalReasonClearData, removalReasonUninstall} {
		fixture.grant(t)
		fixture.manager.runRecordRemovalHooks(databaseDriverTestPlugin, reason)
		if _, found, _ := pluginKVGetRaw(hostPluginID, databaseDriverGrantKey(databaseDriverTestPlugin)); found {
			t.Fatalf("grant survived %s", reason)
		}
	}
}

func TestDatabaseDriverPluginTrust(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "")
	manager := fixture.manager
	host()
	previousHost := managerInst
	managerInst = manager
	t.Cleanup(func() { managerInst = previousHost })

	// Untrusted and ungranted: connect refuses before any credential leaves.
	_, err := manager.connectDatabaseDriver(context.Background(), databaseDriverTestRequest())
	expectDatabaseDriverCode(t, err, databaseDriverErrUntrusted)

	// Setter validation at the Go boundary.
	for _, args := range []string{`{"pluginId":"sample-driver"}`, `{"pluginId":"","trusted":true}`, `{"pluginId":"bad\nid","trusted":true}`, `{"pluginId":"missing.plugin","trusted":true}`} {
		if _, err := pluginHostDatabaseDriverTrustSetHandler("", args); err == nil {
			t.Fatalf("trust set accepted %s", args)
		}
	}
	noCapability := &pluginRecord{id: "plain.plugin", manifest: &Manifest{ID: "plain.plugin", Capabilities: []api.Capability{api.CapabilityNetwork}}}
	manager.records[noCapability.id] = noCapability
	if _, err := pluginHostDatabaseDriverTrustSetHandler("", `{"pluginId":"plain.plugin","trusted":true}`); err == nil {
		t.Fatal("trust accepted for a plugin without the database.driver capability")
	}

	if out, err := pluginHostDatabaseDriverTrustSetHandler("", `{"pluginId":"sample-driver","trusted":true}`); err != nil || !strings.Contains(out, `"trusted":true`) {
		t.Fatalf("trust set = %s %v", out, err)
	}
	if out, err := pluginHostDatabaseDriverTrustGetHandler("", `{"pluginId":"sample-driver"}`); err != nil || !strings.Contains(out, `"trusted":true`) {
		t.Fatalf("trust get = %s %v", out, err)
	}
	// Persisted in the host namespace, never in the plugin's own namespace.
	if _, found, _ := pluginKVGetRaw(hostPluginID, databaseDriverTrustKey(databaseDriverTestPlugin)); !found {
		t.Fatal("trust not persisted in the host namespace")
	}
	if values, err := pluginKVNamespaceRepository(databaseDriverTestPlugin); err != nil || len(values) != 0 {
		t.Fatalf("plugin namespace holds trust: %v %v", values, err)
	}

	// Trusted without a grant: credential release is allowed.
	_, err = manager.connectDatabaseDriver(context.Background(), databaseDriverTestRequest())
	if err != nil && strings.HasPrefix(err.Error(), databaseDriverErrUntrusted+":") {
		t.Fatalf("trusted plugin still refused: %v", err)
	}
	statuses, err := manager.databaseDriverStatuses([]databaseDriverRef{{ConnectionID: databaseDriverTestConn, PluginID: databaseDriverTestPlugin, DriverID: databaseDriverTestDriver}})
	if err != nil || len(statuses) != 1 || !statuses[0].Trusted || statuses[0].Granted {
		t.Fatalf("status while trusted = %+v %v", statuses, err)
	}

	// Untrust keeps explicit grants; ungranted connections are refused again.
	fixture.grant(t)
	if _, err := pluginHostDatabaseDriverTrustSetHandler("", `{"pluginId":"sample-driver","trusted":false}`); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := databaseDriverPluginTrusted(databaseDriverTestPlugin); trusted {
		t.Fatal("untrust kept the trust record")
	}
	if ok, _ := databaseDriverGranted(databaseDriverTestPlugin, databaseDriverTestDriver, databaseDriverTestConn); !ok {
		t.Fatal("untrust dropped an explicit grant")
	}
	other := databaseDriverTestRequest()
	other.ConnectionID = "other-connection"
	_, err = manager.connectDatabaseDriver(context.Background(), other)
	expectDatabaseDriverCode(t, err, databaseDriverErrUntrusted)

	// Removed with the plugin on uninstall and Clear Plugin Data.
	for _, reason := range []removalReason{removalReasonClearData, removalReasonUninstall} {
		if err := manager.setDatabaseDriverTrust(databaseDriverTestPlugin, true); err != nil {
			t.Fatal(err)
		}
		manager.runRecordRemovalHooks(databaseDriverTestPlugin, reason)
		if _, found, _ := pluginKVGetRaw(hostPluginID, databaseDriverTrustKey(databaseDriverTestPlugin)); found {
			t.Fatalf("trust survived %s", reason)
		}
	}

	// Never part of a configuration export.
	if err := manager.setDatabaseDriverTrust(databaseDriverTestPlugin, true); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	if _, err := writeConfigExportWithPlugins(target, config); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); strings.Contains(string(raw), "databaseDrivers") {
		t.Fatalf("configuration export contains driver trust: %s", raw)
	}
}
