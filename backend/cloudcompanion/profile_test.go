package cloudcompanion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchMemberProfile(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	accountID := "42"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"fresh-token"}}`))
		case "/api/index/index":
			if r.Header.Get("ba-user-token") != "fresh-token" {
				t.Error("profile request was not authenticated")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"userInfo":{"id":` + accountID + `,"username":"user","nickname":"User","email":"member@example.com","mobile":"18888888888","money":"12.50","score":7,"join_time":1700000000,"last_login_time":1790241640,"last_login_ip":"127.0.0.1","motto":"bad\u0007text","token":"secret-token","refresh_token":"secret-refresh"}}}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if _, err := fetchMemberProfile(context.Background()); err == nil {
		t.Fatal("signed-out profile fetch succeeded")
	}
	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	profile, err := fetchMemberProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := AccountProfile{AccountID: 42, Username: "user", Nickname: "User", Email: "member@example.com", Mobile: "188****8888",
		Money: "12.50", Score: 7, JoinTime: 1700000000, LastLoginTime: 1790241640, LastLoginIP: "127.0.0.1"}
	if profile != want {
		t.Fatalf("profile = %+v, want %+v", profile, want)
	}
	encoded, _ := cloudAccountProfileHandler("", "")
	if strings.Contains(encoded, "secret") {
		t.Fatalf("token leaked into profile result: %s", encoded)
	}
	accountID = "43"
	if _, err := fetchMemberProfile(context.Background()); err == nil {
		t.Fatal("profile for another account accepted")
	}
}
