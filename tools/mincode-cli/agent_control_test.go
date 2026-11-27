package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mincode/app/backend/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testControlToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestAgentControlCommands(t *testing.T) {
	cases := []struct {
		name                    string
		args                    []string
		path, body, reply, want string
	}{
		{"tool list", []string{"tool", "list", "--json"}, "/v1/tools/list", "{}", `{"tools":[{"name":"sample"}]}`, `{"tools":[{"name":"sample"}]}`},
		{"tool call", []string{"tool", "call", "sample", "--args", `{"n":42}`}, "/v1/tools/call", `{"name":"sample","arguments":{"n":42}}`, `{"content":[{"type":"text","text":"done"}],"isError":false}`, `"text":"done"`},
		{"split", []string{"pane", "split", "--direction", "right", "--cwd", "/work", "--title", "worker", "--command", "echo hi", "--pane", "p1", "--json"}, "/v1/panes/split", `{"pane":"p1","direction":"right","cwd":"/work","title":"worker","command":"echo hi"}`, `{"id":"p2","state":"idle"}`, `"id":"p2"`},
		{"tab", []string{"pane", "tab", "--cwd", "/work", "--command", "echo hi"}, "/v1/panes/tab", `{"cwd":"/work","command":"echo hi"}`, `{"id":"p2","title":"worker"}`, "p2\tworker"},
		{"send", []string{"pane", "send", "--pane", "p2", "hello", "--submit"}, "/v1/panes/send", `{"pane":"p2","text":"hello","submit":true}`, "{}", ""},
		{"read", []string{"pane", "read", "--pane", "p2", "--lines=3"}, "/v1/panes/read", `{"pane":"p2","lines":3}`, `{"text":"one\ntwo"}`, "one\ntwo"},
		{"wait", []string{"pane", "wait", "--pane", "p2", "--timeout-secs", "15"}, "/v1/panes/wait", `{"pane":"p2","timeoutSecs":15}`, `{"state":"idle"}`, "idle"},
		{"list", []string{"pane", "list"}, "/v1/panes/list", "{}", `{"panes":[{"id":"p2","title":"worker","state":"working","cwd":"/work"}]}`, "p2\tworker\tworking\t/work"},
		{"show", []string{"pane", "show", "--pane", "p2"}, "/v1/panes/show", `{"pane":"p2"}`, `{"id":"p2","title":"worker","state":"working","cwd":"/work"}`, "p2\tworker\tworking\t/work"},
		{"close", []string{"pane", "close", "--pane", "p2", "--json"}, "/v1/panes/close", `{"pane":"p2"}`, "{}", "{}"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != test.path || r.Header.Get("Authorization") != "Bearer "+testControlToken {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var got, want any
				_ = json.Unmarshal([]byte(test.body), &want)
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("body = %#v, want %#v, err = %v", got, want, err)
				}
				fmt.Fprint(w, test.reply)
			}))
			defer server.Close()
			t.Setenv("MINCODE_CONTROL_URL", server.URL)
			t.Setenv("MINCODE_CONTROL_TOKEN", testControlToken)
			var output bytes.Buffer
			if err := runAgentControl(context.Background(), test.args, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestAgentControlExitCodes(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		status int
		reply  string
		code   int
	}{
		{"invalid args", []string{"pane", "split", "--direction", "left"}, 200, "{}", 2},
		{"missing pane", []string{"pane", "read"}, 200, "{}", 2},
		{"unavailable", []string{"tool", "list"}, 0, "", 3},
		{"not found", []string{"pane", "read", "--pane", "missing"}, 404, `{"error":"pane not found"}`, 4},
		{"bad token", []string{"tool", "list"}, 401, `{"error":"invalid token"}`, 5},
		{"disabled", []string{"tool", "list"}, 403, `{"error":"external access disabled"}`, 5},
		{"excluded", []string{"tool", "call", "ask_user"}, 409, `{"error":"excluded tool"}`, 5},
		{"not ready", []string{"tool", "list"}, 503, `{"error":"MinCode interface is not ready"}`, 1},
		{"HTTP timeout", []string{"tool", "list"}, 504, `{"error":"timeout"}`, 9},
		{"pane timeout", []string{"pane", "wait", "--pane", "p"}, 200, `{"state":"timeout"}`, 9},
		{"waiting input", []string{"pane", "wait", "--pane", "p"}, 200, `{"state":"waiting_input"}`, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.reply)
			}))
			if test.status == 0 {
				server.Close()
			} else {
				defer server.Close()
			}
			t.Setenv("MINCODE_CONTROL_URL", server.URL)
			t.Setenv("MINCODE_CONTROL_TOKEN", testControlToken)
			err := runAgentControl(context.Background(), test.args, io.Discard)
			if code := commandExitCode(err); code != test.code {
				t.Fatalf("status = %d, want %d, error = %v", code, test.code, err)
			}
		})
	}
}

func TestAgentControlRendezvousAndEnvironmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINCODE_DATA_DIR", dir)
	t.Setenv("MINCODE_CONTROL_URL", "")
	t.Setenv("MINCODE_CONTROL_TOKEN", "")
	recordDir := filepath.Join(dir, core.AppDataNamespace, "external-mcp")
	if err := os.MkdirAll(recordDir, 0700); err != nil {
		t.Fatal(err)
	}
	const recordToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	record := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"invalid MinCode control token"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+recordToken {
			t.Error("record token missing")
		}
		fmt.Fprint(w, `{"tools":[{"name":"from-file"}]}`)
	}))
	defer record.Close()
	pane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testControlToken {
			t.Error("pane token missing")
		}
		fmt.Fprint(w, `{"tools":[{"name":"from-pane"}]}`)
	}))
	defer pane.Close()
	if err := os.WriteFile(filepath.Join(recordDir, "running.json"), []byte(fmt.Sprintf(`{"url":%q}`, record.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runAgentControl(context.Background(), []string{"tool", "list"}, io.Discard); commandExitCode(err) != 5 {
		t.Fatalf("running with trust disabled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "current.json"), []byte(fmt.Sprintf(`{"controlUrl":%q,"controlToken":%q}`, record.URL, recordToken)), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAgentControl(context.Background(), []string{"tool", "list"}, &output); err != nil || !strings.Contains(output.String(), "from-file") {
		t.Fatalf("file discovery: %q, %v", output.String(), err)
	}
	t.Setenv("MINCODE_CONTROL_URL", pane.URL)
	t.Setenv("MINCODE_CONTROL_TOKEN", testControlToken)
	output.Reset()
	if err := runAgentControl(context.Background(), []string{"tool", "list"}, &output); err != nil || !strings.Contains(output.String(), "from-pane") {
		t.Fatalf("environment discovery: %q, %v", output.String(), err)
	}
}

func TestAgentControlArgsFileAndImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !bytes.Contains(data, []byte(`"arguments":{"quality":80}`)) {
			t.Errorf("argument file was not sent: %s", data)
		}
		fmt.Fprint(w, `{"content":[{"type":"image","mimeType":"image/png","data":"AQID"}],"isError":false}`)
	}))
	defer server.Close()
	t.Setenv("MINCODE_CONTROL_URL", server.URL)
	t.Setenv("MINCODE_CONTROL_TOKEN", testControlToken)
	path := filepath.Join(t.TempDir(), "args.json")
	if err := os.WriteFile(path, []byte(`{"quality":80}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAgentControl(context.Background(), []string{"tool", "call", "screenshot", "--args-file", path}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Content []struct {
			Path string `json:"path"`
		}
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || len(result.Content) != 1 || result.Content[0].Path == "" {
		t.Fatalf("missing image file: %s, %v", output.String(), err)
	}
	t.Cleanup(func() { _ = os.Remove(result.Content[0].Path) })
	image, err := os.ReadFile(result.Content[0].Path)
	if err != nil || !bytes.Equal(image, []byte{1, 2, 3}) || strings.Contains(output.String(), "AQID") {
		t.Fatalf("image file = %v, %v", image, err)
	}
}
