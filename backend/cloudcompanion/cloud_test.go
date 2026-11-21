package cloudcompanion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
)

// registeredDeviceResponse answers POST /devices/register like the cloud does for a fresh device.
const registeredDeviceResponse = `{"code":1,"msg":"","data":{"device":{"device_id":"%s","device_type":"desktop"}}}`

func TestMain(m *testing.M) {
	// Logins in tests must not spawn a background realtime loop against the per-test server.
	startRealtimeAfterRegistration = func() {}
	os.Exit(m.Run())
}

func writeRegisteredDevice(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(strings.Replace(registeredDeviceResponse, "%s", r.Header.Get("X-MinCode-Device-Id"), 1)))
}

func TestCloudMemberContract(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/user/checkIn":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":false}}`))
				return
			}
			if r.Method != http.MethodPost || r.PostFormValue("tab") != "login" || r.PostFormValue("username") != "member@example.com" || r.PostFormValue("password") != "password" || r.PostFormValue("keep") != "1" {
				t.Errorf("unexpected login form: %s %v", r.Method, r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":42,"email":"member@example.com","token":"member-access","refresh_token":"member-refresh"}}}`))
		case "/api/common/refreshToken":
			if r.PostFormValue("refreshToken") != "member-refresh" {
				t.Error("wrong refresh token")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"type":"user-refresh","token":"new-member-access"}}`))
		case "/api/mincode/v1/devices/register":
			if r.Header.Get("Authorization") != "Bearer member-access" {
				t.Error("registration must use the member access token")
			}
			writeRegisteredDevice(w, r)
		case "/api/mincode/v1/devices":
			if r.Header.Get("Authorization") != "Bearer member-access" || r.Header.Get("X-MinCode-Signature") == "" {
				t.Error("device list must be signed with the member access token")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"items":[],"stats":{"total":0,"desktop":0,"mobile":0}}}`))
		case "/api/user/logout":
			// The fresh access token from login is reused: no refresh round-trip before logout.
			if r.Header.Get("ba-user-token") != "member-access" || r.PostFormValue("refreshToken") != "member-refresh" {
				t.Error("wrong logout credentials")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":[]}`))
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	login, err := cloudLoginHandler("", `{"email":"member@example.com","password":"password","serverUrl":"`+server.URL+`"}`)
	if err != nil || !strings.Contains(login, `"signedIn":true`) {
		t.Fatalf("login: %s %v", login, err)
	}
	session, err := readSession()
	if err != nil || session.AuthProvider != "member" || session.AccessToken != "member-access" {
		t.Fatalf("stored member session: %+v %v", session, err)
	}
	if list, err := cloudDevicesListHandler("", "{}"); err != nil || !strings.Contains(list, `"stats"`) {
		t.Fatalf("member device list: %s %v", list, err)
	}
	if _, err := cloudLogoutHandler("", "{}"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := readSession(); !os.IsNotExist(err) {
		t.Fatalf("session not removed: %v", err)
	}
	if len(paths) != 5 {
		t.Fatalf("unexpected requests: %v", paths)
	}
}

func TestCloudLogoutRemovesLocalSessionWhenRemoteIsUnavailable(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	if err := writeSession(persistedSession{
		APIBaseURL: "https://api.example.com", AccountID: 42, AccessToken: "access", RefreshToken: "refresh",
		AuthProvider: "buildadmin", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := cloudLogoutHandler("", "{}")
	if err == nil || !strings.Contains(err.Error(), "本地已退出") {
		t.Fatalf("expected remote logout warning: %v", err)
	}
	if _, readErr := readSession(); !os.IsNotExist(readErr) {
		t.Fatalf("local session must be removed after remote failure: %v", readErr)
	}
}

func TestCloudCaptchaBlocksPasswordSubmission(t *testing.T) {
	passwordPosted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			passwordPosted = true
		}
		_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":true}}`))
	}))
	defer server.Close()
	_, err := cloudLoginHandler("", `{"email":"member@example.com","password":"password","serverUrl":"`+server.URL+`"}`)
	if err == nil || !strings.Contains(err.Error(), "请先完成验证码") || passwordPosted {
		t.Fatalf("captcha must block password POST: error=%v posted=%v", err, passwordPosted)
	}
}

