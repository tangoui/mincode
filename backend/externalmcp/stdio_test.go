package externalmcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStdioControlListAndCalls(t *testing.T) {
	controlToken := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+controlToken {
			t.Errorf("unexpected authorization")
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/tools/list":
			fmt.Fprint(w, `{"tools":[{"name":"sample","description":"works","inputSchema":{"type":"object"}}]}`)
		case "/v1/tools/call":
			if strings.Contains(string(body), "missing") {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"tool not found"}`)
			} else {
				if string(body) != `{"name":"sample","arguments":{"value":42}}` {
					t.Errorf("unexpected tool request: %s", body)
				}
				fmt.Fprint(w, `{"content":[{"type":"image","mimeType":"image/png","data":"AQ=="}],"isError":false}`)
			}
		case "/v1/panes/send":
			if string(body) != `{"pane":"p","text":"input","submit":true}` {
				t.Errorf("unexpected pane request: %s", body)
			}
			fmt.Fprint(w, "{}")
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDataDir(t)
	if err := SetControlRecord(server.URL, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetControlRecord("", "") })
	t.Setenv("MINCODE_CONTROL_URL", server.URL)
	t.Setenv("MINCODE_CONTROL_TOKEN", controlToken)
	results := stdioReplies(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sample","arguments":{"value":42}}}`+"\n"+
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pane.send","arguments":{"pane":"p","text":"input","submit":true}}}`+"\n"+
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing"}}`)
	for _, name := range []string{"sample", "pane.split", "pane.tab", "pane.send", "pane.read", "pane.wait", "pane.list", "pane.show", "pane.close"} {
		if !strings.Contains(results[1], `"name":"`+name+`"`) {
			t.Errorf("list is missing %s: %s", name, results[1])
		}
	}
	if !strings.Contains(results[2], `"type":"image"`) || !strings.Contains(results[2], `"data":"AQ=="`) {
		t.Errorf("image reply = %s", results[2])
	}
	if !strings.Contains(results[3], `"text":"{}"`) || strings.Contains(results[3], `"isError":true`) {
		t.Errorf("pane reply = %s", results[3])
	}
	if !strings.Contains(results[4], `"isError":true`) || !strings.Contains(results[4], "tool not found") {
		t.Errorf("MCP error reply = %s", results[4])
	}
}

func TestStdioControlOnlyRendezvous(t *testing.T) {
	useTestDataDir(t)
	t.Setenv("MINCODE_CONTROL_URL", "")
	t.Setenv("MINCODE_CONTROL_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tools":[]}`)
	}))
	defer server.Close()
	if err := SetControlRecord(server.URL, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetControlRecord("", "") })
	got := stdioReplies(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)[1]
	if !strings.Contains(got, "pane.list") || strings.Contains(got, "workspace.search_files") {
		t.Fatalf("tool list = %s", got)
	}
}

func stdioReplies(t *testing.T, input string) map[int]string {
	t.Helper()
	var out bytes.Buffer
	if err := RunStdio(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	results := make(map[int]string)
	for decoder.More() {
		var res struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := decoder.Decode(&res); err != nil {
			t.Fatal(err)
		}
		results[res.ID] = string(res.Result) + string(res.Error)
	}
	return results
}

func useTestDataDir(t *testing.T) {
	t.Helper()
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
}
