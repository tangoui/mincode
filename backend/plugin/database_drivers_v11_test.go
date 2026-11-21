package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db"
)

func TestNegotiatedDatabaseDriverPageLimits(t *testing.T) {
	large := databaseDriverPageLimits{rows: databaseDriverPageMaxRowsV11, bytes: databaseDriverPageMaxBytesV11}
	for _, test := range []struct {
		name    string
		details string
		want    databaseDriverPageLimits
	}{
		{"v1.0", `{"protocol":1,"drivers":["sample"]}`, databaseDriverPageLimitsV10},
		{"v1.1", `{"protocol":1,"minor":1,"drivers":["sample"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}`, large},
		{"future minor", `{"protocol":1,"minor":2,"drivers":["sample"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}`, large},
		{"plugin asks for more", `{"protocol":1,"minor":1,"pageLimits":{"maxRows":100000,"maxBytes":67108864}}`, large},
		{"plugin asks for less", `{"protocol":1,"minor":1,"pageLimits":{"maxRows":2000,"maxBytes":1048576}}`, databaseDriverPageLimits{rows: 2000, bytes: 1 << 20}},
		{"below v1.0", `{"protocol":1,"minor":1,"pageLimits":{"maxRows":10,"maxBytes":2097152}}`, databaseDriverPageLimitsV10},
		{"minor without limits", `{"protocol":1,"minor":1,"drivers":["sample"]}`, databaseDriverPageLimitsV10},
		{"limits without minor", `{"protocol":1,"pageLimits":{"maxRows":5000,"maxBytes":2097152}}`, databaseDriverPageLimitsV10},
		{"wrong protocol", `{"protocol":2,"minor":1,"pageLimits":{"maxRows":5000,"maxBytes":2097152}}`, databaseDriverPageLimitsV10},
		{"malformed", `{"protocol":1,"minor":"1"}`, databaseDriverPageLimitsV10},
	} {
		got := negotiatedDatabaseDriverPageLimits(json.RawMessage(test.details))
		if got != test.want {
			t.Errorf("%s: limits %+v, want %+v", test.name, got, test.want)
		}
		if rows := got.requestRows(); rows > databaseDriverRequestPageRows || rows != min(got.rows, 1000) {
			t.Errorf("%s: request rows %d", test.name, rows)
		}
		wantMessage := pluginRPCMaxMessage
		if test.want.large() {
			wantMessage = pluginRPCMaxLargeReply
		}
		if got := databaseDriverRuntimeMaxMessage(json.RawMessage(test.details)); got != wantMessage {
			t.Errorf("%s: line cap %d, want %d", test.name, got, wantMessage)
		}
	}
}

// TestDatabaseDriverPageBudgetRequestRows pins the host's per-page request:
// at most 1000 rows even when 5000 were negotiated, with the full negotiated
// byte budget, lowered to the caller's caps.
func TestDatabaseDriverPageBudgetRequestRows(t *testing.T) {
	for _, test := range []struct {
		name      string
		limits    databaseDriverPageLimits
		reqRows   int
		reqBytes  int64
		wantRows  int
		wantBytes int64
	}{
		{"v1.1 uncapped", databaseDriverPageLimits{rows: 5000, bytes: 2 << 20}, 0, 0, 1000, 2 << 20},
		{"v1.1 caller asks 5000", databaseDriverPageLimits{rows: 5000, bytes: 2 << 20}, 5000, 16 << 20, 1000, 2 << 20},
		{"v1.1 caller caps", databaseDriverPageLimits{rows: 5000, bytes: 2 << 20}, 200, 1 << 20, 200, 1 << 20},
		{"v1.1 plugin below 1000", databaseDriverPageLimits{rows: 800, bytes: 2 << 20}, 0, 0, 800, 2 << 20},
		{"v1.0", databaseDriverPageLimitsV10, 5000, 16 << 20, 1000, databaseDriverPageMaxBytes},
	} {
		conn := &databaseDriverConnection{limits: test.limits}
		rows, bytes := conn.pageBudget(test.reqRows, test.reqBytes)
		if rows != test.wantRows || bytes != test.wantBytes {
			t.Errorf("%s: budget %d/%d, want %d/%d", test.name, rows, bytes, test.wantRows, test.wantBytes)
		}
	}
}

func TestRuntimeFeaturesMaxMessage(t *testing.T) {
	offer := map[string]int{"databaseDrivers": 1, "tasks": 1}
	for result, want := range map[string]int{
		`{"features":{"tasks":1}}`: pluginRPCMaxMessage,
		`{"features":{"databaseDrivers":1},"databaseDrivers":{"protocol":1,"drivers":["x"]}}`:                                                            pluginRPCMaxMessage,
		`{"features":{"databaseDrivers":1},"databaseDrivers":{"protocol":1,"minor":1,"drivers":["x"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}}`: pluginRPCMaxLargeReply,
		// Not negotiated (no echo): the details never raise the cap.
		`{"features":{"tasks":1},"databaseDrivers":{"protocol":1,"minor":1,"drivers":["x"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}}`: pluginRPCMaxMessage,
	} {
		if got := negotiateRuntimeFeatures(offer, json.RawMessage(result)).maxMessage(); got != want {
			t.Errorf("%s: max message %d, want %d", result, got, want)
		}
	}
}

// largeRPCLine is a well-formed response of about size bytes.
func largeRPCLine(id string, size int) string {
	return `{"jsonrpc":"2.0","id":"` + id + `","result":{"rows":[["` + strings.Repeat("x", size) + `"]],"done":true}}` + "\n"
}