func TestMemberLoginUsesVerifiedClickCaptcha(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/checkIn":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":true}}`))
				return
			}
			if r.PostFormValue("captchaId") == "" || r.PostFormValue("captchaInfo") != "10,20;100;50" {
				t.Errorf("captcha was not forwarded: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":7,"email":"member@example.com","token":"access","refresh_token":"refresh"}}}`))
		case "/api/mincode/v1/devices/register":
			writeRegisteredDevice(w, r)
		case "/api/common/clickCaptcha":
			_, _ = w.Write([]byte(`{"code":1,"data":{"id":"` + r.URL.Query().Get("id") + `","text":["A"],"base64":"data:image/png;base64,aW1hZ2U=","width":100,"height":50}}`))
		case "/api/common/checkClickCaptcha":
			if r.PostFormValue("id") == "" || r.PostFormValue("info") != "10,20;100;50" || r.PostFormValue("unset") != "0" {
				t.Errorf("unexpected captcha check: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":[]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("MINCODE_CLOUD_SERVER_URL", server.URL)
	captchaJSON, err := cloudLoginCaptchaHandler("", `{"serverUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var challenge struct {
		Required bool `json:"required"`
		memberClickCaptcha
	}
	if err := json.Unmarshal([]byte(captchaJSON), &challenge); err != nil || !challenge.Required || challenge.ID == "" {
		t.Fatalf("captcha response: %s %v", captchaJSON, err)
	}
	_, err = cloudLoginCaptchaCheckHandler("", `{"id":"`+challenge.ID+`","info":"10,20;100;50"}`)
	if err != nil {
		t.Fatal(err)
	}
	login, err := cloudLoginHandler("", `{"email":"member@example.com","password":"password","serverUrl":"`+server.URL+`","captchaId":"`+challenge.ID+`","captchaInfo":"10,20;100;50"}`)
	if err != nil || !strings.Contains(login, `"signedIn":true`) {
		t.Fatalf("login: %s %v", login, err)
	}
}

func TestMemberRegistrationUsesEmailCodeAndNativeCheckIn(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	var sent, registered bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/checkIn":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":true}}`))
				return
			}
			registered = true
			if !sent || r.PostFormValue("tab") != "register" || r.PostFormValue("registerType") != "email" || r.PostFormValue("email") != "member@example.com" || r.PostFormValue("username") != "newmember" || r.PostFormValue("password") != "password" || r.PostFormValue("captcha") != "123456" || r.PostFormValue("keep") != "1" {
				t.Errorf("unexpected registration form: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":17,"email":"member@example.com","token":"access","refresh_token":"refresh"}}}`))
		case "/api/mincode/v1/devices/register":
			writeRegisteredDevice(w, r)
		case "/api/common/clickCaptcha":
			_, _ = w.Write([]byte(`{"code":1,"data":{"id":"` + r.URL.Query().Get("id") + `","text":["A"],"base64":"data:image/png;base64,aW1hZ2U=","width":100,"height":50}}`))
		case "/api/common/checkClickCaptcha":
			if r.PostFormValue("unset") != "0" {
				t.Errorf("verification must preserve challenge: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":[]}`))
		case "/api/ems/send":
			sent = true
			if r.PostFormValue("email") != "member@example.com" || r.PostFormValue("event") != "user_register" || r.PostFormValue("captchaId") == "" || r.PostFormValue("captchaInfo") != "10,20;100;50" {
				t.Errorf("unexpected email code request: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"code":1,"data":[]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	captchaJSON, err := cloudLoginCaptchaHandler("", `{"purpose":"register","serverUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var captcha memberClickCaptcha
	if err := json.Unmarshal([]byte(captchaJSON), &captcha); err != nil || captcha.ID == "" {
		t.Fatalf("captcha response: %s %v", captchaJSON, err)
	}
	if _, err := cloudLoginCaptchaCheckHandler("", `{"purpose":"register","id":"`+captcha.ID+`","info":"10,20;100;50"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudRegisterCodeHandler("", `{"email":"member@example.com","serverUrl":"`+server.URL+`","captchaId":"`+captcha.ID+`","captchaInfo":"10,20;100;50"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudRegisterCodeHandler("", `{"email":"member@example.com","serverUrl":"`+server.URL+`","captchaId":"`+captcha.ID+`","captchaInfo":"10,20;100;50"}`); err == nil {
		t.Fatal("captcha must be single-use")
	}
	result, err := cloudRegisterHandler("", `{"email":"member@example.com","username":"newmember","password":"password","passwordConfirmation":"password","code":"123456","serverUrl":"`+server.URL+`"}`)
	if err != nil || !registered || !strings.Contains(result, `"signedIn":true`) {
		t.Fatalf("register: %s %v registered=%v", result, err, registered)
	}
	session, err := readSession()
	if err != nil || session.AuthProvider != "member" || !statusFromSession(session, deviceIdentity{}).DeviceAvailable {
		t.Fatalf("member session: %+v %v", session, err)
	}
}

func TestMemberRegistrationWithoutRefreshTokenFallsBackToLogin(t *testing.T) {
	for _, captchaRequired := range []bool{false, true} {
		t.Run(map[bool]string{false: "login fallback", true: "login captcha required"}[captchaRequired], func(t *testing.T) {
			root := t.TempDir()
			previousRoot := identityRootDir
			identityRootDir = func() string { return root }
			t.Cleanup(func() { identityRootDir = previousRoot })
			var logins int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/mincode/v1/devices/register" {
					writeRegisteredDevice(w, r)
					return
				}
				if r.URL.Path != "/api/user/checkIn" {
					t.Errorf("unexpected request: %s", r.URL.Path)
					return
				}
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":` + map[bool]string{false: "false", true: "true"}[captchaRequired] + `}}`))
					return
				}
				switch r.PostFormValue("tab") {
				case "register":
					// An older cloud: account created, but no refresh token issued.
					_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":17,"email":"member@example.com","token":"access","refresh_token":""}}}`))
				case "login":
					logins++
					if r.PostFormValue("username") != "newmember" || r.PostFormValue("password") != "password" || r.PostFormValue("keep") != "1" {
						t.Errorf("unexpected fallback login form: %v", r.PostForm)
					}
					_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":17,"email":"member@example.com","token":"login-access","refresh_token":"login-refresh"}}}`))
				}
			}))
			defer server.Close()
			result, err := cloudRegisterHandler("", `{"email":"member@example.com","username":"newmember","password":"password","passwordConfirmation":"password","code":"123456","serverUrl":"`+server.URL+`"}`)
			if err != nil {
				t.Fatalf("registration must not fail after the account exists: %v", err)
			}
			var status CloudStatus
			if err := json.Unmarshal([]byte(result), &status); err != nil {
				t.Fatal(err)
			}
			session, readErr := readSession()
			if captchaRequired {
				if logins != 0 || status.SignedIn || !status.RegisteredNeedsLogin || status.Email != "member@example.com" || !os.IsNotExist(readErr) {
					t.Fatalf("status=%+v logins=%d session err=%v", status, logins, readErr)
				}
				return
			}
			if logins != 1 || !status.SignedIn || status.RegisteredNeedsLogin || readErr != nil || session.RefreshToken != "login-refresh" {
				t.Fatalf("status=%+v logins=%d session=%+v err=%v", status, logins, session, readErr)
			}
		})
	}
}

