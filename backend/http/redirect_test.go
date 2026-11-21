package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const redirectTestSecret = "secret-key"
const redirectTestBody = "top-secret-body"

func sendViaHandler(t *testing.T, target, method string) (string, error) {
	t.Helper()
	body := redirectTestBody
	args, err := json.Marshal(map[string]any{
		"payload": map[string]any{
			"method":  method,
			"url":     target,
			"headers": [][2]string{{"X-Api-Key", redirectTestSecret}, {"Authorization", "Bearer " + redirectTestSecret}},
			"body":    body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return httpSendRequestHandler(context.Background(), "", string(args))
}

// leakRecorder 记录重定向目标是否被访问、以及是否收到了凭据或请求体。
type leakRecorder struct {
	hits   atomic.Int32
	leaked atomic.Bool
}

func (l *leakRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" || strings.Contains(string(b), redirectTestBody) {
			l.leaked.Store(true)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "ok")
	})
}

func TestHTTPSendRejectsCrossOriginRedirect(t *testing.T) {
	statuses := []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, status := range statuses {
		rec := &leakRecorder{}
		other := httptest.NewServer(rec.handler())
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/steal", status)
		}))

		_, err := sendViaHandler(t, origin.URL+"/start", http.MethodPost)
		origin.Close()
		other.Close()
		if err == nil || !strings.Contains(err.Error(), "跨域重定向") {
			t.Fatalf("status %d: cross-origin redirect must fail, got %v", status, err)
		}
		if rec.hits.Load() != 0 || rec.leaked.Load() {
			t.Fatalf("status %d: target must not be contacted (hits=%d leaked=%v)", status, rec.hits.Load(), rec.leaked.Load())
		}
	}
}

func TestHTTPSendFollowsSameOriginRedirect(t *testing.T) {
	var gotKey, gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotKey.Store(r.Header.Get("X-Api-Key"))
		gotBody.Store(string(b))
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	out, err := sendViaHandler(t, srv.URL+"/old", http.MethodPost)
	if err != nil {
		t.Fatalf("same-origin redirect must succeed: %v", err)
	}
	var resp httpResponsePayload
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusOK || resp.Body != "done" {
		t.Fatalf("unexpected response %+v", resp)
	}
	if gotKey.Load() != redirectTestSecret || gotBody.Load() != redirectTestBody {
		t.Fatalf("same-origin 307 should keep headers/body, got key=%v body=%v", gotKey.Load(), gotBody.Load())
	}
}

func TestSSEConnectRejectsCrossOriginRedirect(t *testing.T) {
	rec := &leakRecorder{}
	other := httptest.NewServer(rec.handler())
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/events", http.StatusFound)
	}))
	defer origin.Close()

	args, _ := json.Marshal(map[string]any{
		"sessionId": "redirect-test-sse",
		"url":       origin.URL + "/events",
		"headers":   [][2]string{{"X-Api-Key", redirectTestSecret}},
	})
	_, err := sseConnectHandler("", string(args))
	if err == nil || !strings.Contains(err.Error(), "跨域重定向") {
		t.Fatalf("SSE cross-origin redirect must fail, got %v", err)
	}
	if rec.hits.Load() != 0 || rec.leaked.Load() {
		t.Fatalf("SSE redirect target must not be contacted (hits=%d leaked=%v)", rec.hits.Load(), rec.leaked.Load())
	}
	if getStreamSession("redirect-test-sse") != nil {
		t.Fatal("failed SSE session must be cleaned up")
	}
}

func TestCheckSameOriginRedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	first := mk("https://api.example.com/v1")
	cases := map[string]bool{
		"https://api.example.com/v2":        true,
		"https://API.example.com:443/v2":    true,
		"http://api.example.com/v1":         false, // 降级
		"https://api.example.com:8443/v1":   false, // 不同端口
		"https://evil.example.net/v1":       false, // 不同主机
		"https://sub.api.example.com/v1":    false, // 子域也不算同源
		"http://169.254.169.254/latest":     false,
		"https://metadata.google.internal/": false,
	}
	for target, ok := range cases {
		err := checkSameOriginRedirect(mk(target), []*http.Request{first})
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v err=%v", target, err == nil, err)
		}
	}

	plainFirst := mk("http://localhost:8080/a")
	if err := checkSameOriginRedirect(mk("http://localhost:8080/b"), []*http.Request{plainFirst}); err != nil {
		t.Errorf("same-origin http redirect should pass: %v", err)
	}
	if err := checkSameOriginRedirect(mk("http://localhost/b"), []*http.Request{plainFirst}); err == nil {
		t.Error("port change must be rejected")
	}

	via := make([]*http.Request, httpMaxRedirects)
	for i := range via {
		via[i] = first
	}
	if err := checkSameOriginRedirect(mk("https://api.example.com/again"), via); err == nil {
		t.Fatal("redirect chain must be bounded")
	}
}
