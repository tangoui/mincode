package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

func jsonrpcHarness(t *testing.T) *processRuntime {
	t.Helper()
	h := newProcessRuntime("", Manifest{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.generation, h.runContext, h.cmd = 1, ctx, &exec.Cmd{}
	h.writes = make(chan rpcWriteRequest, 16)
	return h
}

func feed(t *testing.T, h *processRuntime, line string) bool {
	t.Helper()
	busy := false
	return h.handleStdoutMessage(1, []byte(line), &busy)
}

func nextReply(t *testing.T, h *processRuntime) []byte {
	t.Helper()
	select {
	case request := <-h.writes:
		return request.data
	case <-time.After(2 * time.Second):
		t.Fatal("no reply written")
		return nil
	}
}

func noReply(t *testing.T, h *processRuntime) {
	t.Helper()
	select {
	case request := <-h.writes:
		t.Fatalf("unexpected reply: %s", request.data)
	case <-time.After(100 * time.Millisecond):
	}
}

func decodeError(t *testing.T, data []byte) (string, int) {
	t.Helper()
	var reply rpcEnvelope
	if err := json.Unmarshal(data, &reply); err != nil || reply.JSONRPC != "2.0" || reply.Error == nil {
		t.Fatalf("not an error reply: %s (%v)", data, err)
	}
	return string(reply.ID), reply.Error.Code
}

// JSON-RPC 2.0 §5.1: unparsable input gets -32700 with id null.
func TestJSONRPCParseErrorReply(t *testing.T) {
	h := jsonrpcHarness(t)
	if !feed(t, h, `{"jsonrpc":"2.0","method":"host.info",`) {
		t.Fatal("parse error ended the stream")
	}
	if id, code := decodeError(t, nextReply(t, h)); id != "null" || code != -32700 {
		t.Fatalf("id %s code %d", id, code)
	}
}

// §5.1: a valid JSON value that is not a valid Request gets -32600, echoing a valid id.
func TestJSONRPCInvalidRequestReplies(t *testing.T) {
	cases := []struct{ line, id string }{
		{`{"jsonrpc":"1.0","id":7,"method":"host.info"}`, "7"},
		{`{"jsonrpc":"2.0","id":"a","method":1}`, `"a"`},
		{`{"jsonrpc":"2.0","id":{"x":1},"method":"host.info"}`, "null"},
		{`"hello"`, "null"},
		{`[]`, "null"},
	}
	for _, c := range cases {
		h := jsonrpcHarness(t)
		feed(t, h, c.line)
		if id, code := decodeError(t, nextReply(t, h)); id != c.id || code != -32600 {
			t.Fatalf("%s: id %s code %d", c.line, id, code)
		}
	}
	// Messages without a method are responses, malformed or not, and never answered.
	for _, line := range []string{`{"jsonrpc":"1.0","id":"x","result":1}`, `{"jsonrpc":"2.0","id":3}`} {
		h := jsonrpcHarness(t)
		feed(t, h, line)
		noReply(t, h)
	}
}

// §6: a batch gets one array holding a response per request (notifications get
// none, invalid entries get -32600); a batch of only notifications gets nothing.
func TestJSONRPCBatchRequests(t *testing.T) {
	h := jsonrpcHarness(t)
	feed(t, h, `[{"jsonrpc":"2.0","id":1,"method":"host.info"},{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"n"}},1,{"jsonrpc":"2.0","id":"two","method":"host.nope"}]`)
	var replies []rpcEnvelope
	data := nextReply(t, h)
	if err := json.Unmarshal(data, &replies); err != nil {
		t.Fatalf("batch reply is not an array: %s", data)
	}
	sort.Slice(replies, func(i, j int) bool { return string(replies[i].ID) < string(replies[j].ID) })
	if len(replies) != 3 {
		t.Fatalf("replies: %s", data)
	}
	if string(replies[0].ID) != `"two"` || replies[0].Error == nil || replies[0].Error.Code != -32601 {
		t.Fatalf("unknown method: %+v", replies[0])
	}
	if string(replies[1].ID) != "1" || replies[1].Error != nil || len(replies[1].Result) == 0 {
		t.Fatalf("host.info: %+v", replies[1])
	}
	if string(replies[2].ID) != "null" || replies[2].Error == nil || replies[2].Error.Code != -32600 {
		t.Fatalf("invalid entry: %+v", replies[2])
	}
	noReply(t, h)

	feed(t, h, `[{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"a"}},{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"b"}}]`)
	noReply(t, h)
}

// Responses may arrive batched too; each one reaches its pending call.
func TestJSONRPCBatchedResponses(t *testing.T) {
	h := jsonrpcHarness(t)
	first, second := make(chan rpcEnvelope, 1), make(chan rpcEnvelope, 1)
	h.pending = map[string]chan rpcEnvelope{"1": first, "2": second}
	feed(t, h, `[{"jsonrpc":"2.0","id":"1","result":"a"},{"jsonrpc":"2.0","id":"2","error":{"code":-32000,"message":"b"}}]`)
	select {
	case got := <-first:
		if string(got.Result) != `"a"` {
			t.Fatalf("first: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first response not delivered")
	}
	select {
	case got := <-second:
		if got.Error == nil || got.Error.Message != "b" {
			t.Fatalf("second: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second response not delivered")
	}
	noReply(t, h)
}

// A batch reply above the 1 MiB message limit is split into several arrays,
// each within the limit, instead of being dropped: every request still gets
// its reply by id.
func TestJSONRPCBatchReplySplitsAtMessageLimit(t *testing.T) {
	h := jsonrpcHarness(t)
	// The request stays under the limit; the replies, echoing every id with an
	// error object, do not.
	const count = 4300
	pad := strings.Repeat("x", 180)
	entries := make([]string, count)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"jsonrpc":"2.0","id":"%s-%d","method":"host.nope"}`, pad, i)
	}
	request := "[" + strings.Join(entries, ",") + "]"
	if len(request) >= pluginRPCMaxMessage {
		t.Fatalf("request is %d bytes", len(request))
	}
	feed(t, h, request)
	seen := map[string]bool{}
	arrays := 0
	for len(seen) < count {
		var data []byte
		select {
		case request := <-h.writes:
			data = request.data
			request.result <- nil // the writer's ack lets the next array go out
		case <-time.After(2 * time.Second):
			t.Fatalf("no array after %d arrays, %d replies", arrays, len(seen))
		}
		arrays++
		if len(data) > pluginRPCMaxMessage {
			t.Fatalf("array %d is %d bytes", arrays, len(data))
		}
		var replies []rpcEnvelope
		if err := json.Unmarshal(data, &replies); err != nil {
			t.Fatalf("array %d is not a batch: %v", arrays, err)
		}
		for _, reply := range replies {
			if reply.Error == nil || seen[string(reply.ID)] {
				t.Fatalf("reply %s: %+v", reply.ID, reply.Error)
			}
			seen[string(reply.ID)] = true
		}
	}
	if arrays < 2 {
		t.Fatalf("reply was not split: %d array", arrays)
	}
	noReply(t, h)
}

func TestSplitBatchKeepsOrderWithinLimit(t *testing.T) {
	items := [][]byte{[]byte(`"aaaa"`), []byte(`"bb"`), []byte(`"cccccc"`), []byte(strings.Repeat("z", 20)), []byte(`"d"`)}
	got := splitBatch(items, 16)
	want := []string{`["aaaa","bb"]`, `["cccccc","d"]`}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if string(got[i]) != want[i] || len(got[i]) >= 16 {
			t.Fatalf("array %d = %s", i, got[i])
		}
	}
}
