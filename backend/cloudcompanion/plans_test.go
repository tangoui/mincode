package cloudcompanion

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
)

const validFrameworkCatalog = `{"code":1,"data":{"items":[{"plan":"free","display_name":"Free","duration_days":null,"price_minor":0,"currency":"USD","version_range":"*","policy_revision":1,"effective_at":1700000000,"limits":{"max_devices":0,"max_active_workspaces":2,"max_windows_per_workspace":3}},{"plan":"annual","display_name":"Annual","duration_days":365,"price_minor":12900,"currency":"USD","version_range":"*","policy_revision":2,"effective_at":1700000000,"limits":{"max_devices":1,"max_active_workspaces":2,"max_windows_per_workspace":3}}]}}`

func TestFrameworkPlansPublicReadDoesNotAuthenticateOrChangeSession(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/mincode/frameworkPlans/index" || r.URL.RawQuery != "" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "" ||
			r.Header.Get("ba-user-token") != "" || r.Header.Get("Cookie") != "" ||
			r.Header.Get("X-MinCode-Signature") != "" {
			t.Errorf("catalog request contained credentials or lacked JSON accept header: %v", r.Header)
		}
		_, _ = w.Write([]byte(validFrameworkCatalog))
	}))
	defer server.Close()
	t.Setenv("MINCODE_CLOUD_SERVER_URL", server.URL)
	for _, signedIn := range []bool{false, true} {
		if signedIn {
			session := persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "access", RefreshToken: "refresh", AuthProvider: "member"}
			if err := writeSession(session); err != nil {
				t.Fatal(err)
			}
		}
		result, err := cloudFrameworkPlansHandler("", "")
		if err != nil {
			t.Fatalf("signedIn=%v: %v", signedIn, err)
		}
		var plans []FrameworkPlan
		if err := json.Unmarshal([]byte(result), &plans); err != nil {
			t.Fatal(err)
		}
		if len(plans) != 2 || plans[0].Limits.MaxDevices != 0 || plans[0].DurationDays != nil ||
			plans[1].PriceMinor != 12900 || plans[1].DurationDays == nil || *plans[1].DurationDays != 365 {
			t.Fatalf("unexpected public catalog: %+v", plans)
		}
		if signedIn {
			session, err := readSession()
			if err != nil || session.AccessToken != "access" || session.RefreshToken != "refresh" {
				t.Fatalf("catalog changed session: %+v, %v", session, err)
			}
		} else if _, err := readSession(); !os.IsNotExist(err) {
			t.Fatalf("catalog created a session: %v", err)
		}
	}
	if requests != 2 {
		t.Fatalf("expected two public reads, got %d", requests)
	}
}