func TestLegacyMemberSessionRemainsReadable(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	if err := writeSession(persistedSession{APIBaseURL: "https://example.com", AccountID: 5, AccessToken: "access", RefreshToken: "refresh", AuthProvider: "buildadmin"}); err != nil {
		t.Fatal(err)
	}
	session, err := readSession()
	if err != nil || !isMemberSession(session) || !statusFromSession(session, deviceIdentity{}).DeviceAvailable {
		t.Fatalf("legacy session: %+v %v", session, err)
	}
}

func TestCloudRejectsNonSuccessCodesAndRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/api/user/checkIn", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"code":303,"msg":"expired","data":{}}`))
	}))
	defer server.Close()
	if err := cloudPost(context.Background(), server.URL, "user/checkIn", url.Values{}, "", nil); err == nil {
		t.Fatal("code 303 accepted")
	}
	if err := cloudPost(context.Background(), server.URL, "redirect", url.Values{}, "", nil); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestGenerateIdentityProducesSeparateSigningAndEncryptionKeys(t *testing.T) {
	identity, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIdentity(identity); err != nil {
		t.Fatalf("generated identity invalid: %v", err)
	}
	if identity.SigningPublicKey == identity.EncryptionPublicKey {
		t.Fatal("signing and encryption public keys must be distinct")
	}
	if !strings.HasPrefix(identity.DeviceID, "desktop-") {
		t.Fatalf("unexpected device id: %s", identity.DeviceID)
	}
}

