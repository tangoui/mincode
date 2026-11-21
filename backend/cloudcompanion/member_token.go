package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// errMemberTokenRejected marks a BuildAdmin auth failure (code 409 token expired, 303 not
// logged in). Code 401 is a permission failure and is deliberately not included.
var errMemberTokenRejected = errors.New("云账号会话已失效，请重新登录")

const (
	// BuildAdmin user_token_keep_time is 3 days; refresh well before that so clock drift or a
	// shorter server setting still ends in a normal refresh rather than a failed request.
	memberAccessTokenLifetime      = 72 * time.Hour
	memberAccessTokenRefreshMargin = 12 * time.Hour
	memberAccessTokenMaxFutureSkew = 5 * time.Minute
)

var (
	memberTokenMu  sync.Mutex
	memberTokenNow = time.Now
)

func isMemberAuthFailureCode(code int) bool {
	return code == 303 || code == 409
}

func sameMemberCredential(a, b persistedSession) bool {
	return isMemberSession(a) && isMemberSession(b) && a.AccountID == b.AccountID &&
		a.APIBaseURL == b.APIBaseURL && a.RefreshToken == b.RefreshToken
}

func memberAccessTokenFresh(session persistedSession, now time.Time) bool {
	if strings.TrimSpace(session.AccessToken) == "" || session.AccessTokenObtainedAt <= 0 {
		return false
	}
	age := now.Sub(time.Unix(session.AccessTokenObtainedAt, 0))
	return age > -memberAccessTokenMaxFutureSkew && age < memberAccessTokenLifetime-memberAccessTokenRefreshMargin
}

// memberAccessToken returns the cached access token for session, refreshing it only when it is
// missing, near expiry, or equal to rejected (a token the server just refused). Concurrent
// callers share one refresh: whoever arrives second sees the new token and reuses it.
func memberAccessToken(ctx context.Context, session persistedSession, rejected string) (string, error) {
	memberTokenMu.Lock()
	defer memberTokenMu.Unlock()
	current, err := readSession()
	if err != nil || !sameMemberCredential(current, session) {
		return "", errors.New("云账号会话已切换，请重试")
	}
	if memberAccessTokenFresh(current, memberTokenNow()) && current.AccessToken != rejected {
		return current.AccessToken, nil
	}
	var refreshed cloudRefreshResponse
	if err := cloudPost(ctx, current.APIBaseURL, "common/refreshToken", url.Values{"refreshToken": {current.RefreshToken}}, "", &refreshed); err != nil {
		return "", fmt.Errorf("验证云账号会话: %w", err)
	}
	token := strings.TrimSpace(refreshed.Token)
	if token == "" {
		return "", errors.New("刷新云账号令牌响应无效")
	}
	next := current
	next.AccessToken = token
	next.AccessTokenObtainedAt = memberTokenNow().Unix()
	written, err := replaceSessionIf(func(stored persistedSession) bool { return sameMemberCredential(stored, current) }, next)
	if err != nil {
		return "", err
	}
	if !written {
		return "", errors.New("云账号会话已切换，请重试")
	}
	return token, nil
}

// withMemberToken runs call with the cached access token. If the server rejects that token
// (errMemberTokenRejected) it refreshes once and retries once; any other error is returned as is.
func withMemberToken(ctx context.Context, session persistedSession, call func(token string) error) error {
	token, err := memberAccessToken(ctx, session, "")
	if err != nil {
		return err
	}
	err = call(token)
	if !errors.Is(err, errMemberTokenRejected) {
		return err
	}
	token, err = memberAccessToken(ctx, session, token)
	if err != nil {
		return err
	}
	return call(token)
}

// memberGet performs an authenticated GET and returns the HTTP status and decoded envelope.
// A well-formed envelope carrying an auth failure code yields errMemberTokenRejected; other
// validation is left to the caller.
func memberGet(ctx context.Context, baseURL, path, token string, limit int64) (int, cloudEnvelope, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return 0, cloudEnvelope{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("ba-user-token", token)
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return 0, cloudEnvelope{}, errors.New("云账号网络请求失败，请稍后重试")
	}
	defer response.Body.Close()
	raw, err := core.ReadAllLimited(response.Body, limit)
	if err != nil {
		return response.StatusCode, cloudEnvelope{}, err
	}
	var envelope cloudEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return response.StatusCode, cloudEnvelope{}, nil
	}
	if isMemberAuthFailureCode(envelope.Code) {
		return response.StatusCode, envelope, errMemberTokenRejected
	}
	return response.StatusCode, envelope, nil
}
