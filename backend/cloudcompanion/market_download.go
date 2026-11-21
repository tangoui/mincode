package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
)

const marketArtifactTimeout = 3 * time.Minute

// marketArtifactClient is shared by all artifact downloads so connections are
// reused; idle ones expire on their own instead of being closed per request.
// It must not inherit proxy credentials or a cookie jar, and never follows
// redirects: the member token may only reach the session's own server, and the
// download link the server returns is fetched as is.
var marketArtifactClient = &http.Client{
	Timeout: marketArtifactTimeout,
	Transport: &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type marketArtifactLink struct {
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// DownloadMarketArtifact downloads one published marketplace artifact for the
// signed-in member in two steps: the member token goes only to the session's
// own server, which checks the entitlement and returns a short-lived link (a
// signed OSS URL, or a ticket link on the same server); the link is then
// fetched without any credential. That server must be the marketplace origin
// the release metadata came from. Callers must still verify the size and
// SHA-256 against the release listing.
func DownloadMarketArtifact(ctx context.Context, marketOrigin string, artifactID, maxBytes int64, destination io.Writer) (int64, error) {
	if artifactID < 1 || maxBytes < 1 {
		return 0, errors.New("插件产物参数无效")
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return 0, errors.New("请先在设置中登录云账号，再从插件市场安装")
	}
	if err != nil {
		return 0, fmt.Errorf("读取云账号会话: %w", err)
	}
	if !isMemberSession(session) {
		return 0, errors.New("当前会话不是云端会员账号，请重新登录")
	}
	if strings.TrimRight(session.APIBaseURL, "/") != strings.TrimRight(marketOrigin, "/") {
		return 0, fmt.Errorf("插件市场（%s）与已登录的云账号服务器（%s）不一致", marketOrigin, session.APIBaseURL)
	}
	link, err := requestMarketArtifactLink(ctx, session, artifactID)
	if err != nil {
		return 0, err
	}
	if link.SizeBytes > maxBytes {
		return 0, errors.New("插件包大小与发布记录不一致")
	}
	downloadURL, err := resolveMarketArtifactURL(session.APIBaseURL, link.URL)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return 0, fmt.Errorf("创建插件下载请求: %w", err)
	}
	response, err := marketArtifactClient.Do(request)
	if err != nil {
		return 0, errors.New("插件下载失败，请检查网络后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("插件下载失败（HTTP %d）：下载链接可能已过期，请重试", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return 0, errors.New("插件包大小与发布记录不一致")
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return written, fmt.Errorf("下载插件包: %w", err)
	}
	if written > maxBytes {
		return written, errors.New("插件包大小与发布记录不一致")
	}
	if !sameMemberSession(session) {
		return written, errors.New("云账号会话已切换，请重试")
	}
	return written, nil
}

// errMarketPaidHostRequired is the cloud's refusal for plugins that need a paid host plan.
var errMarketPaidHostRequired = errors.New("该插件需要 MinCode 主程序付费套餐（年度或永久），请先在 设置 → 云账号 购买或激活")

// errMarketDownloadQuota is the cloud's refusal once the daily download quota is used up.
var errMarketDownloadQuota = errors.New("今日插件下载次数已达上限，请明天再试")

func marketRefusalReason(data json.RawMessage) string {
	var detail struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(data, &detail) != nil {
		return ""
	}
	return detail.Reason
}

func requestMarketArtifactLink(ctx context.Context, session persistedSession, artifactID int64) (marketArtifactLink, error) {
	endpoint := strings.TrimRight(session.APIBaseURL, "/") + "/api/mincode/marketplaceDownload/link?artifact_id=" + strconv.FormatInt(artifactID, 10)
	var link marketArtifactLink
	err := withMemberToken(ctx, session, func(token string) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("创建插件下载请求: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("ba-user-token", token)
		response, err := marketArtifactClient.Do(request)
		if err != nil {
			return errors.New("插件下载请求失败，请稍后重试")
		}
		defer response.Body.Close()
		raw, err := core.ReadAllLimited(response.Body, 64*1024)
		var envelope cloudEnvelope
		if err != nil || json.Unmarshal(raw, &envelope) != nil {
			return fmt.Errorf("云端下载链接响应无效（HTTP %d）", response.StatusCode)
		}
		// BuildAdmin reports refusals as a JSON envelope, often with HTTP 200.
		if isMemberAuthFailureCode(envelope.Code) {
			return errMemberTokenRejected
		}
		if envelope.Code != 1 {
			switch marketRefusalReason(envelope.Data) {
			case "requires_paid_host":
				return errMarketPaidHostRequired
			case "download_quota":
				return errMarketDownloadQuota
			}
		}
		if response.StatusCode != http.StatusOK || envelope.Code != 1 || json.Unmarshal(envelope.Data, &link) != nil || link.URL == "" {
			return fmt.Errorf("云端拒绝下载该插件（HTTP %d）：可能未发布、当前账号无下载授权或会话已失效", response.StatusCode)
		}
		return nil
	})
	return link, err
}

// resolveMarketArtifactURL accepts a link on the session's own server (local
// storage tickets are relative) or an HTTPS link elsewhere (signed OSS URLs).
func resolveMarketArtifactURL(baseURL, raw string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return "", errors.New("云账号服务器地址无效")
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || reference.User != nil || reference.Fragment != "" {
		return "", errors.New("云端返回的下载链接无效")
	}
	resolved := base.ResolveReference(reference)
	sameOrigin := strings.EqualFold(resolved.Scheme, base.Scheme) && strings.EqualFold(resolved.Host, base.Host)
	if resolved.Host == "" || (resolved.Scheme != "https" && !sameOrigin) {
		return "", errors.New("云端返回的下载链接必须使用 HTTPS")
	}
	return resolved.String(), nil
}
