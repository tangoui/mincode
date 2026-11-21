package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAnthropicAuthHeaders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		auth          string
		apiKey        string
		wantAPIKey    string
		wantAuthorize string
	}{
		{name: "default", auth: "", apiKey: "k", wantAPIKey: "k"},
		{name: "x-api-key", auth: "x-api-key", apiKey: "k", wantAPIKey: "k"},
		{name: "bearer", auth: "bearer", apiKey: "k", wantAuthorize: "Bearer k"},
		{name: "empty key default", auth: "", apiKey: ""},
		{name: "empty key bearer", auth: "bearer", apiKey: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			headers := anthropicAuthHeaders(&aiStreamConfig{APIKey: tc.apiKey, AnthropicAuth: tc.auth})
			if headers["anthropic-version"] != "2023-06-01" {
				t.Fatalf("anthropic-version missing: %#v", headers)
			}
			apiKey, hasAPIKey := headers["x-api-key"]
			authorization, hasAuthorization := headers["Authorization"]
			if (tc.wantAPIKey != "") != hasAPIKey || apiKey != tc.wantAPIKey {
				t.Fatalf("x-api-key = %q (present %v), want %q: %#v", apiKey, hasAPIKey, tc.wantAPIKey, headers)
			}
			if (tc.wantAuthorize != "") != hasAuthorization || authorization != tc.wantAuthorize {
				t.Fatalf("Authorization = %q (present %v), want %q: %#v", authorization, hasAuthorization, tc.wantAuthorize, headers)
			}
		})
	}
}

func TestModelsListUsesBearerForAnthropic(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-test"}]}`))
	}))
	defer server.Close()

	args, err := json.Marshal(map[string]any{"config": map[string]any{
		"baseUrl":       server.URL + "/v1",
		"apiKey":        "k",
		"apiFormat":     "anthropic",
		"anthropicAuth": "bearer",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aiListModelsHandler("", string(args)); err != nil {
		t.Fatalf("list models: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := seen.Get("Authorization"); got != "Bearer k" {
		t.Fatalf("Authorization = %q, want Bearer k", got)
	}
	if got := seen.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key must be absent for bearer auth, got %q", got)
	}
	if got := seen.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", got)
	}
}