func TestFetchFrameworkPlansRejectsIncompleteAndMalformedResponses(t *testing.T) {

	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing quota", func(p map[string]any) { delete(p["limits"].(map[string]any), "max_devices") }},
		{"null quota", func(p map[string]any) { p["limits"].(map[string]any)["max_devices"] = nil }},
		{"missing workspaces quota", func(p map[string]any) { delete(p["limits"].(map[string]any), "max_active_workspaces") }},
		{"missing windows quota", func(p map[string]any) { delete(p["limits"].(map[string]any), "max_windows_per_workspace") }},
		{"fractional quota", func(p map[string]any) { p["limits"].(map[string]any)["max_devices"] = 0.5 }},
		{"negative quota", func(p map[string]any) { p["limits"].(map[string]any)["max_devices"] = -1 }},
		{"oversized quota", func(p map[string]any) { p["limits"].(map[string]any)["max_devices"] = 4294967296 }},
		{"missing limits", func(p map[string]any) { delete(p, "limits") }},
		{"missing price", func(p map[string]any) { delete(p, "price_minor") }},
		{"null price", func(p map[string]any) { p["price_minor"] = nil }},
		{"negative price", func(p map[string]any) { p["price_minor"] = -1 }},
		{"oversized price", func(p map[string]any) { p["price_minor"] = 4294967296 }},
		{"fractional price", func(p map[string]any) { p["price_minor"] = 1.5 }},
		{"paid price zero", func(p map[string]any) { p["price_minor"] = 0 }},
		{"original price below price", func(p map[string]any) { p["original_price_minor"] = 100 }},
		{"fractional original price", func(p map[string]any) { p["original_price_minor"] = 19900.5 }},
		{"oversized original price", func(p map[string]any) { p["original_price_minor"] = 4294967296 }},
		{"bad currency", func(p map[string]any) { p["currency"] = "usd" }},
		{"missing currency", func(p map[string]any) { delete(p, "currency") }},
		{"missing duration", func(p map[string]any) { delete(p, "duration_days") }},
		{"null annual duration", func(p map[string]any) { p["duration_days"] = nil }},
		{"zero annual duration", func(p map[string]any) { p["duration_days"] = 0 }},
		{"fractional duration", func(p map[string]any) { p["duration_days"] = 1.5 }},
		{"long duration", func(p map[string]any) { p["duration_days"] = 36501 }},
		{"missing revision", func(p map[string]any) { delete(p, "policy_revision") }},
		{"missing effective date", func(p map[string]any) { delete(p, "effective_at") }},
		{"missing version range", func(p map[string]any) { delete(p, "version_range") }},
		{"empty version range", func(p map[string]any) { p["version_range"] = "" }},
		{"empty name", func(p map[string]any) { p["display_name"] = "  " }},
		{"unknown plan", func(p map[string]any) { p["plan"] = "custom" }},
		{"duplicate plan", func(p map[string]any) { p["plan"] = "free" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(validFrameworkCatalog), &payload); err != nil {
				t.Fatal(err)
			}
			items := payload["data"].(map[string]any)["items"].([]any)

			tc.edit(items[1].(map[string]any))
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			if plans, err := fetchFrameworkPlans(context.Background(), server.URL); err == nil || plans != nil {
				t.Fatalf("accepted invalid catalog: %+v, %v", plans, err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"code":1,"data":{}}`, `{"code":1,"data":{"items":null}}`, `{"code":0,"data":{"items":[]}}`, `{"code":1,"data":{"items":[null]}}`, `not json`,
		strings.Replace(validFrameworkCatalog, `"duration_days":null`, `"duration_days":1`, 1),
		strings.Replace(validFrameworkCatalog, `"price_minor":0`, `"price_minor":1`, 1),
		strings.Replace(validFrameworkCatalog, `"version_range":"*"`, `"version_range":""`, 1),
		strings.Replace(validFrameworkCatalog, `"duration_days":null`, `"duration_days":365`, 1),
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if _, err := fetchFrameworkPlans(context.Background(), server.URL); err == nil {
				t.Fatal("accepted invalid envelope")
			}
		})
	}
}

func TestFetchFrameworkPlansOriginalPrice(t *testing.T) {
	fetch := func(edit func(items []any)) ([]FrameworkPlan, error) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal([]byte(validFrameworkCatalog), &payload); err != nil {
			t.Fatal(err)
		}
		edit(payload["data"].(map[string]any)["items"].([]any))
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		defer server.Close()
		return fetchFrameworkPlans(context.Background(), server.URL)
	}
	plans, err := fetch(func(items []any) { items[1].(map[string]any)["original_price_minor"] = 19900 })
	if err != nil || plans[1].OriginalPriceMinor == nil || *plans[1].OriginalPriceMinor != 19900 || plans[0].OriginalPriceMinor != nil {
		t.Fatalf("discounted plan = %+v, %v", plans, err)
	}
	plans, err = fetch(func(items []any) {
		items[0].(map[string]any)["original_price_minor"] = nil
		items[1].(map[string]any)["original_price_minor"] = 12900
	})
	if err != nil || plans[0].OriginalPriceMinor != nil || plans[1].OriginalPriceMinor != nil {
		t.Fatalf("original equal to price must mean no discount: %+v, %v", plans, err)
	}
	if plans, err := fetch(func(items []any) { items[0].(map[string]any)["original_price_minor"] = 500 }); err == nil || plans != nil {
		t.Fatalf("accepted a free plan with an original price: %+v", plans)
	}
}

func TestFetchFrameworkPlansAllowsEmptyCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":1,"data":{"items":[]}}`))
	}))
	defer server.Close()
	plans, err := fetchFrameworkPlans(context.Background(), server.URL)
	if err != nil || plans == nil || len(plans) != 0 {
		t.Fatalf("empty public catalog: %+v, %v", plans, err)
	}
}

func TestFrameworkPlansNetworkErrorDoesNotExposeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"timeout", context.DeadlineExceeded, "获取公开套餐超时，请检查云服务连接"},
		{"dns", &net.DNSError{Err: "secret", Name: "private.example"}, "云服务域名无法解析，请检查网络或云服务地址"},
		{"tls", x509.UnknownAuthorityError{}, "云服务 TLS 证书验证失败，请检查云服务证书"},
		{"reset", syscall.ECONNRESET, "云服务连接被重置，请检查服务器 HTTPS 配置或网络"},
		{"refused", syscall.ECONNREFUSED, "云服务拒绝连接，请检查服务是否已启动"},
		{"other", errors.New("private request URL and credentials"), "无法连接云服务，请检查云服务地址、网络和 HTTPS 配置"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameworkPlansNetworkError(tc.err).Error(); got != tc.want {
				t.Fatalf("network error = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchFrameworkPlansRejectsOversizeAndRedirect(t *testing.T) {
	redirects := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirects++
		_, _ = w.Write([]byte(validFrameworkCatalog))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oversize/api/mincode/frameworkPlans/index" {
			_, _ = w.Write([]byte(strings.Repeat("x", 64*1024+1)))
			return
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	if _, err := fetchFrameworkPlans(context.Background(), server.URL+"/oversize"); err == nil {
		t.Fatal("oversized catalog accepted")
	}
	if _, err := fetchFrameworkPlans(context.Background(), server.URL); err == nil || redirects != 0 {
		t.Fatalf("followed catalog redirect: %v (target requests: %d)", err, redirects)
	}
}
