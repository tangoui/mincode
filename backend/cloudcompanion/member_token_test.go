package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type memberTokenServer struct {
	refreshes atomic.Int32
	calls     atomic.Int32
	// respond decides the reply for the n-th (1-based) API call made with token.
	respond func(n int32, token string) string
}

func newMemberTokenServer(t *testing.T, respond func(n int32, token string) string) (*memberTokenServer, *httptest.Server) {
	t.Helper()
	state := &memberTokenServer{respond: respond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			n := state.refreshes.Add(1)
			if r.PostFormValue("refreshToken") != "refresh" {
				t.Errorf("wrong refresh token")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"type":"user-refresh","token":"minted-` + string(rune('0'+n)) + `"}}`))
		case "/api/test/item":
			n := state.calls.Add(1)
			_, _ = w.Write([]byte(state.respond(n, r.Header.Get("ba-user-token"))))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return state, server
}

func seedMemberSession(t *testing.T, baseURL string, token string, obtainedAt int64) {
	t.Helper()
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	if err := writeSession(persistedSession{APIBaseURL: baseURL, AccountID: 42, AccessToken: token, RefreshToken: "refresh", AuthProvider: "member", AccessTokenObtainedAt: obtainedAt}); err != nil {
		t.Fatal(err)
	}
}

func fixMemberTokenClock(t *testing.T, now time.Time) {
	t.Helper()
	previous := memberTokenNow
	memberTokenNow = func() time.Time { return now }
	t.Cleanup(func() { memberTokenNow = previous })
}

func TestMemberTokenIsCachedUntilNearExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	state, server := newMemberTokenServer(t, func(_ int32, token string) string {
		return `{"code":1,"data":{"token":"` + token + `"}}`
	})
	// A session file written before the obtain time existed forces exactly one refresh.
	seedMemberSession(t, server.URL, "legacy-access", 0)
	var out struct {
		Token string `json:"token"`
	}
	for i := 0; i < 3; i++ {
		if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &out); err != nil || out.Token != "minted-1" {
			t.Fatalf("call %d: token %q, %v", i, out.Token, err)
		}
	}
	if got := state.refreshes.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want 1", got)
	}
	session, err := readSession()
	if err != nil || session.AccessToken != "minted-1" || session.AccessTokenObtainedAt != now.Unix() {
		t.Fatalf("stored session = %+v, %v", session, err)
	}

	// Still fresh just inside the margin; stale just past it.
	fixMemberTokenClock(t, now.Add(memberAccessTokenLifetime-memberAccessTokenRefreshMargin-time.Minute))
	if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &out); err != nil || state.refreshes.Load() != 1 {
		t.Fatalf("fresh token refreshed: %v (%d)", err, state.refreshes.Load())
	}
	fixMemberTokenClock(t, now.Add(memberAccessTokenLifetime-memberAccessTokenRefreshMargin))
	if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &out); err != nil || out.Token != "minted-2" || state.refreshes.Load() != 2 {
		t.Fatalf("stale token not refreshed: %q %v (%d)", out.Token, err, state.refreshes.Load())
	}
}

func TestMemberTokenFutureObtainTimeIsRefreshed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	state, server := newMemberTokenServer(t, func(_ int32, _ string) string { return `{"code":1,"data":{}}` })
	seedMemberSession(t, server.URL, "access", now.Add(time.Hour).Unix())
	if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &struct{}{}); err != nil || state.refreshes.Load() != 1 {
		t.Fatalf("future obtain time trusted: %v (%d)", err, state.refreshes.Load())
	}
}

func TestMemberTokenRejectedIsRefreshedAndRetriedOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	for _, code := range []int{409, 303} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			state, server := newMemberTokenServer(t, func(n int32, token string) string {
				if n == 1 {
					if token != "cached" {
						t.Errorf("first call used %q, want cached token", token)
					}
					return `{"code":` + strconv.Itoa(code) + `,"msg":"Token expiration","data":null}`
				}
				return `{"code":1,"data":{"token":"` + token + `"}}`
			})
			seedMemberSession(t, server.URL, "cached", now.Unix())
			var out struct {
				Token string `json:"token"`
			}
			if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &out); err != nil || out.Token != "minted-1" {
				t.Fatalf("retry = %q, %v", out.Token, err)
			}
			if state.refreshes.Load() != 1 || state.calls.Load() != 2 {
				t.Fatalf("refreshes=%d calls=%d, want 1 and 2", state.refreshes.Load(), state.calls.Load())
			}
		})
	}
}

func TestMemberTokenRetriesOnlyOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	state, server := newMemberTokenServer(t, func(_ int32, _ string) string {
		return `{"code":409,"msg":"Token expiration","data":null}`
	})
	seedMemberSession(t, server.URL, "cached", now.Unix())
	_, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &struct{}{})
	if !errors.Is(err, errMemberTokenRejected) || state.calls.Load() != 2 || state.refreshes.Load() != 1 {
		t.Fatalf("err=%v calls=%d refreshes=%d", err, state.calls.Load(), state.refreshes.Load())
	}
}

func TestMemberTokenPermissionFailureIsNotRetried(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	state, server := newMemberTokenServer(t, func(_ int32, _ string) string {
		return `{"code":401,"msg":"You have no permission","data":null}`
	})
	seedMemberSession(t, server.URL, "cached", now.Unix())
	if _, err := memberRequest(context.Background(), http.MethodGet, "/api/test/item", nil, &struct{}{}); err == nil || errors.Is(err, errMemberTokenRejected) {
		t.Fatalf("permission failure treated as auth failure: %v", err)
	}
	if state.calls.Load() != 1 || state.refreshes.Load() != 0 {
		t.Fatalf("calls=%d refreshes=%d, want 1 and 0", state.calls.Load(), state.refreshes.Load())
	}
}

func TestPersistedSessionWithoutObtainTimeStillDecodes(t *testing.T) {
	var session persistedSession
	legacy := `{"apiBaseUrl":"https://api.example.com","accountId":42,"email":"","accessToken":"a","refreshToken":"r","expiresAt":"0001-01-01T00:00:00Z","authProvider":"member"}`
	if err := json.Unmarshal([]byte(legacy), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePersistedSession(session); err != nil || session.AccessTokenObtainedAt != 0 {
		t.Fatalf("legacy session = %+v, %v", session, err)
	}
	if memberAccessTokenFresh(session, time.Now()) {
		t.Fatal("legacy session without obtain time must refresh once")
	}
}

func TestAuthorizedCloudGetRefreshesOnMemberTokenRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fixMemberTokenClock(t, now)
	var calls, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"code":1,"data":{"type":"user-refresh","token":"fresh-access"}}`))
		case "/api/mincode/v1/devices":
			switch calls.Add(1) {
			case 1:
				if r.Header.Get("Authorization") != "Bearer cached" {
					t.Errorf("first call must use the cached token")
				}
				_, _ = w.Write([]byte(`{"code":409,"msg":"Token expiration","data":null}`))
			default:
				if r.Header.Get("Authorization") != "Bearer fresh-access" {
					t.Errorf("retry must use the refreshed token")
				}
				_, _ = w.Write([]byte(`{"code":1,"data":{"items":[],"stats":{"total":0,"desktop":0,"mobile":0}}}`))
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	seedMemberSession(t, server.URL, "cached", now.Unix())
	identity, err := loadOrCreateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	var devices serverDeviceList
	if err := authorizedCloudGet(context.Background(), session, identity, "/devices", &devices); err != nil {
		t.Fatalf("retry after 409 failed: %v", err)
	}
	if calls.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("calls=%d refreshes=%d, want 2 and 1", calls.Load(), refreshes.Load())
	}
	stored, err := readSession()
	if err != nil || stored.AccessToken != "fresh-access" {
		t.Fatalf("session must survive with the refreshed token: %+v %v", stored, err)
	}
}