func TestCanonicalCloudRequestSharedVector(t *testing.T) {
	got := canonicalCloudRequest(
		http.MethodPost,
		"/api/mincode/v1/devices/register",
		"1800000000",
		"nonce_1234567890123456",
		[]byte(`{"hello":"world"}`),
		"access-token",
	)
	want := "POST\n/api/mincode/v1/devices/register\n1800000000\nnonce_1234567890123456\n93a23971a914e5eacbf0a8d25154cda309c3c1c72fbb9914d47c60f3cb681588\n3f16bed7089f4653e5ef21bfd2824d7f3aaaecc7a598e7e89c580e1606a9cc52"
	if got != want {
		t.Fatalf("canonical string mismatch:\n got %q\nwant %q", got, want)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	if publicKey := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)); publicKey != "6kpsY+KcUgq+9VB7Ey7F+ZVHdq6+vnuSQh7qaRRG0iw=" {
		t.Fatalf("public key = %s", publicKey)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(got)))
	if signature != "TAOpmuGkIqqfsWi4Dzl26ehLwumxoTKoaVJGSn8fwDFDLqsDdQHYMlIQ7MamORmb1imwAV8TlHqVNsLcwITsAg==" {
		t.Fatalf("signature = %s", signature)
	}
}

func TestSignCloudRequestSetsDeviceIDHeader(t *testing.T) {
	identity, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/mincode/v1/devices"}, Header: make(http.Header)}
	if err := signCloudRequest(req, nil, "access-token", identity); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-MinCode-Device-Id"); got != identity.DeviceID {
		t.Fatalf("X-MinCode-Device-Id = %q, want %q", got, identity.DeviceID)
	}
}

// seedMemberRelayContext writes a member session for baseURL and returns the local device identity.
func seedMemberRelayContext(t *testing.T, baseURL string) deviceIdentity {
	t.Helper()
	seedMemberSession(t, baseURL, "cached", time.Now().Unix())
	identity, err := loadOrCreateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestAuthenticatedCloudContextAcceptsMemberSession(t *testing.T) {
	identity := seedMemberRelayContext(t, "https://api.example.com")
	session, got, err := authenticatedCloudContext()
	if err != nil {
		t.Fatalf("member session rejected: %v", err)
	}
	if !isMemberSession(session) || got.DeviceID != identity.DeviceID {
		t.Fatalf("context = %+v %+v", session, got)
	}
	if !statusFromSession(session, identity).DeviceAvailable {
		t.Fatal("member session must report the device as available")
	}
}

func TestRegisterRelayDeviceDeviceLimit(t *testing.T) {
	var gotHeader string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mincode/v1/devices/register" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotHeader = r.Header.Get("X-MinCode-Device-Id")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"code":0,"msg":"设备数量已达套餐上限","data":{"reason":"device_limit","current":2,"limit":2}}`))
	}))
	defer server.Close()
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	err = registerRelayDevice(context.Background(), session, identity)
	if !errors.Is(err, errRelayDeviceLimit) {
		t.Fatalf("err = %v, want errRelayDeviceLimit", err)
	}
	if !strings.Contains(err.Error(), "2/2") {
		t.Fatalf("device limit message must reuse the quota hint: %v", err)
	}
	if gotHeader != identity.DeviceID || gotBody["device_id"] != identity.DeviceID || gotBody["device_type"] != "desktop" || gotBody["signing_public_key"] != identity.SigningPublicKey {
		t.Fatalf("registration request header=%q body=%v", gotHeader, gotBody)
	}
}

