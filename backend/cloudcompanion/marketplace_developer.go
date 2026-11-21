package cloudcompanion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mincode/app/backend/core"
	"mincode/app/backend/marketpublish"
)

const developerAPIPath = "mincode/marketplaceDeveloper/"

var developerReasonMessages = map[string]string{
	"product_owned_by_other": "该插件 ID 已被其他开发者使用，请换一个 ID",
	"version_submitted":      "该版本已提交审核，请提高版本号",
	"version_published":      "该版本已发布，请提高版本号",
	"version_retired":        "该版本号已作废（审核被拒或已删除），请提高版本号后重新提交",
	"artifacts_missing":      "还有平台的安装包没有上传：%s",
	"cover_invalid":          "封面图片不符合要求（PNG、JPEG、WebP 或 GIF，不超过 2 MiB，宽 320–4096、高 180–4096 像素）",
	"developer_suspended":    "你的开发者权限已被暂停",
	"release_not_found":      "云端找不到这个版本草稿",
	"product_unavailable":    "插件在云端不可用",
}

func publishAccountChanged() error {
	return &marketpublish.APIError{Reason: "account_changed", Message: "云账号已切换，请重新运行 publish"}
}
func publishSession(ctx context.Context) (persistedSession, error) {
	if err := ctx.Err(); err != nil {
		return persistedSession{}, err
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) || err == nil && !isMemberSession(session) {
		return persistedSession{}, &marketpublish.APIError{Reason: "not_signed_in", Message: "请先在 MinCode 中登录云账号"}
	}
	if err != nil {
		return persistedSession{}, errors.New("无法读取云账号会话，请重新登录")
	}
	return session, nil
}

// Never show remote msg or JSON parser errors: either may contain credentials/body contents.
func decodeDeveloperResponse(response *http.Response, result any) error {
	raw, err := core.ReadAllLimited(response.Body, cloudMaxResponseBytes)
	var envelope cloudEnvelope
	if err != nil || json.Unmarshal(raw, &envelope) != nil {
		return errors.New("云账号响应无效，请稍后重试")
	}
	if isMemberAuthFailureCode(envelope.Code) {
		return errMemberTokenRejected
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != 1 {
		var detail struct {
			Reason  string   `json:"reason"`
			Missing []string `json:"missing"`
		}
		_ = json.Unmarshal(envelope.Data, &detail)
		message, known := developerReasonMessages[detail.Reason]
		if !known {
			message = "云账号请求未成功，请稍后重试"
			detail.Reason = "" // Only the cloud contract's fixed reason enum may cross this boundary.
		}
		// Missing platforms are display data, not arbitrary server text.
		var missing []string
		for _, target := range detail.Missing {
			switch target {
			case "darwin/arm64", "darwin/amd64", "windows/amd64", "linux/amd64":
				missing = append(missing, target)
			}
		}
		if detail.Reason == "artifacts_missing" {
			message = fmt.Sprintf(message, strings.Join(missing, "、"))
		}
		return &marketpublish.APIError{Reason: detail.Reason, Message: message, Missing: missing}
	}
	if result != nil {
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, result) != nil {
			return errors.New("云账号响应数据无效，请稍后重试")
		}
	}
	return nil
}

func developerRequest(ctx context.Context, session persistedSession, action string, query, fields url.Values, result any) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return withMemberToken(ctx, session, func(token string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameMemberSession(session) {
			return publishAccountChanged()
		}
		endpoint := strings.TrimRight(session.APIBaseURL, "/") + "/api/" + developerAPIPath + action
		method := http.MethodGet
		var body io.Reader
		if fields != nil {
			method = http.MethodPost
			body = strings.NewReader(fields.Encode())
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
		if err != nil {
			return errors.New("无法创建云账号请求")
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("ba-user-token", token)
		if fields != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response, err := marketPublishClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("云账号网络请求失败，请稍后重试")
		}
		defer response.Body.Close()
		if !sameMemberSession(session) {
			return publishAccountChanged()
		}
		return decodeDeveloperResponse(response, result)
	})
}

func MarketplaceLookup(ctx context.Context, productKey string) (marketpublish.Lookup, error) {
	session, err := publishSession(ctx)
	if err != nil {
		return marketpublish.Lookup{}, err
	}
	return marketplaceLookupSession(ctx, session, productKey)
}
func marketplaceLookupSession(ctx context.Context, session persistedSession, productKey string) (marketpublish.Lookup, error) {
	// The API flattens targets and spells the release identifier release_id, not id.
	var wire struct {
		Exists        bool   `json:"exists"`
		Owned         bool   `json:"owned"`
		ProductStatus string `json:"product_status"`
		Product       *struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Icon        string   `json:"icon"`
			CoverURL    string   `json:"cover_url"`
			Homepage    string   `json:"homepage"`
			Readme      string   `json:"readme"`
			Category    string   `json:"category"`
			Tags        []string `json:"tags"`
		} `json:"product"`
		Releases []struct {
			ID           int64  `json:"release_id"`
			Version      string `json:"version"`
			Status       string `json:"status"`
			ReviewStatus string `json:"review_status"`
			Targets      []struct {
				OS     string `json:"os"`
				Arch   string `json:"arch"`
				SHA256 string `json:"sha256"`
			} `json:"targets"`
		} `json:"releases"`
	}
	if err := developerRequest(ctx, session, "lookup", url.Values{"product_key": {productKey}}, nil, &wire); err != nil {
		return marketpublish.Lookup{}, err
	}
	lookup := marketpublish.Lookup{Exists: wire.Exists, Owned: wire.Owned, ProductStatus: wire.ProductStatus}
	if wire.Product != nil {
		p := wire.Product
		lookup.Product = &marketpublish.RemoteProduct{Name: p.Name, Description: p.Description, Icon: p.Icon, CoverURL: p.CoverURL, Homepage: p.Homepage, Readme: p.Readme, Category: p.Category, Tags: p.Tags}
	}
	for _, release := range wire.Releases {
		r := marketpublish.RemoteRelease{ID: release.ID, Version: release.Version, Status: release.Status, ReviewStatus: release.ReviewStatus}
		for _, target := range release.Targets {
			r.Targets = append(r.Targets, marketpublish.RemoteTarget{Target: marketpublish.Target{OS: target.OS, Arch: target.Arch}, SHA256: target.SHA256})
		}
		lookup.Releases = append(lookup.Releases, r)
	}
	return lookup, nil
}

