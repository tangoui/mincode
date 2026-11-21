package debug

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"
)

// functionDataFake scripts an adapter that sends `initialized` right after
// initialize with caps, and answers setFunctionBreakpoints /
// setDataBreakpoints with reply (nil body + non-empty msg = failure).
func functionDataFake(t *testing.T, caps map[string]any, reply func(command string) (body any, msg string)) (*fakeAdapter, io.ReadWriteCloser) {
	t.Helper()
	fa, conn := newFakeAdapter(t)
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, caps, "")
		fa.SendEvent("initialized", nil)
	})
	for _, command := range []string{"setFunctionBreakpoints", "setDataBreakpoints"} {
		fa.Handle(command, func(req Message) {
			body, msg := reply(command)
			fa.Respond(req, msg == "", body, msg)
		})
	}
	fa.Start()
	return fa, conn
}

func beginWithFunctionData(t *testing.T, conn io.ReadWriteCloser, rec *sessionRecorder, params BeginParams) {
	t.Helper()
	opts := SessionOptions{ID: "s1"}
	if rec != nil {
		opts = rec.options("s1")
	}
	s := newSession(conn, opts)
	t.Cleanup(func() { s.Close("test done") })
	if _, err := s.Begin(testCtx(t), params); err != nil {
		t.Fatalf("Begin: %v", err)
	}
}

var fullFunctionDataCaps = map[string]any{
	"supportsConfigurationDoneRequest": true,
	"supportsFunctionBreakpoints":      true,
	"supportsDataBreakpoints":          true,
}

func functionDataParams() BeginParams {
	return BeginParams{
		AdapterID:   "java",
		Request:     "launch",
		Breakpoints: map[string][]SourceBreakpoint{"/proj/A.java": {{Line: 3}}},
		FunctionBreakpoints: []FunctionBreakpoint{
			{Name: "pkg.A.run"},
			{Name: "pkg.A.stop", Condition: "x == 1", HitCondition: "2"},
		},
		DataBreakpoints: []DataBreakpoint{
			{DataID: "field-1", AccessType: "write"},
			{DataID: "field-2", Condition: "v == 3", HitCondition: "4"},
		},
		ExceptionFilters: []string{"uncaught"},
	}
}

func TestBeginSendsFunctionAndDataBreakpointsInOrder(t *testing.T) {
	fa, conn := functionDataFake(t, fullFunctionDataCaps, func(string) (any, string) {
		return map[string]any{"breakpoints": []map[string]any{{"verified": true}, {"verified": false, "message": "class not loaded"}}}, ""
	})
	beginWithFunctionData(t, conn, nil, functionDataParams())

	want := []string{"initialize", "launch", "setBreakpoints", "setFunctionBreakpoints", "setDataBreakpoints", "setExceptionBreakpoints", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	if got := string(fa.Requests("setFunctionBreakpoints")[0].Arguments); got != `{"breakpoints":[{"name":"pkg.A.run"},{"name":"pkg.A.stop","condition":"x == 1","hitCondition":"2"}]}` {
		t.Errorf("setFunctionBreakpoints arguments = %s", got)
	}
	if got := string(fa.Requests("setDataBreakpoints")[0].Arguments); got != `{"breakpoints":[{"dataId":"field-1","accessType":"write"},{"dataId":"field-2","condition":"v == 3","hitCondition":"4"}]}` {
		t.Errorf("setDataBreakpoints arguments = %s", got)
	}
}

func TestBeginSkipsFunctionAndDataBreakpointsWithoutCapabilityOrEntries(t *testing.T) {
	reply := func(string) (any, string) { return map[string]any{"breakpoints": []any{}}, "" }
	base := []string{"initialize", "launch", "setBreakpoints", "setExceptionBreakpoints", "configurationDone"}

	t.Run("capabilities false", func(t *testing.T) {
		fa, conn := functionDataFake(t, map[string]any{
			"supportsConfigurationDoneRequest": true,
			"supportsFunctionBreakpoints":      false,
		}, reply)
		beginWithFunctionData(t, conn, nil, functionDataParams())
		if got := fa.Commands(); !reflect.DeepEqual(got, base) {
			t.Fatalf("commands = %v, want %v", got, base)
		}
	})
	t.Run("empty lists", func(t *testing.T) {
		fa, conn := functionDataFake(t, fullFunctionDataCaps, reply)
		params := functionDataParams()
		params.FunctionBreakpoints, params.DataBreakpoints = []FunctionBreakpoint{}, nil
		beginWithFunctionData(t, conn, nil, params)
		if got := fa.Commands(); !reflect.DeepEqual(got, base) {
			t.Fatalf("commands = %v, want %v", got, base)
		}
	})
	t.Run("only function breakpoints supported", func(t *testing.T) {
		fa, conn := functionDataFake(t, map[string]any{
			"supportsConfigurationDoneRequest": true,
			"supportsFunctionBreakpoints":      true,
		}, reply)
		beginWithFunctionData(t, conn, nil, functionDataParams())
		want := []string{"initialize", "launch", "setBreakpoints", "setFunctionBreakpoints", "setExceptionBreakpoints", "configurationDone"}
		if got := fa.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %v, want %v", got, want)
		}
	})
	t.Run("noDebug", func(t *testing.T) {
		fa, conn := functionDataFake(t, fullFunctionDataCaps, reply)
		params := functionDataParams()
		params.Arguments = json.RawMessage(`{"noDebug":true}`)
		beginWithFunctionData(t, conn, nil, params)
		want := []string{"initialize", "launch", "configurationDone"}
		if got := fa.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %v, want %v", got, want)
		}
	})
}

