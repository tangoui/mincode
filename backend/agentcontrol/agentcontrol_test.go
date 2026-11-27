package agentcontrol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func testControl(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MINCODE_DATA_DIR", dir)
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Shutdown(); core.SetEmitter(nil) })
	url, token := IssuePaneToken("first")
	if !strings.HasPrefix(url, "http://127.0.0.1:") || len(token) != 64 {
		t.Fatal("missing loopback address or pane token")
	}
	return url, token, filepath.Join(dir, core.AppDataNamespace, "external-mcp", "current.json")
}

func frontend(t *testing.T, cmd, body string) string {
	t.Helper()
	out, err := core.Dispatch(cmd, body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requestControl(t *testing.T, url, route, token, body string, want int) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s returned %d, want %d: %s", route, response.StatusCode, want, data)
	}
	return string(data)
}

const toolFixture = `{"tools":[{"name":"sample","description":"test","inputSchema":{"type":"object"}},{"name":"ask_user"},{"name":"agent.run_parallel"},{"name":"agent.integrate_result"}]}`

func TestAuthorizationAndReadiness(t *testing.T) {
	url, pane, record := testControl(t)
	requestControl(t, url, "/v1/tools/list", "", "{}", 401)
	requestControl(t, url, "/v1/tools/list", "invalid", "{}", 401)
	requestControl(t, url, "/v1/tools/list", pane, "{}", 503)
	frontend(t, "agent_control_ready", toolFixture)
	list := requestControl(t, url, "/v1/tools/list", pane, "{}", 200)
	if !strings.Contains(list, "sample") || strings.Contains(list, "ask_user") || strings.Contains(list, "agent.") {
		t.Fatalf("unexpected tool list: %s", list)
	}
	requestControl(t, url, "/v1/tools/call", pane, `{"name":"ask_user"}`, 409)
	requestControl(t, url, "/v1/tools/call", pane, `{"name":"unknown"}`, 404)
	if err := SetExternalTrusted(true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		URL   string `json:"controlUrl"`
		Token string `json:"controlToken"`
	}
	if err := json.Unmarshal(data, &c); err != nil || c.URL != url || len(c.Token) != 64 || c.Token == pane {
		t.Fatal("invalid external credentials")
	}
	requestControl(t, url, "/v1/tools/list", c.Token, "{}", 200)
	if err := SetExternalTrusted(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("external token record remains: %v", err)
	}
	requestControl(t, url, "/v1/tools/list", c.Token, "{}", 403)
	requestControl(t, url, "/v1/tools/list", pane, "{}", 200)
	RevokePane("first")
	requestControl(t, url, "/v1/tools/list", pane, "{}", 401)
}

func TestFrontendToolAndPaneResults(t *testing.T) {
	url, pane, _ := testControl(t)
	frontend(t, "agent_control_ready", toolFixture)
	core.SetEmitter(func(name string, data any) {
		raw, _ := json.Marshal(data)
		var event struct {
			RequestID string `json:"requestId"`
			Name      string `json:"name"`
			Op        string `json:"op"`
			Pane      string `json:"pane"`
			Origin    struct {
				Kind   string `json:"kind"`
				PaneID string `json:"paneId"`
			} `json:"origin"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Error(err)
			return
		}
		switch name {
		case "agent-control:call":
			if event.Name != "sample" || event.Origin.Kind != "pane" || event.Origin.PaneID != "first" {
				t.Errorf("unexpected tool event: %s", raw)
			}
			if _, err := core.Dispatch("agent_control_result", `{"requestId":"`+event.RequestID+`","content":[{"type":"text","text":"done"}],"isError":false}`); err != nil {
				t.Error(err)
			}
		case "agent-control:pane-op":
			if event.Op != "split" || event.Pane != "first" {
				t.Errorf("unexpected pane event: %s", raw)
			}
			if _, err := core.Dispatch("agent_control_pane_result", `{"requestId":"`+event.RequestID+`","pane":{"id":"second","alive":true,"state":"idle"}}`); err != nil {
				t.Error(err)
			}
		}
	})
	result := requestControl(t, url, "/v1/tools/call", pane, `{"name":"sample","arguments":{"q":1}}`, 200)
	if !strings.Contains(result, `"text":"done"`) || !strings.Contains(result, `"isError":false`) {
		t.Errorf("incorrect tool result: %s", result)
	}
	result = requestControl(t, url, "/v1/panes/split", pane, `{"direction":"right"}`, 200)
	if !strings.Contains(result, `"id":"second"`) {
		t.Errorf("incorrect pane result: %s", result)
	}
	if log := frontend(t, "agent_control_log", "{}"); !strings.Contains(log, "sample") || strings.Contains(log, pane) {
		t.Errorf("unexpected log: %s", log)
	}
}

func TestClientDisconnectAndTimeoutCancelFrontend(t *testing.T) {
	for _, mode := range []string{"disconnect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			_, pane, _ := testControl(t)
			frontend(t, "agent_control_ready", toolFixture)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			timeout := time.Second
			if mode == "timeout" {
				timeout = 20 * time.Millisecond
			}
			var id string
			canceled := false
			core.SetEmitter(func(name string, data any) {
				raw, _ := json.Marshal(data)
				var event struct {
					RequestID string `json:"requestId"`
				}
				_ = json.Unmarshal(raw, &event)
				switch name {
				case "agent-control:call":
					id = event.RequestID
					if mode == "disconnect" {
						stop()
					}
				case "agent-control:cancel":
					canceled = id != "" && event.RequestID == id
				}
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/tools/call", strings.NewReader(`{"name":"sample"}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+pane)
			rec := httptest.NewRecorder()
			control.handle(rec, req, timeout)
			if !canceled || (mode == "timeout" && rec.Code != 504) {
				t.Fatalf("missing cancellation or wrong status: cancel=%v status=%d", canceled, rec.Code)
			}
		})
	}
}