func TestReadStdoutLargeReplies(t *testing.T) {
	const big = 3 << 19 // 1.5 MiB: above the 1 MiB default, below the 4 MiB raised cap
	run := func(limit int64, largeIDs []string, stdout string) (*processRuntime, map[string]chan rpcEnvelope) {
		t.Helper()
		h := newProcessRuntime("", Manifest{}, nil, nil)
		h.generation = 1
		h.pending = map[string]chan rpcEnvelope{}
		h.largePending = map[string]struct{}{}
		channels := map[string]chan rpcEnvelope{}
		for _, id := range []string{"a", "b", "c"} {
			channels[id] = make(chan rpcEnvelope, 1)
			h.pending[id] = channels[id]
		}
		for _, id := range largeIDs {
			h.largePending[id] = struct{}{}
		}
		lineLimit := newRPCLineLimit()
		lineLimit.Store(limit)
		done := make(chan struct{})
		go func() { h.readStdout(1, strings.NewReader(stdout), lineLimit); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("readStdout did not return")
		}
		return h, channels
	}
	delivered := func(channel chan rpcEnvelope) bool {
		select {
		case <-channel:
			return true
		default:
			return false
		}
	}
	small := `{"jsonrpc":"2.0","id":"c","result":true}` + "\n"

	t.Run("raised cap accepts a registered large reply", func(t *testing.T) {
		h, channels := run(pluginRPCMaxLargeReply, []string{"a"}, largeRPCLine("a", big)+small)
		if !delivered(channels["a"]) || !delivered(channels["c"]) {
			t.Fatal("large reply or following reply not delivered")
		}
		if len(h.largePending) != 0 {
			t.Fatalf("largePending not cleared: %v", h.largePending)
		}
	})
	t.Run("unregistered large reply is fatal", func(t *testing.T) {
		_, channels := run(pluginRPCMaxLargeReply, []string{"a"}, largeRPCLine("b", big)+small)
		if delivered(channels["b"]) || delivered(channels["c"]) {
			t.Fatal("oversized reply to an ordinary call was accepted")
		}
	})
	t.Run("large request or notification is fatal", func(t *testing.T) {
		notification := `{"jsonrpc":"2.0","method":"host.log","params":{"message":"` + strings.Repeat("x", big) + `"}}` + "\n"
		_, channels := run(pluginRPCMaxLargeReply, []string{"a"}, notification+small)
		if delivered(channels["c"]) {
			t.Fatal("reader continued after an oversized notification")
		}
	})
	t.Run("large invalid JSON is fatal", func(t *testing.T) {
		_, channels := run(pluginRPCMaxLargeReply, []string{"a"}, strings.Repeat("x", big)+"\n"+small)
		if delivered(channels["c"]) {
			t.Fatal("reader continued after oversized invalid output")
		}
	})
	t.Run("default cap keeps 1 MiB fatal", func(t *testing.T) {
		_, channels := run(pluginRPCMaxMessage, []string{"a"}, largeRPCLine("a", big)+small)
		if delivered(channels["a"]) || delivered(channels["c"]) {
			t.Fatal("1 MiB cap not enforced before negotiation")
		}
	})
	t.Run("raised cap still bounded", func(t *testing.T) {
		_, channels := run(pluginRPCMaxLargeReply, []string{"a"}, largeRPCLine("a", pluginRPCMaxLargeReply)+small)
		if delivered(channels["a"]) || delivered(channels["c"]) {
			t.Fatal("reply above 4 MiB accepted")
		}
	})
}

// TestDatabaseDriverV11TranscriptReplay replays the protocol 1.1 golden
// transcript: the negotiated 5000 rows / 2 MiB reach connect, and execute and
// the host prefetch's fetch request at most 1000 rows with the 2 MiB budget.
func TestDatabaseDriverV11TranscriptReplay(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "replay")
	transcript, err := os.ReadFile(filepath.Join("..", "..", "tools", "verify", "fixtures", "database-driver-protocol", "v11-negotiation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "transcript.jsonl"), transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture.grant(t)
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if limits := conn.(*databaseDriverConnection).limits; limits != (databaseDriverPageLimits{rows: 5000, bytes: 2 << 20}) || limits.requestRows() != 1000 {
		t.Fatalf("limits = %+v", limits)
	}
	var rows [][]any
	summary, err := conn.Execute(ctx, db.ExternalExecute{
		RequestID: "q1", Statement: "SELECT id, name, payload FROM users ORDER BY id", Database: "default",
		Mode: db.ExternalExecuteAuto, MaxRows: 2, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
	}, collectDatabaseDriverRows(&rows))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 3 || len(rows) != 3 {
		t.Fatalf("summary %+v rows %v", summary, rows)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestDatabaseDriverV11LargePage runs a real process that negotiates 1.1
// (5000 rows / 2 MiB): the host still requests 1000-row pages (the helper
// rejects any other budget), and a page of wide rows above the 1 MiB default
// line cap is accepted.
func TestDatabaseDriverV11LargePage(t *testing.T) {
	fixture := newDatabaseDriverFixture(t, "v11")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture.grant(t)
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	summary, err := conn.Execute(ctx, db.ExternalExecute{
		RequestID: "q1", Statement: "large", Mode: db.ExternalExecuteQuery, MaxRows: 5000, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
	}, collectDatabaseDriverRows(&rows))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != databaseDriverV11LargeRows || len(rows) != databaseDriverV11LargeRows {
		t.Fatalf("summary %+v rows %d", summary, len(rows))
	}
	// The generation keeps working after a large page.
	rows = nil
	if _, err := conn.Execute(ctx, db.ExternalExecute{
		RequestID: "q2", Statement: "large", Mode: db.ExternalExecuteQuery, MaxRows: 5000, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
	}, collectDatabaseDriverRows(&rows)); err != nil || len(rows) != databaseDriverV11LargeRows {
		t.Fatalf("second large page: %d rows, %v", len(rows), err)
	}
}