func TestRegisterRelayDeviceKeyMismatchIsNotTreatedAsLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"设备公钥与已登记的不一致","data":{"reason":"key_mismatch"}}`))
	}))
	defer server.Close()
	identity := seedMemberRelayContext(t, server.URL)
	session, _ := readSession()
	err := registerRelayDevice(context.Background(), session, identity)
	if !errors.Is(err, errRelayKeyMismatch) || errors.Is(err, errMemberTokenRejected) {
		t.Fatalf("err = %v, want errRelayKeyMismatch", err)
	}
	if _, err := readSession(); err != nil {
		t.Fatalf("key mismatch must keep the session: %v", err)
	}
}

func TestLoginSucceedsWhenDeviceRegistrationFails(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/checkIn":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"code":1,"data":{"userLoginCaptchaSwitch":false}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":42,"email":"member@example.com","token":"member-access","refresh_token":"member-refresh"}}}`))
		case "/api/mincode/v1/devices/register":
			_, _ = w.Write([]byte(`{"code":0,"msg":"设备数量已达套餐上限","data":{"reason":"device_limit","current":1,"limit":1}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	raw, err := cloudLoginHandler("", `{"email":"member@example.com","password":"password","serverUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("registration failure must not block login: %v", err)
	}
	var status CloudStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	if !status.SignedIn || status.DeviceAvailable || !strings.Contains(status.DeviceRegistrationError, "1/1") {
		t.Fatalf("status = %+v", status)
	}
	if _, err := readSession(); err != nil {
		t.Fatalf("session must be kept: %v", err)
	}
}

func TestSignCloudRequestVerifiesWithPublicKey(t *testing.T) {
	identity, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"hello":"world"}`)
	req := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/api/mincode/v1/devices/register"}, Header: make(http.Header)}
	previousNow := cloudNow
	cloudNow = func() time.Time { return time.Unix(1_800_000_000, 0) }
	t.Cleanup(func() { cloudNow = previousNow })
	if err := signCloudRequest(req, body, "access-token", identity); err != nil {
		t.Fatal(err)
	}
	publicKey, _ := base64.StdEncoding.DecodeString(identity.SigningPublicKey)
	signature, _ := base64.StdEncoding.DecodeString(req.Header.Get("X-MinCode-Signature"))
	canonical := canonicalCloudRequest(
		req.Method,
		req.URL.EscapedPath(),
		req.Header.Get("X-MinCode-Timestamp"),
		req.Header.Get("X-MinCode-Nonce"),
		body,
		"access-token",
	)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(canonical), signature) {
		t.Fatal("signature does not verify")
	}
}

func TestCloudAPIBaseURLRequiresHTTPSExceptLoopback(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "")
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "")
	t.Setenv("MINCODE_MARKET_SERVER_URL", "")
	// Development builds default to the local cloud, production builds to the public API origin.
	if got, err := cloudAPIBaseURL(""); err != nil || got != strings.TrimRight(core.ResolveMinCodeCloudServer(""), "/") {
		t.Fatalf("cloud default = %q, %v", got, err)
	}
	if _, err := cloudAPIBaseURL("http://example.com"); err == nil {
		t.Fatal("remote HTTP must be rejected")
	}
	if got, err := cloudAPIBaseURL("http://127.0.0.1:8787/"); err != nil || got != "http://127.0.0.1:8787" {
		t.Fatalf("loopback HTTP should be allowed, got=%q err=%v", got, err)
	}
	if got, err := cloudAPIBaseURL("https://api.example.com/"); err != nil || got != "https://api.example.com" {
		t.Fatalf("HTTPS should be allowed, got=%q err=%v", got, err)
	}
}

func TestCloudLoginPersistsOnlyEncryptedSession(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })

	identity, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(filepath.Join(root, identityDataDirectory, identityFileName), identity); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, identityDataDirectory, identityFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), identity.SigningPrivateKey) || !strings.HasPrefix(string(raw), "enc:v2:") {
		t.Fatal("identity file must contain only encrypted data")
	}
}

func TestCloudDoRequestRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", cloudMaxResponseBytes+1)))
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if err := cloudDoRequest(req, nil); err == nil {
		t.Fatal("oversized response must fail")
	}
}

func TestCloudWebSocketURLDerivesSecureEndpoint(t *testing.T) {
	t.Setenv("MINCODE_CLOUD_WS_URL", "")
	got, err := cloudWebSocketURL("https://api.example.com")
	if err != nil || got != "wss://api.example.com/ws/mincode/v1" {
		t.Fatalf("unexpected websocket url %q err=%v", got, err)
	}
	if _, err := validateWebSocketURL("ws://example.com/ws/mincode/v1"); err == nil {
		t.Fatal("remote insecure websocket must be rejected")
	}
}

func TestRequestTimestampValid(t *testing.T) {
	previousNow := cloudNow
	cloudNow = func() time.Time { return time.Unix(1_800_000_000, 0) }
	t.Cleanup(func() { cloudNow = previousNow })
	if !requestTimestampValid(1_800_000_000) {
		t.Fatal("current timestamp should be valid")
	}
	if requestTimestampValid(1_799_999_699) {
		t.Fatal("timestamp outside clock skew must be invalid")
	}
}

func TestCloudRegisterRequiresNativeRegistrationFields(t *testing.T) {
	result, err := cloudRegisterHandler("", `{}`)
	if err == nil || !strings.Contains(err.Error(), "完整注册信息") || result != "" {
		t.Fatalf("registration must reject incomplete fields: result=%q error=%v", result, err)
	}
}

func TestValidatePersistedSession(t *testing.T) {
	valid := persistedSession{
		APIBaseURL: "https://api.example.com/", AccessToken: "access", RefreshToken: "refresh",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	got, err := validatePersistedSession(valid)
	if err != nil || got.APIBaseURL != "https://api.example.com" {
		t.Fatalf("legacy session with optional account fields should remain valid: %+v, %v", got, err)
	}
	cases := map[string]func(*persistedSession){
		"remote http":    func(s *persistedSession) { s.APIBaseURL = "http://example.com" },
		"credentials":    func(s *persistedSession) { s.APIBaseURL = "https://user:pass@example.com" },
		"query":          func(s *persistedSession) { s.APIBaseURL = "https://example.com?token=secret" },
		"relative URL":   func(s *persistedSession) { s.APIBaseURL = "/cloud" },
		"empty access":   func(s *persistedSession) { s.AccessToken = "  " },
		"empty refresh":  func(s *persistedSession) { s.RefreshToken = "" },
		"missing expiry": func(s *persistedSession) { s.ExpiresAt = time.Time{} },
		"negative ID":    func(s *persistedSession) { s.AccountID = -1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			session := valid
			change(&session)
			if _, err := validatePersistedSession(session); err == nil {
				t.Fatal("invalid persisted session accepted")
			}
		})
	}
}

func TestCloudStatusMarksExpiredSessionWithoutSigningOut(t *testing.T) {
	previousNow := cloudNow
	cloudNow = func() time.Time { return time.Unix(1_800_000_000, 0) }
	t.Cleanup(func() { cloudNow = previousNow })
	identity := deviceIdentity{DeviceID: "desktop-test"}
	session := persistedSession{APIBaseURL: "https://api.example.com", ExpiresAt: cloudNow().Add(-time.Second)}
	status := statusFromSession(session, identity)
	if !status.SignedIn || !status.Expired || status.ExpiresAt == nil {
		t.Fatalf("expired access token must retain refreshable session: %+v", status)
	}
	session.ExpiresAt = cloudNow().Add(time.Second)
	status = statusFromSession(session, identity)
	if status.Expired || !status.SignedIn {
		t.Fatalf("fresh session status: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"expired"`) {
		t.Fatalf("fresh session should preserve old status shape: %s", raw)
	}
}