func TestRevokeExternalTrustCancelsPendingTool(t *testing.T) {
	url, _, record := testControl(t)
	frontend(t, "agent_control_ready", toolFixture)
	if err := SetExternalTrusted(true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"controlToken"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	core.SetEmitter(func(name string, data any) {
		switch name {
		case "agent-control:call":
			close(started)
		case "agent-control:cancel":
			close(canceled)
		}
	})
	response := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, url+"/v1/tools/call", strings.NewReader(`{"name":"sample"}`))
		req.Header.Set("Authorization", "Bearer "+c.Token)
		res, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			response <- 0
			return
		}
		res.Body.Close()
		response <- res.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request never reached frontend")
	}
	if err := SetExternalTrusted(false); err != nil {
		t.Fatal(err)
	}
	if status := <-response; status != http.StatusForbidden {
		t.Fatalf("pending request status = %d", status)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("pending request did not cancel frontend")
	}
}

func TestReleasingOldPaneTokenDoesNotRevokeReplacement(t *testing.T) {
	url, old, _ := testControl(t)
	frontend(t, "agent_control_ready", toolFixture)
	_, next := IssuePaneToken("first")
	if next == old {
		t.Fatal("new PTY reused old credentials")
	}
	RevokePaneToken("first", old)
	requestControl(t, url, "/v1/tools/list", old, "{}", 401)
	requestControl(t, url, "/v1/tools/list", next, "{}", 200)
}

func TestRevokePaneCancelsPendingTool(t *testing.T) {
	for _, mode := range []string{"pane", "token"} {
		t.Run(mode, func(t *testing.T) {
			_, token, _ := testControl(t)
			frontend(t, "agent_control_ready", toolFixture)
			started, canceled := make(chan string, 1), make(chan string, 1)
			core.SetEmitter(func(name string, data any) {
				switch name {
				case "agent-control:call":
					started <- data.(map[string]any)["requestId"].(string)
				case "agent-control:cancel":
					canceled <- data.(map[string]string)["requestId"]
				}
			})
			ctx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			var status int
			go func() {
				defer close(done)
				req := httptest.NewRequest(http.MethodPost, "/v1/tools/call", strings.NewReader(`{"name":"sample"}`)).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				control.handle(rec, req, 5*time.Second)
				status = rec.Code
			}()
			defer func() {
				stop()
				<-done
			}()

			var requestID string
			select {
			case requestID = <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not reach the frontend")
			}
			if mode == "pane" {
				RevokePane("first")
			} else {
				RevokePaneToken("first", token)
			}

			select {
			case id := <-canceled:
				if id != requestID {
					t.Fatalf("canceled request = %q, want %q", id, requestID)
				}
			case <-time.After(time.Second):
				t.Fatal("revocation did not cancel the frontend request")
			}
			<-done
			if status != http.StatusUnauthorized {
				t.Fatalf("revoked request status = %d, want %d", status, http.StatusUnauthorized)
			}
		})
	}
}
