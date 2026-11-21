package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db"
)

// Protocol v2 draft: negotiation of the experimental columnar page encoding
// and decoding of untrusted column blocks. The golden transcripts
// v2-columnar*.jsonl are produced by the Go SDK (its conformance test replays
// them), so decoding their pages here is the SDK-encoder / host-decoder
// interoperability check that also runs on Windows.

func readDatabaseDriverTranscript(t *testing.T, name string) []databaseDriverTranscriptLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tools", "verify", "fixtures", "database-driver-protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	var lines []databaseDriverTranscriptLine
	for index, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if index == 0 {
			continue
		}
		var line databaseDriverTranscriptLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("%s line %d: %v", name, index+1, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// transcriptResult returns the result of the plugin reply with id.
func transcriptResult(t *testing.T, lines []databaseDriverTranscriptLine, id string) json.RawMessage {
	t.Helper()
	for _, line := range lines {
		var message rpcEnvelope
		_ = json.Unmarshal(line.Message, &message)
		if line.Dir == "plugin" && string(message.ID) == `"`+id+`"` {
			return message.Result
		}
	}
	t.Fatalf("no plugin reply %s", id)
	return nil
}

func transcriptDetails(t *testing.T, lines []databaseDriverTranscriptLine) json.RawMessage {
	t.Helper()
	var result struct {
		DatabaseDrivers json.RawMessage `json:"databaseDrivers"`
	}
	if err := json.Unmarshal(transcriptResult(t, lines, "1"), &result); err != nil {
		t.Fatal(err)
	}
	return result.DatabaseDrivers
}

func TestDatabaseDriverRequestedEncoding(t *testing.T) {
	announced := `{"protocol":1,"minor":1,"drivers":["sample"],"pageLimits":{"maxRows":5000,"maxBytes":2097152},"encodings":["columnar1"]}`
	for _, test := range []struct {
		name    string
		details string
		enabled bool
		want    string
	}{
		{"announced, switch on", announced, true, databaseDriverEncodingColumnar},
		{"announced, switch off", announced, false, ""},
		{"v1.1 plugin (old driver, new host)", `{"protocol":1,"minor":1,"drivers":["sample"],"pageLimits":{"maxRows":5000,"maxBytes":2097152}}`, true, ""},
		{"v1.0 plugin", `{"protocol":1,"drivers":["sample"]}`, true, ""},
		{"unknown encodings only", `{"protocol":1,"encodings":["arrow"]}`, true, ""},
		{"wrong protocol", `{"protocol":2,"encodings":["columnar1"]}`, true, ""},
		{"malformed", `{"protocol":1,"encodings":"columnar1"}`, true, ""},
		{"too many encodings", `{"protocol":1,"encodings":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","columnar1"]}`, true, ""},
	} {
		if got := databaseDriverRequestedEncoding(json.RawMessage(test.details), test.enabled); got != test.want {
			t.Errorf("%s: requested %q, want %q", test.name, got, test.want)
		}
	}
	// Both golden transcripts announce the encoding.
	for _, name := range []string{"v2-columnar.jsonl", "v2-columnar-declined.jsonl"} {
		if got := databaseDriverRequestedEncoding(transcriptDetails(t, readDatabaseDriverTranscript(t, name)), true); got != databaseDriverEncodingColumnar {
			t.Errorf("%s: requested %q", name, got)
		}
	}
	// The encoding member is omitted unless requested, so a pre-v2 plugin
	// (strict params) sees exactly the 1.1 connect params.
	encoded, _ := json.Marshal(databaseDriverConnectParams{DriverID: "sample", Config: map[string]any{}})
	if strings.Contains(string(encoded), "encoding") {
		t.Fatalf("connect params without a request carry encoding: %s", encoded)
	}
}

func TestDatabaseDriverAcceptedEncoding(t *testing.T) {
	for _, test := range []struct {
		requested, confirmed string
		want, fails          bool
	}{
		{databaseDriverEncodingColumnar, databaseDriverEncodingColumnar, true, false},
		{databaseDriverEncodingColumnar, "", false, false}, // the plugin may decline
		{"", "", false, false},
		{"", databaseDriverEncodingColumnar, false, true}, // never requested
		{databaseDriverEncodingColumnar, "arrow", false, true},
	} {
		got, err := databaseDriverAcceptedEncoding(test.requested, test.confirmed)
		if got != test.want || (err != nil) != test.fails {
			t.Errorf("%q/%q: %v %v", test.requested, test.confirmed, got, err)
		}
		if err != nil {
			expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
		}
	}
}

// decodeTranscriptPage decodes a transcript page as the proxy does.
func decodeTranscriptPage(t *testing.T, raw json.RawMessage, method string, maxRows int, first, columnar bool) *databaseDriverPageResult {
	t.Helper()
	page, err := decodeDatabaseDriverPageHeaderEncoded(method, raw, databaseDriverPageMaxBytesV11, first, columnar)
	if err != nil {
		t.Fatalf("%s header: %v", method, err)
	}
	if err := page.decodeRows(method, maxRows); err != nil {
		t.Fatalf("%s rows: %v", method, err)
	}
	return page
}

// TestDatabaseDriverColumnarTranscriptPages: the SDK-encoded columnar pages
// decode to exactly the rows the 1.1 JSON pages of the same result decode to,
// and every column kind decodes to the JSON path's value space.
func TestDatabaseDriverColumnarTranscriptPages(t *testing.T) {
	columnar := readDatabaseDriverTranscript(t, "v2-columnar.jsonl")
	jsonRows := readDatabaseDriverTranscript(t, "v11-negotiation.jsonl")
	for _, id := range []string{"3", "4"} {
		method, first := databaseDriverMethodFetch, false
		if id == "3" {
			method, first = databaseDriverMethodExecute, true
		}
		want := decodeTranscriptPage(t, transcriptResult(t, jsonRows, id), method, 1000, first, false)
		got := decodeTranscriptPage(t, transcriptResult(t, columnar, id), method, 1000, first, true)
		if !reflect.DeepEqual(got.decodedRows, want.decodedRows) || !reflect.DeepEqual(got.Columns, want.Columns) ||
			got.Cursor != want.Cursor || got.Done != want.Done {
			t.Fatalf("page %s: columnar %#v / %+v, JSON %#v / %+v", id, got.decodedRows, got, want.decodedRows, want)
		}
	}
	types := decodeTranscriptPage(t, transcriptResult(t, columnar, "5"), databaseDriverMethodExecute, 1000, true, true)
	want := [][]any{
		{int64(-3), 0.25, true, "x", "text", nil},
		{int64(1 << 40), 0.1, nil, "日本", int64(5), nil},
		{nil, -1e21, false, nil, 2.5, nil},
		{int64(0), nil, true, "", map[string]any{"k": json.Number("1")}, nil},
	}
	if !reflect.DeepEqual(types.decodedRows, want) || !types.Done {
		t.Fatalf("types page %#v", types.decodedRows)
	}
}

func TestDatabaseDriverColumnarPageValidation(t *testing.T) {
	lines := readDatabaseDriverTranscript(t, "v2-columnar.jsonl")
	valid := transcriptResult(t, lines, "5")
	var page map[string]json.RawMessage
	_ = json.Unmarshal(valid, &page)
	var data string
	_ = json.Unmarshal(page["data"], &data)
	bin, _ := base64.StdEncoding.DecodeString(data)
	withData := func(value string) json.RawMessage {
		page["data"], _ = json.Marshal(value)
		out, _ := json.Marshal(page)
		return out
	}
	headerError := func(name string, raw json.RawMessage, maxBytes int64, columnar bool) {
		t.Helper()
		if _, err := decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodExecute, raw, maxBytes, true, columnar); err == nil {
			t.Errorf("%s: header accepted", name)
		} else {
			expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
		}
	}
	// Not negotiated: a columnar page is a protocol error, never decoded.
	headerError("not negotiated", valid, databaseDriverPageMaxBytesV11, false)
	headerError("unknown encoding", json.RawMessage(strings.Replace(string(valid), `"encoding":"columnar1"`, `"encoding":"arrow"`, 1)), databaseDriverPageMaxBytesV11, true)
	headerError("data without encoding", json.RawMessage(strings.Replace(string(valid), `"encoding":"columnar1",`, ``, 1)), databaseDriverPageMaxBytesV11, true)
	headerError("rows and data", json.RawMessage(strings.Replace(string(valid), `"done":true`, `"done":true,"rows":[]`, 1)), databaseDriverPageMaxBytesV11, true)
	headerError("data not a string", json.RawMessage(strings.Replace(string(valid), `"data":"`, `"data":["`, 1)+"]"), databaseDriverPageMaxBytesV11, true)
	headerError("escaped data", withData(`<`+data), databaseDriverPageMaxBytesV11, true)
	headerError("data over the budget", valid, int64(len(data)-1), true)

	// Without negotiation a stray "data" member (no "encoding") is ignored
	// like any unknown member before v2.
	if page := decodeTranscriptPage(t, json.RawMessage(`{"columns":[{"name":"a"}],"rows":[[1]],"data":"x","done":true}`), databaseDriverMethodExecute, 1000, true, false); len(page.decodedRows) != 1 {
		t.Fatalf("stray data member: %v", page.decodedRows)
	}
	// A negotiated connection may still answer JSON rows.
	if page := decodeTranscriptPage(t, transcriptResult(t, readDatabaseDriverTranscript(t, "v11-negotiation.jsonl"), "3"), databaseDriverMethodExecute, 1000, true, true); len(page.decodedRows) != 2 {
		t.Fatalf("JSON page on a columnar connection: %v", page.decodedRows)
	}

	rowsError := func(name string, raw json.RawMessage, maxRows int, maxBytes int64) {
		t.Helper()
		page, err := decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodExecute, raw, maxBytes, true, true)
		if err == nil {
			err = page.decodeRows(databaseDriverMethodExecute, maxRows)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
			return
		}
		expectDatabaseDriverCode(t, err, databaseDriverErrProtocol)
	}
	rowsError("more rows than requested", valid, 3, databaseDriverPageMaxBytesV11)
	rowsError("bad base64", withData("!!!!"+data), 1000, databaseDriverPageMaxBytesV11)
	rowsError("trailing byte", withData(base64.StdEncoding.EncodeToString(append(append([]byte(nil), bin...), 0))), 1000, databaseDriverPageMaxBytesV11)
	// Every truncation of a valid page is rejected without a panic.
	for cut := 0; cut < len(bin); cut++ {
		rowsError("truncated", withData(base64.StdEncoding.EncodeToString(bin[:cut])), 1000, databaseDriverPageMaxBytesV11)
	}
	encode := func(raw string) json.RawMessage { return withData(base64.StdEncoding.EncodeToString([]byte(raw))) }
	for name, raw := range map[string]string{
		"bad magic":            "MCC2\x01\x01\x00",
		"too many columns":     "MCC1\x01\x81\x08",
		"bad kind":             "MCC1\x01\x01\x07",
		"null bitmap on mixed": "MCC1\x01\x01\x86\x00\x00",
		"bad bool":             "MCC1\x01\x01\x01\x02",
		"bad tag":              "MCC1\x01\x01\x06\x09",
		"invalid utf-8":        "MCC1\x01\x01\x04\x01\xff",
		"split rune":           "MCC1\x02\x01\x04\x01\x02\xe6\x97\xa5",
		"length overflow":      "MCC1\x01\x01\x04\xff\xff\xff\xff\xff\xff\xff\xff\xff\x01",
		"json scalar":          "MCC1\x01\x01\x05\x011",
		"json trailing":        "MCC1\x01\x01\x05\x04{}{}",
		"varint overflow":      "MCC1\x01\x01\x02\xff\xff\xff\xff\xff\xff\xff\xff\xff\x7f",
	} {
		rowsError(name, encode(raw), 1000, databaseDriverPageMaxBytesV11)
	}
	// The cell count is bounded by the byte budget: 1000 rows of 1024
	// all-null columns are a few bytes of data but need a large allocation.
	rowsError("cells beyond the budget", encode("MCC1\xe8\x07\x80\x08"+strings.Repeat("\x00", 1024)), 1000, 4096)
}

