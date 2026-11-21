package cloudcompanion

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadMarketArtifact(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	payload := []byte("PK\x03\x04 plugin archive")
	refuse, expired, paidHost, quota := false, false, false, false
	linkURL := "/files/plugin.zip?ticket=abc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"fresh-token"}}`))
		case "/api/mincode/marketplaceDownload/link":
			if r.Method != http.MethodGet || r.Header.Get("ba-user-token") != "fresh-token" || r.URL.Query().Get("artifact_id") != "7" {
				t.Error("invalid link request")
			}
			if quota {
				_, _ = w.Write([]byte(`{"code":429,"msg":"Daily download limit reached; try again tomorrow","data":{"reason":"download_quota","retry_after":3600}}`))
				return
			}
			if paidHost {
				_, _ = w.Write([]byte(`{"code":403,"msg":"Requires a paid MinCode plan","data":{"reason":"requires_paid_host"}}`))
				return
			}
			if refuse {
				_, _ = w.Write([]byte(`{"code":0,"msg":"Artifact unavailable","data":null}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":1,"data":{"url":%q,"expires_at":1,"size_bytes":%d,"sha256":"x","filename":"p.zip"}}`, linkURL, len(payload))
		case "/files/plugin.zip":
			if r.Header.Get("ba-user-token") != "" || r.URL.Query().Get("ticket") != "abc" {
				t.Error("the download link must be fetched as is, without the member token")
			}
			if expired {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write(payload)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "登录") {
		t.Fatalf("signed-out download: %v", err)
	}
	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadMarketArtifact(context.Background(), "https://market.example.com", 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("token would reach a server other than the market origin: %v", err)
	}
	written, err := DownloadMarketArtifact(context.Background(), server.URL+"/", 7, int64(len(payload)), &out)
	if err != nil || written != int64(len(payload)) || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("download: %d %v", written, err)
	}
	out.Reset()
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, int64(len(payload))-1, &out); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	expired = true
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("expired link: %v", err)
	}
	expired = false
	linkURL = "http://files.example.com/plugin.zip"
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("plain HTTP link to another host accepted: %v", err)
	}
	linkURL = "/files/plugin.zip?ticket=abc"
	refuse = true
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("refusal accepted: %v", err)
	}
	refuse, paidHost = false, true
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "付费套餐") {
		t.Fatalf("paid host refusal: %v", err)
	}
	paidHost, quota = false, true
	if _, err := DownloadMarketArtifact(context.Background(), server.URL, 7, 1024, &out); err == nil || !strings.Contains(err.Error(), "下载次数已达上限") {
		t.Fatalf("download quota refusal: %v", err)
	}
}

func TestResolveMarketArtifactURL(t *testing.T) {
	for raw, want := range map[string]string{
		"/api/mincode/marketplaceDownload/file?ticket=ab":                 "https://cloud.example.com/api/mincode/marketplaceDownload/file?ticket=ab",
		"https://bucket.oss-cn-hangzhou.aliyuncs.com/p/a.zip?Signature=x": "https://bucket.oss-cn-hangzhou.aliyuncs.com/p/a.zip?Signature=x",
	} {
		if got, err := resolveMarketArtifactURL("https://cloud.example.com", raw); err != nil || got != want {
			t.Fatalf("%s: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://bucket.example.com/a.zip", "https://user:pass@bucket.example.com/a.zip", "https://bucket.example.com/a.zip#x", "file:///etc/passwd"} {
		if _, err := resolveMarketArtifactURL("https://cloud.example.com", raw); err == nil {
			t.Fatalf("unsafe link accepted: %s", raw)
		}
	}
}