func marketplaceDraftFields(in marketpublish.DraftInput) url.Values {
	r, p := in.Request, in.Request.Product
	fields := url.Values{"product_key": {r.PluginID}, "name": {p.Name}, "version": {r.Version}, "description": {p.Description}, "release_notes": {r.ReleaseNotes},
		"manifest_api": {strconv.Itoa(r.ManifestAPI)}, "cover_url": {in.CoverURL}, "homepage": {p.Homepage}, "icon": {p.Icon}, "readme": {p.Readme}, "category": {p.Category}}
	// ThinkPHP accepts repeated tags[]. An explicit empty string is the cloud's empty-list
	// representation; omission on update would preserve the old list instead of clearing it.
	if len(p.Tags) == 0 {
		fields.Set("tags", "")
	} else {
		fields["tags[]"] = append([]string{}, p.Tags...)
	}
	// Screenshots are intentionally absent: a shared product rejects changing them.
	return fields
}
func MarketplaceCreateDraft(ctx context.Context, in marketpublish.DraftInput) (int64, bool, error) {
	session, err := publishSession(ctx)
	if err != nil {
		return 0, false, err
	}
	return marketplaceCreateDraftSession(ctx, session, in)
}
func marketplaceCreateDraftSession(ctx context.Context, session persistedSession, in marketpublish.DraftInput) (int64, bool, error) {
	var result struct {
		ReleaseID int64 `json:"release_id"`
		Reused    bool  `json:"reused"`
	}
	err := developerRequest(ctx, session, "draft", nil, marketplaceDraftFields(in), &result)
	if err == nil && result.ReleaseID < 1 {
		err = errors.New("云端草稿响应无效")
	}
	return result.ReleaseID, result.Reused, err
}
func MarketplaceUpdateDraft(ctx context.Context, releaseID int64, in marketpublish.DraftInput) error {
	session, err := publishSession(ctx)
	if err != nil {
		return err
	}
	return marketplaceUpdateDraftSession(ctx, session, releaseID, in)
}
func marketplaceUpdateDraftSession(ctx context.Context, session persistedSession, releaseID int64, in marketpublish.DraftInput) error {
	fields := marketplaceDraftFields(in)
	fields.Set("release_id", strconv.FormatInt(releaseID, 10))
	return developerRequest(ctx, session, "update", nil, fields, nil)
}
func MarketplaceUploadCover(ctx context.Context, path string, progress func(int64, int64)) (string, error) {
	session, err := publishSession(ctx)
	if err != nil {
		return "", err
	}
	return marketplaceUploadCoverSession(ctx, session, path, progress)
}
func marketplaceUploadCoverSession(ctx context.Context, session persistedSession, path string, progress func(int64, int64)) (string, error) {
	info, err := marketUploadFileInfo(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 2<<20 {
		return "", &marketpublish.APIError{Reason: "cover_invalid", Message: developerReasonMessages["cover_invalid"]}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("无法读取封面图片")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("封面文件已发生变化")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 2<<20+1))
	if err != nil || n != info.Size() {
		return "", errors.New("封面文件已发生变化")
	}
	var result struct {
		CoverURL string `json:"cover_url"`
	}
	err = memberMultipartPostSession(ctx, session, developerAPIPath+"coverUpload", nil, "file", path, filepath.Base(path), fmt.Sprintf("%x", hash.Sum(nil)), progress, &result)
	if err == nil && result.CoverURL == "" {
		err = errors.New("云端封面响应无效")
	}
	return result.CoverURL, err
}
func MarketplaceUploadArtifact(ctx context.Context, releaseID int64, target marketpublish.Target, path, sha256 string, progress func(int64, int64)) error {
	return memberMultipartPost(ctx, developerAPIPath+"upload", artifactFields(releaseID, target), "file", path, filepath.Base(path), sha256, progress, nil)
}
func artifactFields(releaseID int64, target marketpublish.Target) url.Values {
	return url.Values{"release_id": {strconv.FormatInt(releaseID, 10)}, "os": {target.OS}, "arch": {target.Arch}}
}
func MarketplaceSubmit(ctx context.Context, releaseID int64) error {
	session, err := publishSession(ctx)
	if err != nil {
		return err
	}
	return developerRequest(ctx, session, "submit", nil, url.Values{"release_id": {strconv.FormatInt(releaseID, 10)}}, nil)
}
