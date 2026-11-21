package debug

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidDebuggerAddress(t *testing.T) {
	for _, address := range []string{
		"jdt://contents/java.base/java.util/ArrayList.class?=x",
		"foo+bar.baz://x",
		"ab://x",
		"x-y://" + strings.Repeat("a", 4096-len("x-y://")),
	} {
		got, err := ValidDebuggerAddress(address)
		if err != nil || got != address {
			t.Errorf("ValidDebuggerAddress(%q) = %q, %v; want it unchanged", address, got, err)
		}
	}
	for _, address := range []string{
		"",
		"file:///etc/passwd",
		"FILE://x",
		"File://x",
		"/abs/path",
		"relative",
		"C://x",
		"c://x",
		"Z://",
		"a://",
		"C:\\Windows\\x",
		"1abc://x",
		"+x://y",
		"jdt:/x",
		"jdt:x",
		"x-y://" + strings.Repeat("a", 4096-len("x-y://")+1),
		"jdt://a\nb",
		"jdt://a\rb",
		"jdt://a\x00b",
		"jdt://a\tb",
		"jdt://a\u202eb",
		"jdt://a\u2066b",
		"jdt://a\u200bb",
		"jdt://a\u0085b",
		"jdt://a\xffb",
	} {
		_, err := ValidDebuggerAddress(address)
		if err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
			t.Errorf("ValidDebuggerAddress(%q) err = %v, want invalid_request", address, err)
		}
	}
}

// Address breakpoints go after the path breakpoints and before the method
// breakpoints; their results carry the address and address: true.
func TestBeginSendsAddressBreakpointsAfterPathBreakpoints(t *testing.T) {
	const address = "jdt://contents/java.base/java.util/ArrayList.class?=x"
	fa, conn := functionDataFake(t, fullFunctionDataCaps, func(string) (any, string) {
		return map[string]any{"breakpoints": []any{map[string]any{"verified": true}}}, ""
	})
	fa.Handle("setBreakpoints", func(req Message) {
		if strings.Contains(string(req.Arguments), address) {
			fa.Respond(req, false, nil, "class not found")
			return
		}
		fa.Respond(req, true, map[string]any{"breakpoints": []any{map[string]any{"verified": true}}}, "")
	})
	params := functionDataParams()
	params.AddressBreakpoints = map[string][]SourceBreakpoint{
		address:       {{Line: 10, Condition: "size == 1"}},
		"abc://other": {{Line: 2}},
	}
	rec := newSessionRecorder()
	beginWithFunctionData(t, conn, rec, params)

	want := []string{"initialize", "launch", "setBreakpoints", "setBreakpoints", "setBreakpoints", "setFunctionBreakpoints", "setDataBreakpoints", "setExceptionBreakpoints", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	reqs := fa.Requests("setBreakpoints")
	if got := string(reqs[0].Arguments); !strings.Contains(got, `"path":"/proj/A.java"`) {
		t.Errorf("first setBreakpoints = %s, want the path breakpoints", got)
	}
	if got := string(reqs[1].Arguments); got != `{"breakpoints":[{"line":2}],"source":{"path":"abc://other"}}` {
		t.Errorf("second setBreakpoints = %s", got)
	}
	if got := string(reqs[2].Arguments); got != `{"breakpoints":[{"line":10,"condition":"size == 1"}],"source":{"path":"`+address+`"}}` {
		t.Errorf("third setBreakpoints = %s", got)
	}

	results := map[string]map[string]json.RawMessage{}
	for len(rec.events) > 0 {
		ev := <-rec.events
		if ev.Event != BreakpointsSetEvent {
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatalf("event body %s: %v", ev.Body, err)
		}
		if _, ok := body["breakpoints"]; ok {
			var path string
			_ = json.Unmarshal(body["path"], &path)
			results[path] = body
		}
	}
	if file := results["/proj/A.java"]; file == nil || file["address"] != nil {
		t.Errorf("path result = %v, want no address flag", file)
	}
	if other := results["abc://other"]; other == nil || string(other["address"]) != "true" || string(other["breakpoints"]) != `[{"verified":true}]` {
		t.Errorf("address result = %v", other)
	}
	failed := results[address]
	if failed == nil || string(failed["address"]) != "true" || string(failed["breakpoints"]) != "null" || string(failed["error"]) != `"class not found"` {
		t.Errorf("failed address result = %v", failed)
	}
}

func TestBeginSkipsAddressBreakpointsForNoDebug(t *testing.T) {
	fa, conn := functionDataFake(t, fullFunctionDataCaps, func(string) (any, string) { return map[string]any{"breakpoints": []any{}}, "" })
	params := functionDataParams()
	params.Arguments = json.RawMessage(`{"noDebug":true}`)
	params.AddressBreakpoints = map[string][]SourceBreakpoint{"abc://x": {{Line: 1}}}
	beginWithFunctionData(t, conn, nil, params)
	if got, want := fa.Commands(), []string{"initialize", "launch", "configurationDone"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestDebugSetBreakpointsAddress(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("setBreakpoints", func(req Message) {
			fa.Respond(req, true, json.RawMessage(`{"breakpoints":[{"verified":false,"line":7}]}`), "")
		})
	})
	const address = "jdt://contents/java.base/java.util/ArrayList.class?=x"
	out, err := dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId": s.ID(), "address": address, "breakpoints": []map[string]any{{"line": 7, "condition": "n == 1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"breakpoints":[{"verified":false,"line":7}]}` {
		t.Fatalf("body = %s", out)
	}
	reqs := fa.Requests("setBreakpoints")
	if len(reqs) != 1 {
		t.Fatalf("setBreakpoints requests = %d", len(reqs))
	}
	var args struct {
		Source map[string]string `json:"source"`
	}
	if err := json.Unmarshal(reqs[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args.Source, map[string]string{"path": address}) {
		t.Fatalf("source = %v", args.Source)
	}
	// Clearing sends an empty array.
	if _, err := dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "address": address}); err != nil {
		t.Fatal(err)
	}
	if reqs = fa.Requests("setBreakpoints"); len(reqs) != 2 || !strings.Contains(string(reqs[1].Arguments), `"breakpoints":[]`) {
		t.Fatalf("clear request = %v", reqs)
	}

	file := root + "/main.py"
	for _, args := range []map[string]any{
		{"sessionId": s.ID(), "path": file, "address": address}, // both
		{"sessionId": s.ID()}, // neither
		{"sessionId": s.ID(), "address": "file:///etc/passwd"},                 // file scheme
		{"sessionId": s.ID(), "address": "FILE://x"},                           // file scheme, any case
		{"sessionId": s.ID(), "address": "/etc/passwd"},                        // a path is not an address
		{"sessionId": s.ID(), "address": "relative"},                           // no scheme
		{"sessionId": s.ID(), "address": "jdt://a\nb"},                         // control character
		{"sessionId": s.ID(), "address": "jdt://a\u202eb"},                     // bidi override
		{"sessionId": s.ID(), "address": "jdt://" + strings.Repeat("a", 4096)}, // too long
		{"sessionId": s.ID(), "address": address, "breakpoints": []map[string]any{{"line": 0}}},
	} {
		_, err := dispatch(t, "debug_set_breakpoints", args)
		wantErrContains(t, err, "invalid_request")
	}
	if got := len(fa.Requests("setBreakpoints")); got != 2 {
		t.Fatalf("adapter received %d setBreakpoints, want 2", got)
	}
}
