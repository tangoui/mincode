package mcpauth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func callback(t *testing.T, redirectURI string, query url.Values) (int, string) {
	t.Helper()
	resp, err := http.Get(redirectURI + "?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestLoopbackRejectsWrongState(t *testing.T) {
	lb, redirectURI, err := StartLoopback("good-state")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if !regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/callback$`).MatchString(redirectURI) {
		t.Fatalf("redirect uri = %q", redirectURI)
	}

	status, body := callback(t, redirectURI, url.Values{"state": {"bad-state"}, "code": {"stolen-code"}})
	if status != http.StatusBadRequest || strings.Contains(body, "stolen-code") {
		t.Fatalf("wrong state: status=%d body=%q", status, body)
	}
	// still waiting after the bad request
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if _, err := lb.Wait(short); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("wrong state must not complete the wait, got %v", err)
	}
	cancel()

	status, body = callback(t, redirectURI, url.Values{"state": {"good-state"}, "code": {"real-code"}})
	if status != http.StatusOK || !strings.Contains(body, "可以关闭此页面") || strings.Contains(body, "real-code") || strings.Contains(body, "good-state") {
		t.Fatalf("callback page: status=%d body=%q", status, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := lb.Wait(ctx)
	if err != nil || code != "real-code" {
		t.Fatalf("Wait = %q, %v", code, err)
	}
}

func TestLoopbackTimeoutAndCancel(t *testing.T) {
	lb, _, err := StartLoopback("s")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := lb.Wait(ctx); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("timeout error = %v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := lb.Wait(cancelled); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_CANCELLED]") {
		t.Fatalf("cancel error = %v", err)
	}
	lb.Close()
	lb.Close() // idempotent
	if _, err := lb.Wait(context.Background()); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_CANCELLED]") {
		t.Fatalf("closed loopback must not block, got %v", err)
	}
}

func TestLoopbackReportsDeniedConsent(t *testing.T) {
	lb, redirectURI, err := StartLoopback("st")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	status, body := callback(t, redirectURI, url.Values{"state": {"st"}, "error": {"access_denied"}, "error_description": {"<script>x</script>"}})
	if status != http.StatusOK || strings.Contains(body, "script") {
		t.Fatalf("denied page: status=%d body=%q", status, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := lb.Wait(ctx); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_DENIED]") || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied error = %v", err)
	}
}

func TestLoopbackRejectsOtherPathsAndHosts(t *testing.T) {
	lb, redirectURI, err := StartLoopback("st")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	other := strings.TrimSuffix(redirectURI, "/callback") + "/other?state=st&code=c"
	resp, err := http.Get(other)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other path status = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, redirectURI+"?state=st&code=c", nil)
	req.Host = "rebound.example"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign Host status = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := lb.Wait(ctx); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("rejected requests must not complete the wait, got %v", err)
	}
	if _, _, err := StartLoopback(""); err == nil {
		t.Fatal("empty state must be refused")
	}
}
