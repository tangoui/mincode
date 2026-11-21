package cloudcompanion

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The cloud explains a refused registration with a fixed reason enum; the desktop maps known reasons to its
// own text and never shows the untrusted remote message.
func TestMemberRegistrationExplainsServerReason(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	reply := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkIn" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer server.Close()
	register := func() error {
		_, err := cloudRegisterHandler("", `{"email":"member@example.com","username":"newmember","password":"password","passwordConfirmation":"password","code":"123456","serverUrl":"`+server.URL+`"}`)
		return err
	}
	cases := map[string]string{
		"username_taken":   "该用户名已被注册",
		"username_invalid": "用户名需以英文字母开头",
		"password_invalid": "密码需为 6–32 位",
		"email_invalid":    "邮箱格式不正确",
		"email_taken":      "该邮箱已注册",
		"captcha_invalid":  "邮箱验证码错误或已过期",
	}
	for reason, want := range cases {
		reply = `{"code":0,"msg":"<script>remote</script>","data":{"reason":"` + reason + `"}}`
		if err := register(); err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "remote") {
			t.Errorf("%s: %v", reason, err)
		}
	}
	reply = `{"code":0,"msg":"x","data":{"reason":"something_new"}}`
	if err := register(); err == nil || !strings.Contains(err.Error(), "code 0") {
		t.Fatalf("unknown reasons keep the generic message: %v", err)
	}
	reply = `{"code":0,"msg":"x","data":null}`
	if err := register(); err == nil || !strings.Contains(err.Error(), "code 0") {
		t.Fatalf("older clouds without a reason keep the generic message: %v", err)
	}
}
