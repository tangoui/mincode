package debug

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func scriptLogpointAdapter(fa *fakeAdapter, supported *bool) {
	fa.Handle("initialize", func(req Message) {
		caps := map[string]any{}
		if supported != nil {
			caps["supportsLogPoints"] = *supported
		}
		fa.Respond(req, true, caps, "")
	})
	fa.Handle("launch", func(req Message) {
		fa.SendEvent("initialized", nil)
		fa.Respond(req, true, nil, "")
	})
	fa.Handle("setBreakpoints", func(req Message) {
		var args struct {
			Breakpoints []map[string]any `json:"breakpoints"`
		}
		_ = json.Unmarshal(req.Arguments, &args)
		results := make([]map[string]any, 0, len(args.Breakpoints))
		for _, bp := range args.Breakpoints {
			line := int(bp["line"].(float64))
			results = append(results, map[string]any{"id": line + 100, "line": line + 10, "column": 2, "verified": true, "message": "moved"})
		}
		fa.Respond(req, true, map[string]any{"breakpoints": results, "adapterMetadata": "kept"}, "")
	})
}

func logpointSource(t *testing.T, root string) string {
	t.Helper()
	file := filepath.Join(root, "logpoints.py")
	if err := os.WriteFile(file, []byte("value = 5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLogpointsBeginAndLive(t *testing.T) {
	yes, no := true, false
	for _, capability := range []struct {
		name      string
		supported *bool
	}{{"supported", &yes}, {"unsupported", &no}, {"not advertised", nil}} {
		for _, phase := range []string{"begin", "live"} {
			t.Run(capability.name+"/"+phase, func(t *testing.T) {
				rec := captureEmits(t)
				fa, s, root := registerScripted(t, func(fa *fakeAdapter) { scriptLogpointAdapter(fa, capability.supported) })
				file := logpointSource(t, root)
				input := []map[string]any{
					{"line": 3, "condition": "value > 0", "hitCondition": "2", "logMessage": "  value={value} <tag>  "},
					{"line": 7, "condition": "value == 5"},
					{"line": 9, "logMessage": "second={value}"},
				}
				b := BeginParams{AdapterID: "fixture", Request: "launch"}
				if phase == "begin" {
					raw, _ := json.Marshal(input)
					var breakpoints []SourceBreakpoint
					if err := json.Unmarshal(raw, &breakpoints); err != nil {
						t.Fatal(err)
					}
					b.Breakpoints = map[string][]SourceBreakpoint{file: breakpoints}
				}
				if _, err := s.Begin(testCtx(t), b); err != nil {
					t.Fatal(err)
				}
				var response string
				if phase == "live" {
					var err error
					response, err = dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "path": file, "breakpoints": input})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					ev := rec.waitEvent(testCtx(t), t, s.ID(), BreakpointsSetEvent)
					response = string(ev.Body)
				}
				requests := fa.Requests("setBreakpoints")
				if len(requests) != 1 {
					t.Fatalf("requests = %d", len(requests))
				}
				var sent struct {
					Breakpoints []map[string]any `json:"breakpoints"`
				}
				if err := json.Unmarshal(requests[0].Arguments, &sent); err != nil {
					t.Fatal(err)
				}
				supported := capability.supported != nil && *capability.supported
				if supported {
					if len(sent.Breakpoints) != 3 || sent.Breakpoints[0]["logMessage"] != "  value={value} <tag>  " {
						t.Fatalf("lost log text or whitespace: %s", requests[0].Arguments)
					}
					if sent.Breakpoints[0]["condition"] != "value > 0" || sent.Breakpoints[0]["hitCondition"] != "2" {
						t.Fatal("lost condition fields")
					}
				} else if len(sent.Breakpoints) != 1 || sent.Breakpoints[0]["line"] != float64(7) {
					t.Fatalf("unsupported logpoints must never become stopping breakpoints: %s", requests[0].Arguments)
				}
				var body struct {
					Breakpoints     []map[string]any `json:"breakpoints"`
					AdapterMetadata string           `json:"adapterMetadata"`
				}
				if err := json.Unmarshal([]byte(response), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Breakpoints) != 3 {
					t.Fatalf("lost requested ordering: %s", response)
				}
				if bp := body.Breakpoints[1]; bp["id"] != float64(107) || bp["line"] != float64(17) || bp["column"] != float64(2) || bp["verified"] != true {
					t.Fatalf("ordinary breakpoint metadata shifted: %s", response)
				}
				if phase == "live" && body.AdapterMetadata != "kept" {
					t.Fatalf("lost adapter body metadata: %s", response)
				}
				if !supported {
					for _, index := range []int{0, 2} {
						bp := body.Breakpoints[index]
						if bp["verified"] != false || bp["message"] != "unsupported: logpoints" || bp["line"] != float64(input[index]["line"].(int)) {
							t.Fatalf("missing unverified logpoint reason: %s", response)
						}
					}
				}
			})
		}
	}
}