func TestCloudLogoutRejectsDifferentServerWithoutSendingToken(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })

	identity, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(filepath.Join(root, identityDataDirectory, identityFileName), identity); err != nil {
		t.Fatal(err)
	}
	serverRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverRequests++
		if r.Header.Get("ba-user-token") != "access" || r.URL.Path != "/api/user/logout" {
			t.Errorf("unexpected logout request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":1,"data":[]}`))
	}))
	defer server.Close()
	attackerRequests := 0
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	session := persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "access", RefreshToken: "refresh", AuthProvider: "member", AccessTokenObtainedAt: time.Now().Unix()}
	if err := writeSession(session); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudLogoutHandler("", `{"serverUrl":"`+attacker.URL+`"}`); err == nil {
		t.Fatal("different logout server must be rejected")
	}
	if attackerRequests != 0 || serverRequests != 0 {
		t.Fatalf("rejected request sent to servers: owner=%d attacker=%d", serverRequests, attackerRequests)
	}
	if _, err := readSession(); err != nil {
		t.Fatalf("rejected logout must preserve session: %v", err)
	}
	if _, err := cloudLogoutHandler("", `{"serverUrl":"`+server.URL+`/"}`); err != nil {
		t.Fatalf("matching logout should succeed: %v", err)
	}
	if serverRequests != 1 || attackerRequests != 0 {
		t.Fatalf("logout server requests: owner=%d attacker=%d", serverRequests, attackerRequests)
	}
	if _, err := readSession(); !os.IsNotExist(err) {
		t.Fatalf("successful logout should remove session: %v", err)
	}
}

func TestDesktopRegistrationUsesRuntimeVersion(t *testing.T) {
	previous := misc.AppVersion
	misc.AppVersion = "2.3.4"
	t.Cleanup(func() { misc.AppVersion = previous })
	payload := desktopRegistrationPayload(deviceIdentity{}, "test device")
	if payload["app_version"] != misc.AppVersion {
		t.Fatalf("app_version = %v, want %s", payload["app_version"], misc.AppVersion)
	}
}

// blockingRegisterServer answers POST /devices/register only after release is closed; failFirst makes the
// first attempt fail with a generic refusal. requests counts register attempts.
func blockingRegisterServer(t *testing.T, failFirst bool) (server *httptest.Server, release func(), requests *atomic.Int32) {
	t.Helper()
	released := make(chan struct{})
	requests = &atomic.Int32{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mincode/v1/devices/register" {
			t.Errorf("unexpected request %s", r.URL.Path)
			return
		}
		if n := requests.Add(1); failFirst && n == 1 {
			_, _ = w.Write([]byte(`{"code":0,"msg":"设备登记暂不可用，请稍后重试","data":[]}`))
			return
		}
		select {
		case <-released:
		case <-r.Context().Done():
			return
		}
		writeRegisteredDevice(w, r)
	}))
	t.Cleanup(server.Close)
	var once sync.Once
	release = func() { once.Do(func() { close(released) }) }
	t.Cleanup(release)
	return server, release, requests
}

// statusWithin calls cloud_status and fails the test unless it returns within limit.
func statusWithin(t *testing.T, limit time.Duration) CloudStatus {
	t.Helper()
	type result struct {
		raw string
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := cloudStatusHandler("", "")
		done <- result{raw, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("cloud_status: %v", got.err)
		}
		var status CloudStatus
		if err := json.Unmarshal([]byte(got.raw), &status); err != nil {
			t.Fatal(err)
		}
		return status
	case <-time.After(limit):
		t.Fatal("cloud_status blocked on the in-flight device registration")
		return CloudStatus{}
	}
}

func waitRegistrationState(t *testing.T, want string) CloudStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := statusWithin(t, time.Second)
		if status.DeviceRegistration == want || time.Now().After(deadline) {
			if status.DeviceRegistration != want {
				t.Fatalf("registration state = %q, want %q (%+v)", status.DeviceRegistration, want, status)
			}
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCloudStatusReturnsWhileStartupRegistrationInFlight(t *testing.T) {
	server, release, requests := blockingRegisterServer(t, false)
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	previousStart := startRealtimeAfterRegistration
	startRealtimeAfterRegistration = func() { started <- struct{}{} }
	t.Cleanup(func() { startRealtimeAfterRegistration = previousStart })

	startupRelayRegistration(session, identity)
	if status := statusWithin(t, 2*time.Second); status.DeviceRegistration != relayRegistrationPending || !status.SignedIn {
		t.Fatalf("status while registering = %+v", status)
	}
	if registerRelayDeviceInBackground(session, identity, nil) {
		t.Fatal("a second background registration started while one is pending")
	}
	statusWithin(t, 2*time.Second)
	release()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("realtime was not started after the background registration succeeded")
	}
	if status := waitRegistrationState(t, relayRegistrationRegistered); !status.DeviceAvailable || status.DeviceRegistrationError != "" {
		t.Fatalf("status after registration = %+v", status)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("register requests = %d, want 1", got)
	}
}

func TestCloudStatusRetriesFailedRegistrationInBackgroundOnce(t *testing.T) {
	server, release, requests := blockingRegisterServer(t, true)
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := registerRelayDeviceAndRecord(context.Background(), session, identity); err == nil {
		t.Fatal("first registration should fail")
	}
	if status := statusWithin(t, 2*time.Second); status.DeviceRegistration != relayRegistrationPending || status.DeviceRegistrationError == "" {
		t.Fatalf("status must report the retry as pending and keep the last error: %+v", status)
	}
	for i := 0; i < 3; i++ {
		statusWithin(t, 2*time.Second)
	}
	release()
	waitRegistrationState(t, relayRegistrationRegistered)
	if got := requests.Load(); got != 2 {
		t.Fatalf("register requests = %d, want 2 (one failure, one single-flight retry)", got)
	}
}

// A registration that failed at login (device limit freed later, or a transient
// error) and succeeds on the status retry must connect the relay, not leave the
// phone unreachable until MinCode restarts.
func TestCloudStatusRetryConnectsRealtimeOnceRegistered(t *testing.T) {
	server, release, _ := blockingRegisterServer(t, true)
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	previousStart := startRealtimeAfterRegistration
	startRealtimeAfterRegistration = func() { started <- struct{}{} }
	t.Cleanup(func() { startRealtimeAfterRegistration = previousStart })
	if err := registerRelayDeviceAndRecord(context.Background(), session, identity); err == nil {
		t.Fatal("first registration should fail")
	}
	statusWithin(t, 2*time.Second)
	release()
	waitRegistrationState(t, relayRegistrationRegistered)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("realtime relay was not connected after the retried registration succeeded")
	}
}

func TestCancelRelayRegistrationStopsInFlightAttempt(t *testing.T) {
	server, _, _ := blockingRegisterServer(t, false)
	identity := seedMemberRelayContext(t, server.URL)
	session, _ := readSession()
	called := make(chan struct{}, 1)
	registerRelayDeviceInBackground(session, identity, func(error) { called <- struct{}{} })
	cancelRelayRegistration()
	select {
	case <-called:
		t.Fatal("cancelled registration must not run its completion (would start realtime after Shutdown)")
	case <-time.After(300 * time.Millisecond):
	}
	if state, _, _, _ := relayRegistrationState(session, identity); state == relayRegistrationPending {
		t.Fatalf("state after cancel = %q", state)
	}
}

func TestRealtimeDisconnectDuringStartupRegistrationIsNotUndone(t *testing.T) {
	server, release, requests := blockingRegisterServer(t, false)
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	previousStart := startRealtimeAfterRegistration
	startRealtimeAfterRegistration = func() { started <- struct{}{} }
	t.Cleanup(func() { startRealtimeAfterRegistration = previousStart })

	startupRelayRegistration(session, identity)
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() == 0 {
		t.Fatal("startup registration never reached the server")
	}
	if _, err := cloudRealtimeDisconnectHandler("", "{}"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-started:
		t.Fatal("startup registration reconnected realtime after the user disconnected")
	case <-time.After(500 * time.Millisecond):
	}
	if state, _, _, _ := relayRegistrationState(session, identity); state == relayRegistrationPending {
		t.Fatalf("registration still pending after disconnect: %q", state)
	}
}