// replayDatabaseDriverTranscript connects through the replay helper with the
// columnar switch set as given and runs the transcript's first execute.
func replayDatabaseDriverTranscript(t *testing.T, name string, columnar bool) (*databaseDriverConnection, [][]any) {
	t.Helper()
	previous := databaseDriverColumnarEnabled
	databaseDriverColumnarEnabled = func() bool { return columnar }
	t.Cleanup(func() { databaseDriverColumnarEnabled = previous })
	fixture := newDatabaseDriverFixture(t, "replay")
	transcript, err := os.ReadFile(filepath.Join("..", "..", "tools", "verify", "fixtures", "database-driver-protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "transcript.jsonl"), transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	fixture.grant(t)
	conn, err := fixture.manager.connectDatabaseDriver(ctx, databaseDriverTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	summary, err := conn.Execute(ctx, db.ExternalExecute{
		RequestID: "q1", Statement: "SELECT id, name, payload FROM users ORDER BY id", Database: "default",
		Mode: db.ExternalExecuteAuto, MaxRows: 2, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
	}, collectDatabaseDriverRows(&rows))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 3 || len(rows) != 3 || rows[0][0] != "9007199254740993" {
		t.Fatalf("summary %+v rows %v", summary, rows)
	}
	return conn.(*databaseDriverConnection), rows
}

// TestDatabaseDriverV2TranscriptReplay runs the interoperability matrix
// against the replay helper (POSIX; the transcript replay compares every
// host request, so an unexpected "encoding" member fails the call):
//   - new driver + new host, switch on: columnar negotiated (v2-columnar);
//   - new driver + host with the switch off: JSON rows (v2-columnar-declined);
//   - old 1.1 driver + new host, switch on: JSON rows (v11-negotiation).
func TestDatabaseDriverV2TranscriptReplay(t *testing.T) {
	t.Run("columnar", func(t *testing.T) {
		conn, _ := replayDatabaseDriverTranscript(t, "v2-columnar.jsonl", true)
		if !conn.columnar {
			t.Fatal("columnar encoding not negotiated")
		}
		var rows [][]any
		if _, err := conn.Execute(context.Background(), db.ExternalExecute{
			RequestID: "q2", Statement: "SELECT * FROM types", Database: "default",
			Mode: db.ExternalExecuteAuto, MaxRows: 5000, MaxBytes: 16 << 20, Timeout: 120 * time.Second,
		}, collectDatabaseDriverRows(&rows)); err != nil || len(rows) != 4 || rows[1][0] != int64(1<<40) {
			t.Fatalf("types: %v %v", rows, err)
		}
		if err := conn.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("new driver, switch off", func(t *testing.T) {
		conn, _ := replayDatabaseDriverTranscript(t, "v2-columnar-declined.jsonl", false)
		if conn.columnar {
			t.Fatal("columnar negotiated with the switch off")
		}
		_ = conn.Close(context.Background())
	})
	t.Run("old driver, switch on", func(t *testing.T) {
		conn, _ := replayDatabaseDriverTranscript(t, "v11-negotiation.jsonl", true)
		if conn.columnar {
			t.Fatal("columnar negotiated with a 1.1 driver")
		}
		_ = conn.Close(context.Background())
	})
}