func TestUnsupportedLogpointClearsPreviouslyStoppingBreakpoint(t *testing.T) {
	captureEmits(t)
	no := false
	fa, s, root := registerScripted(t, func(fa *fakeAdapter) { scriptLogpointAdapter(fa, &no) })
	file := logpointSource(t, root)
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fixture", Request: "launch"}); err != nil {
		t.Fatal(err)
	}
	for _, bps := range [][]map[string]any{{{"line": 3}}, {{"line": 3, "logMessage": "value={value}"}}} {
		if _, err := dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "path": file, "breakpoints": bps}); err != nil {
			t.Fatal(err)
		}
	}
	requests := fa.Requests("setBreakpoints")
	var args struct {
		Breakpoints []json.RawMessage `json:"breakpoints"`
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if err := json.Unmarshal(requests[1].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args.Breakpoints == nil || len(args.Breakpoints) != 0 {
		t.Fatalf("must explicitly clear old stopping breakpoint: %s", requests[1].Arguments)
	}
}

func TestBlankLogMessageRemainsOrdinaryBreakpoint(t *testing.T) {
	captureEmits(t)
	no := false
	fa, s, root := registerScripted(t, func(fa *fakeAdapter) { scriptLogpointAdapter(fa, &no) })
	file := logpointSource(t, root)
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fixture", Request: "launch"}); err != nil {
		t.Fatal(err)
	}
	response, err := dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "path": file, "breakpoints": []map[string]any{{"line": 3, "logMessage": " \t "}}})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Breakpoints []map[string]any `json:"breakpoints"`
	}
	_ = json.Unmarshal(fa.Requests("setBreakpoints")[0].Arguments, &sent)
	if len(sent.Breakpoints) != 1 || sent.Breakpoints[0]["logMessage"] != nil {
		t.Fatalf("blank message should be omitted: %+v", sent)
	}
	var body struct {
		Breakpoints []struct {
			Verified bool `json:"verified"`
		} `json:"breakpoints"`
	}
	_ = json.Unmarshal([]byte(response), &body)
	if len(body.Breakpoints) != 1 || !body.Breakpoints[0].Verified {
		t.Fatalf("ordinary breakpoint not verified: %s", response)
	}
}

func TestNoDebugConfiguresWithoutInstallingAnyBreakpoints(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	var launch Message
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true, "supportsLogPoints": true}, "")
	})
	fa.Handle("launch", func(req Message) { launch = req; fa.SendEvent("initialized", nil) })
	fa.Handle("configurationDone", func(req Message) { fa.Respond(req, true, nil, ""); fa.Respond(launch, true, nil, "") })
	fa.Start()
	s := newSession(conn, SessionOptions{ID: "run-with-logpoints"})
	defer s.Close("test done")
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fixture", Request: "launch", Arguments: json.RawMessage(`{"noDebug":true}`), Breakpoints: map[string][]SourceBreakpoint{"/p/app.py": {{Line: 2, LogMessage: "value={value}"}, {Line: 3}}}, ExceptionFilters: []string{"raised"}}); err != nil {
		t.Fatal(err)
	}
	commands := fa.Commands()
	if len(commands) != 3 || commands[0] != "initialize" || commands[1] != "launch" || commands[2] != "configurationDone" {
		t.Fatalf("noDebug must configure the adapter without installing breakpoints: %v", commands)
	}
}
