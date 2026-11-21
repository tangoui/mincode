package cloudcompanion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The server explains a refused redemption with a fixed reason enum; the desktop maps known reasons to its
// own text and never shows the untrusted remote message.
func TestRedeemMemberCodeExplainsServerReason(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	reply := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/common/refreshToken" {
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"fresh-token"}}`))
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer server.Close()
	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "token", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"stopped":             "已被停止",
		"exhausted":           "兑换次数已用完",
		"already_redeemed":    "当前账号已兑换过",
		"expired":             "已过最晚兑换时间",
		"not_found":           "授权码不存在",
		"product_unavailable": "授权商品已下架或停用",
		"plan_unavailable":    "配额策略未启用",
		"invalid_format":      "授权码格式无效",
	}
	for reason, want := range cases {
		reply = `{"code":0,"msg":"<script>remote</script>","data":{"reason":"` + reason + `"}}`
		_, err := redeemMemberCode(context.Background(), "ABCD-1234-EFGH-5678", 42)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "remote") {
			t.Errorf("%s: %v", reason, err)
		}
	}
	reply = `{"code":0,"msg":"x","data":{"reason":"something_new"}}`
	if _, err := redeemMemberCode(context.Background(), "ABCD-1234-EFGH-5678", 42); err == nil || !strings.Contains(err.Error(), "code 0") {
		t.Fatalf("unknown reasons keep the generic message: %v", err)
	}
}