// A wrong method name or an unknown field must not stop the launch: the
// failure is reported like a failed setBreakpoints and the sequence goes on.
func TestBeginContinuesWhenFunctionOrDataBreakpointsFail(t *testing.T) {
	fa, conn := functionDataFake(t, fullFunctionDataCaps, func(command string) (any, string) {
		if command == "setFunctionBreakpoints" {
			return nil, "bad method name"
		}
		return map[string]any{"breakpoints": []map[string]any{{"verified": true, "id": 7}, {"verified": false}}}, ""
	})
	rec := newSessionRecorder()
	beginWithFunctionData(t, conn, rec, functionDataParams())

	want := []string{"initialize", "launch", "setBreakpoints", "setFunctionBreakpoints", "setDataBreakpoints", "setExceptionBreakpoints", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
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
		for _, key := range []string{"functionBreakpoints", "dataBreakpoints"} {
			if _, ok := body[key]; ok {
				results[key] = body
			}
		}
	}
	fn, data := results["functionBreakpoints"], results["dataBreakpoints"]
	if fn == nil || string(fn["path"]) != `""` || string(fn["functionBreakpoints"]) != `null` || string(fn["error"]) != `"bad method name"` {
		t.Errorf("function breakpoint result = %v", fn)
	}
	if data == nil || string(data["path"]) != `""` || string(data["dataBreakpoints"]) != `[{"id":7,"verified":true},{"verified":false}]` {
		t.Errorf("data breakpoint result = %v", data)
	}
	if _, ok := data["error"]; ok {
		t.Errorf("data breakpoints have error: %s", data["error"])
	}
	if _, ok := data["breakpoints"]; ok {
		t.Errorf("data breakpoint result uses breakpoints key: %v", data)
	}
}

// The host keeps the DAP standard request and event names so a plugin cannot
// declare one of them as its custom hot code replace event or request.
func TestIsStandardDAPName(t *testing.T) {
	requests := []string{
		"initialize", "launch", "attach", "restart", "disconnect", "terminate", "breakpointLocations",
		"setBreakpoints", "setFunctionBreakpoints", "setExceptionBreakpoints", "dataBreakpointInfo",
		"setDataBreakpoints", "setInstructionBreakpoints", "continue", "next", "stepIn", "stepOut",
		"stepBack", "reverseContinue", "restartFrame", "goto", "pause", "stackTrace", "scopes",
		"variables", "setVariable", "source", "threads", "terminateThreads", "modules", "loadedSources",
		"evaluate", "setExpression", "stepInTargets", "gotoTargets", "completions", "exceptionInfo",
		"readMemory", "writeMemory", "disassemble", "locations", "configurationDone", "cancel",
		"runInTerminal", "startDebugging",
	}
	events := []string{
		"initialized", "stopped", "continued", "exited", "terminated", "thread", "output", "breakpoint",
		"module", "loadedSource", "process", "capabilities", "progressStart", "progressUpdate",
		"progressEnd", "invalidated", "memory",
	}
	for _, name := range append(requests, events...) {
		if !IsStandardDAPName(name) {
			t.Errorf("%q is not reported as a DAP standard name", name)
		}
	}
	for _, name := range []string{"Continue", "STOPPED", "setdatabreakpoints", "RunInTerminal"} {
		if !IsStandardDAPName(name) {
			t.Errorf("%q differs only in case and must be reported", name)
		}
	}
	for _, name := range []string{"hotcodereplace", "redefineClasses", "codeReloaded", "", "continue2"} {
		if IsStandardDAPName(name) {
			t.Errorf("%q is reported as a DAP standard name", name)
		}
	}
}
