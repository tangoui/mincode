package cloudcompanion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/marketpublish"
)

func developerTestSession(t *testing.T, base string) {
	t.Helper()
	root := t.TempDir()
	previous := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previous })
	if err := writeSession(persistedSession{APIBaseURL: base, AccountID: 42, Email: "dev@example.com", AuthProvider: "member",
		AccessToken: "test-member-access", RefreshToken: "test-member-refresh", AccessTokenObtainedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
}

func developerTestFile(t *testing.T, contents []byte) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.min")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, fmt.Sprintf("%x", sha256.Sum256(contents))
}

func TestMemberMultipartPostStreamsFileWithToken(t *testing.T) {
	payload := bytes.Repeat([]byte("PK archive data"), 30000)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/mincode/marketplaceDeveloper/upload" || r.Method != http.MethodPost || r.Header.Get("ba-user-token") != "test-member-access" {
			t.Errorf("wrong upload route, method, or credential")
		}
		if r.ContentLength != -1 {
			t.Error("upload must stream rather than prebuffer the body")
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		fields := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			raw, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
				return
			}
			if part.FormName() == "file" {
				if part.FileName() != "release.min" || !bytes.Equal(raw, payload) {
					t.Error("wrong multipart filename or bytes")
				}
			} else {
				fields[part.FormName()] = string(raw)
			}
		}
		if !reflect.DeepEqual(fields, map[string]string{"release_id": "7", "os": "darwin", "arch": "arm64"}) {
			t.Errorf("fields = %v", fields)
		}
		_, _ = io.WriteString(w, `{"code":1,"data":{"artifact_id":12}}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, hash := developerTestFile(t, payload)
	var sent, total int64
	var response struct {
		ArtifactID int64 `json:"artifact_id"`
	}
	err := memberMultipartPost(context.Background(), "mincode/marketplaceDeveloper/upload", url.Values{"release_id": {"7"}, "os": {"darwin"}, "arch": {"arm64"}}, "file", path, "release.min", hash,
		func(s, n int64) { sent, total = s, n }, &response)
	if err != nil || requests.Load() != 1 || sent != int64(len(payload)) || sent != total || response.ArtifactID != 12 {
		t.Fatalf("upload err=%v requests=%d progress=%d/%d response=%+v", err, requests.Load(), sent, total, response)
	}
}

func TestMemberMultipartPostRejectsChangedFile(t *testing.T) {
	complete := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		complete <- err == nil
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, _ := developerTestFile(t, bytes.Repeat([]byte("changed archive"), 10000))
	err := memberMultipartPost(context.Background(), "mincode/marketplaceDeveloper/upload", nil, "file", path, "p.min", strings.Repeat("0", 64), nil, nil)
	if err == nil || err.Error() != "安装包在确认后发生变化，已停止上传" {
		t.Fatalf("err = %v", err)
	}
	select {
	case got := <-complete:
		if got {
			t.Fatal("server received a complete request despite digest mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upload request did not finish")
	}
}

func TestMemberMultipartPostRetriesAfterTokenRefresh(t *testing.T) {
	payload := bytes.Repeat([]byte("archive"), 1000)
	var requests, refreshes atomic.Int32
	var uploaded atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/common/refreshToken" {
			refreshes.Add(1)
			_, _ = io.WriteString(w, `{"code":1,"data":{"token":"test-fresh-access"}}`)
			return
		}
		n := requests.Add(1)
		token := "test-member-access"
		if n == 2 {
			token = "test-fresh-access"
		}
		if r.Header.Get("ba-user-token") != token {
			t.Error("wrong member token for retry")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(raw, payload) {
			t.Errorf("retry did not reopen full file: %v", err)
		}
		uploaded.Add(int64(len(raw)))
		if n == 1 {
			_, _ = io.WriteString(w, `{"code":409,"data":{}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, hash := developerTestFile(t, payload)
	var attempts int
	err := memberMultipartPost(context.Background(), "mincode/marketplaceDeveloper/upload", nil, "file", path, "p.min", hash,
		func(sent, total int64) {
			if sent == total {
				attempts++
			}
		}, nil)
	if err != nil || requests.Load() != 2 || refreshes.Load() != 1 || uploaded.Load() != 2*int64(len(payload)) || attempts != 2 {
		t.Fatalf("err=%v requests=%d refresh=%d bytes=%d reads=%d", err, requests.Load(), refreshes.Load(), uploaded.Load(), attempts)
	}
}

func TestMarketplaceUploadRejectsOversize(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, hash := developerTestFile(t, nil)
	if err := os.Truncate(path, 64<<20+1); err != nil {
		t.Fatal(err)
	}
	err := MarketplaceUploadArtifact(context.Background(), 7, marketpublish.Target{OS: "darwin", Arch: "arm64"}, path, hash, nil)
	if err == nil || !strings.Contains(err.Error(), "64 MiB") || requests.Load() != 0 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
}

func TestDeveloperAPIErrorReasons(t *testing.T) {
	cases := []struct {
		reason, message string
		status          int
		detail          string
	}{
		{"product_owned_by_other", "该插件 ID 已被其他开发者使用，请换一个 ID", 200, ""},
		{"version_submitted", "该版本已提交审核，请提高版本号", 200, ""},
		{"version_published", "该版本已发布，请提高版本号", 200, ""},
		{"version_retired", "该版本号已作废（审核被拒或已删除），请提高版本号后重新提交", 200, ""},
		{"artifacts_missing", "还有平台的安装包没有上传：darwin/amd64", 200, `,"missing":["darwin/amd64"]`},
		{"cover_invalid", "封面图片不符合要求（PNG、JPEG、WebP 或 GIF，不超过 2 MiB，宽 320–4096、高 180–4096 像素）", 200, `,"problem":"storage"`},
		{"developer_suspended", "你的开发者权限已被暂停", 403, ""},
		{"release_not_found", "云端找不到这个版本草稿", 404, ""},
		{"product_unavailable", "插件在云端不可用", 200, ""},
		{"unknown-server-reason", "云账号请求未成功，请稍后重试", 200, ""},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = fmt.Fprintf(w, `{"code":0,"msg":"untrusted secret content","data":{"reason":%q%s}}`, c.reason, c.detail)
			}))
			defer server.Close()
			developerTestSession(t, server.URL)
			err := MarketplaceSubmit(context.Background(), 7)
			var apiErr *marketpublish.APIError
			wantReason := c.reason
			if c.reason == "unknown-server-reason" {
				wantReason = ""
			}
			if !errors.As(err, &apiErr) || apiErr.Message != c.message || apiErr.Reason != wantReason {
				t.Fatalf("err=%#v", err)
			}
			if c.reason == "artifacts_missing" && !reflect.DeepEqual(apiErr.Missing, []string{"darwin/amd64"}) {
				t.Fatalf("missing=%v", apiErr.Missing)
			}
		})
	}
}

func TestMarketplaceLookupDecodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/mincode/marketplaceDeveloper/lookup" || r.URL.Query().Get("product_key") != "acme.notes" || r.Header.Get("ba-user-token") != "test-member-access" {
			t.Error("wrong lookup request")
		}
		_, _ = io.WriteString(w, `{"code":1,"data":{"exists":true,"owned":true,"product_status":"published","product":{"name":"Notes","description":"desc","icon":"note","cover_url":"https://cdn.example.com/a.png","homepage":"https://example.com","readme":"# Notes","category":"productivity","tags":["notes"]},"releases":[{"release_id":7,"version":"1.2.0","status":"draft","review_status":"not_submitted","targets":[{"os":"darwin","arch":"arm64","size_bytes":4,"sha256":"abcd","status":"uploaded"}]}]}}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	lookup, err := MarketplaceLookup(context.Background(), "acme.notes")
	if err != nil || !lookup.Exists || !lookup.Owned || lookup.ProductStatus != "published" || lookup.Product == nil || lookup.Product.CoverURL != "https://cdn.example.com/a.png" || len(lookup.Releases) != 1 {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	release := lookup.Releases[0]
	if release.ID != 7 || release.ReviewStatus != "not_submitted" || len(release.Targets) != 1 || release.Targets[0].Target != (marketpublish.Target{OS: "darwin", Arch: "arm64"}) || release.Targets[0].SHA256 != "abcd" {
		t.Fatalf("release=%+v", release)
	}
}

func TestMarketplaceDraftSendsFullMetadataWithoutScreenshots(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		for key, want := range map[string]string{"product_key": "acme.notes", "name": "Notes", "version": "1.2.0", "description": "desc", "release_notes": "- change", "manifest_api": "2", "cover_url": "https://cdn.example.com/cover.png", "homepage": "https://example.com", "icon": "note", "category": "productivity", "readme": "# Notes"} {
			if r.PostForm.Get(key) != want {
				t.Errorf("field %s=%q want %q", key, r.PostForm.Get(key), want)
			}
		}
		if r.Header.Get("ba-user-token") != "test-member-access" || r.Method != "POST" {
			t.Error("not authenticated POST")
		}
		if !reflect.DeepEqual(r.PostForm["tags[]"], []string{"notes", "text"}) || r.PostForm.Has("screenshots") || r.PostForm.Has("screenshots[]") {
			t.Errorf("tags or screenshots=%v", r.PostForm)
		}
		if strings.HasSuffix(r.URL.Path, "/update") && r.PostForm.Get("release_id") != "7" {
			t.Error("missing update release_id")
		}
		_, _ = io.WriteString(w, `{"code":1,"data":{"release_id":7,"reused":true}}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	in := marketpublish.DraftInput{Request: marketpublish.Request{PluginID: "acme.notes", Version: "1.2.0", ReleaseNotes: "- change", ManifestAPI: 2,
		Product: marketpublish.Product{Name: "Notes", Description: "desc", Icon: "note", Homepage: "https://example.com", Readme: "# Notes", Category: "productivity", Tags: []string{"notes", "text"}}}, CoverURL: "https://cdn.example.com/cover.png"}
	id, reused, err := MarketplaceCreateDraft(context.Background(), in)
	if err != nil || id != 7 || !reused {
		t.Fatalf("create=%d/%v %v", id, reused, err)
	}
	if err := MarketplaceUpdateDraft(context.Background(), 7, in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/api/mincode/marketplaceDeveloper/draft", "/api/mincode/marketplaceDeveloper/update"}) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestMarketplacePublishAPIBindsAccountAcrossSteps(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	api := MarketPublishAPI()
	email, signedIn, err := api.Account(context.Background())
	if err != nil || !signedIn || email != "dev@example.com" {
		t.Fatalf("account=%s/%v %v", email, signedIn, err)
	}
	// Even an account with the same visible email on the same server must be re-confirmed.
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	session.AccountID = 43
	if err := writeSession(session); err != nil {
		t.Fatal(err)
	}
	err = api.Submit(context.Background(), 7)
	var apiErr *marketpublish.APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "account_changed" || writes.Load() != 0 {
		t.Fatalf("err=%v writes=%d", err, writes.Load())
	}
}

func TestMarketplacePublishAPIAwaitingLoginAndCoverNoCredentials(t *testing.T) {
	var coverRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coverRequests.Add(1)
		if r.Header.Get("ba-user-token") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("cover fetch leaked credentials")
		}
		_, _ = io.WriteString(w, "cover bytes")
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteSession(); err != nil {
		t.Fatal(err)
	}
	api := MarketPublishAPI()
	if _, signedIn, err := api.Account(context.Background()); err != nil || signedIn {
		t.Fatalf("signed-out=%v %v", signedIn, err)
	}
	if err := writeSession(session); err != nil {
		t.Fatal(err)
	}
	if _, signedIn, err := api.Account(context.Background()); err != nil || !signedIn {
		t.Fatalf("after login=%v %v", signedIn, err)
	}
	raw, err := api.FetchCover(context.Background(), server.URL+"/cover.png")
	if err != nil || string(raw) != "cover bytes" || coverRequests.Load() != 1 {
		t.Fatalf("cover=%q %v", raw, err)
	}
	if got := api.DeveloperURL(context.Background()); got != server.URL+"/marketplace?tab=account" {
		t.Fatalf("developer URL=%q", got)
	}
}

func TestMemberMultipartPostRevalidatesFileOnRetry(t *testing.T) {
	payload := []byte("original archive")
	path, hash := developerTestFile(t, payload)
	other, _ := developerTestFile(t, []byte("must never be read"))
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/common/refreshToken" {
			_, _ = io.WriteString(w, `{"code":1,"data":{"token":"test-fresh-access"}}`)
			return
		}
		uploads.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, path); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"code":409}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	err := memberMultipartPost(context.Background(), developerAPIPath+"upload", nil, "file", path, "p.min", hash, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "普通文件") || uploads.Load() != 1 {
		t.Fatalf("err=%v uploads=%d", err, uploads.Load())
	}
}

func TestMemberMultipartPostCancelsAndStopsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) }))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, hash := developerTestFile(t, bytes.Repeat([]byte("archive"), 100000))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	err := memberMultipartPost(ctx, developerAPIPath+"upload", nil, "file", path, "p.min", hash, func(sent, total int64) { calls.Add(1); cancel() }, nil)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("err=%v progress calls=%d", err, calls.Load())
	}
}

func TestMemberMultipartPostOnlyOneRefreshRetry(t *testing.T) {
	for _, code := range []int{303, 409} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var uploads, refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/common/refreshToken" {
					refreshes.Add(1)
					_, _ = io.WriteString(w, `{"code":1,"data":{"token":"test-fresh-access"}}`)
					return
				}
				uploads.Add(1)
				// Deliberately reject before consuming the file; the producer must not hang.
				_, _ = fmt.Fprintf(w, `{"code":%d}`, code)
			}))
			defer server.Close()
			developerTestSession(t, server.URL)
			path, hash := developerTestFile(t, bytes.Repeat([]byte("archive"), 100000))
			err := memberMultipartPost(context.Background(), developerAPIPath+"upload", nil, "file", path, "p.min", hash, nil, nil)
			if !errors.Is(err, errMemberTokenRejected) || uploads.Load() != 2 || refreshes.Load() != 1 {
				t.Fatalf("err=%v uploads=%d refresh=%d", err, uploads.Load(), refreshes.Load())
			}
		})
	}
}

func TestMarketplaceDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	if _, err := MarketplaceLookup(context.Background(), "acme.notes"); err == nil {
		t.Error("accepted redirected lookup")
	}
	if err := MarketplaceSubmit(context.Background(), 7); err == nil {
		t.Error("accepted redirected submit")
	}
	path, hash := developerTestFile(t, []byte("archive"))
	if err := MarketplaceUploadArtifact(context.Background(), 7, marketpublish.Target{OS: "darwin", Arch: "arm64"}, path, hash, nil); err == nil {
		t.Error("accepted redirected upload")
	}
	api := MarketPublishAPI()
	if _, _, err := api.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := api.FetchCover(context.Background(), server.URL+"/cover.png"); err == nil {
		t.Error("accepted redirected cover")
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target received %d requests", redirected.Load())
	}
}

func TestMarketplaceCoverUploadUsesCloudContract(t *testing.T) {
	payload := []byte("cover validation belongs to cloud")
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		if r.URL.Path != "/api/mincode/marketplaceDeveloper/coverUpload" || r.Header.Get("ba-user-token") != "test-member-access" {
			t.Error("wrong cover request")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		raw, _ := io.ReadAll(file)
		if !bytes.Equal(raw, payload) {
			t.Error("wrong cover bytes")
		}
		_, _ = io.WriteString(w, `{"code":1,"data":{"cover_url":"https://cdn.example.com/cover.png"}}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	path, _ := developerTestFile(t, payload)
	got, err := MarketplaceUploadCover(context.Background(), path, nil)
	if err != nil || got != "https://cdn.example.com/cover.png" || uploads.Load() != 1 {
		t.Fatalf("cover=%q %v", got, err)
	}
	if err := os.Truncate(path, 2<<20+1); err != nil {
		t.Fatal(err)
	}
	_, err = MarketplaceUploadCover(context.Background(), path, nil)
	var apiErr *marketpublish.APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "cover_invalid" || uploads.Load() != 1 {
		t.Fatalf("oversized cover err=%v uploads=%d", err, uploads.Load())
	}
}

func TestDeveloperAPIErrorDoesNotExposeUntrustedDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"test-member-access","data":{"reason":"test-member-access","missing":["test-member-refresh"]}}`)
	}))
	defer server.Close()
	developerTestSession(t, server.URL)
	err := MarketplaceSubmit(context.Background(), 7)
	var apiErr *marketpublish.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err=%v", err)
	}
	encoded, marshalErr := json.Marshal(apiErr)
	if marshalErr != nil || strings.Contains(string(encoded), "test-member-") || apiErr.Message != "云账号请求未成功，请稍后重试" {
		t.Fatalf("refusal exposed untrusted details")
	}
}

func TestMarketplaceRequestsHonorCancelWhileReadingResponse(t *testing.T) {
	for _, kind := range []string{"lookup", "upload"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":1,`)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			developerTestSession(t, server.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-started; cancel() }()
			var err error
			if kind == "lookup" {
				_, err = MarketplaceLookup(ctx, "acme.notes")
			} else {
				path, hash := developerTestFile(t, []byte("archive"))
				err = MarketplaceUploadArtifact(ctx, 7, marketpublish.Target{OS: "darwin", Arch: "arm64"}, path, hash, nil)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestMarketplaceFetchCoverRejectsCrossOriginPrivateService(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "private service body")
	}))
	defer server.Close()
	previous := marketPublishClient
	client := *previous
	transport := previous.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	client.Transport = transport
	marketPublishClient = &client
	t.Cleanup(func() { marketPublishClient = previous; transport.CloseIdleConnections() })
	developerTestSession(t, "https://api.example.invalid")
	api := MarketPublishAPI()
	if _, _, err := api.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := api.FetchCover(context.Background(), server.URL+"/internal"); err == nil || requests.Load() != 0 {
		t.Fatalf("cross-origin private service was reached: requests=%d err=%v", requests.Load(), err)
	}
}

func TestMarketplaceCoverURLBoundary(t *testing.T) {
	for _, c := range []struct {
		base, raw              string
		allowed, sessionOrigin bool
	}{
		{"http://127.0.0.1:8181", "/cover.png", true, true},
		{"http://127.0.0.1:8181", "http://127.0.0.1:8181/cover.png", true, true},
		{"http://127.0.0.1:8181", "http://127.0.0.1:8182/cover.png", false, false},
		{"http://127.0.0.1:8181", "https://cdn.example.com/cover.png", true, false},
		{"https://api.example.com", "https://cdn.example.com:443/cover.png", true, false},
		{"https://api.example.com", "https://cdn.example.com:444/cover.png", false, false},
		{"https://api.example.com", "http://cdn.example.com/cover.png", false, false},
		{"https://api.example.com", "https://127.0.0.1/cover.png", false, false},
		{"https://api.example.com", "https://10.1.2.3/cover.png", false, false},
		{"https://api.example.com", "https://[::1]/cover.png", false, false},
		{"https://api.example.com", "https://[::ffff:192.168.1.1]/cover.png", false, false},
		{"https://api.example.com", "https://user:pass@cdn.example.com/cover.png", false, false},
		{"https://api.example.com", "https://cdn.example.com/cover.png#fragment", false, false},
		{"http://localhost:443", "https://localhost/cover.png", true, false},
	} {
		_, sameOrigin, err := resolveMarketCoverURL(c.base, c.raw)
		if (err == nil) != c.allowed || err == nil && sameOrigin != c.sessionOrigin {
			t.Errorf("base=%q raw=%q sameOrigin=%v err=%v", c.base, c.raw, sameOrigin, err)
		}
	}
}

func TestMarketplaceCoverDialRejectsNonPublicResolvedIPs(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "100.64.1.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "240.0.0.1",
		"::", "::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1", "2002:a00:1::1",
	} {
		t.Run(address, func(t *testing.T) {
			var dials int
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(address)}, nil
			}
			dial := func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("must not dial")
			}
			_, err := marketCoverDialContext(false, lookup, dial)(context.Background(), "tcp", "cdn.example.com:443")
			if err == nil || dials != 0 {
				t.Fatalf("non-public DNS answer reached dialer: dials=%d err=%v", dials, err)
			}
		})
	}
}

func TestMarketplaceCoverDialPinsCheckedIPAndRechecksDNS(t *testing.T) {
	var lookups int
	var dialed []string
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.168.1.2")}, nil
	}
	dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}
	connect := marketCoverDialContext(false, lookup, dial)
	conn, err := connect(context.Background(), "tcp", "cdn.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := connect(context.Background(), "tcp", "cdn.example.com:443"); err == nil {
		t.Fatal("DNS rebinding was accepted")
	}
	if lookups != 2 || !reflect.DeepEqual(dialed, []string{"[2606:4700:4700::1111]:443"}) {
		t.Fatalf("lookups=%d dialed=%v", lookups, dialed)
	}
}

func TestMarketplaceFetchPublicCoverAnonymousAndNoRedirect(t *testing.T) {
	var privateRequests, publicRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ba-user-token") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("cover request exposed credentials")
		}
		switch r.URL.Path {
		case "/cover.png":
			publicRequests.Add(1)
			_, _ = io.WriteString(w, "public cover")
		case "/redirect":
			http.Redirect(w, r, "https://127.0.0.1/private", http.StatusFound)
		default:
			privateRequests.Add(1)
		}
	}))
	defer server.Close()
	previous := marketCoverClientForURL
	marketCoverClientForURL = func(sessionOrigin bool) *http.Client {
		lookup := func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Errorf("dial did not use checked IP: %q", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "https://"))
		}
		client := newMarketCoverHTTPClient(sessionOrigin, lookup, dial)
		client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		return client
	}
	t.Cleanup(func() { marketCoverClientForURL = previous })
	developerTestSession(t, "https://api.example.invalid")
	api := MarketPublishAPI()
	if _, _, err := api.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The httptest certificate includes example.com; the injected dialer reaches only the fake cloud.
	raw, err := api.FetchCover(context.Background(), "https://example.com/cover.png")
	if err != nil || string(raw) != "public cover" {
		t.Fatalf("cover=%q err=%v", raw, err)
	}
	if _, err := api.FetchCover(context.Background(), "https://example.com/redirect"); err == nil {
		t.Fatal("followed redirect")
	}
	if publicRequests.Load() != 1 || privateRequests.Load() != 0 {
		t.Fatalf("public=%d private=%d", publicRequests.Load(), privateRequests.Load())
	}
}

func TestMarketplaceCoverRejectsSpecialRelayBeforeDial(t *testing.T) {
	if _, _, err := resolveMarketCoverURL("https://api.example.invalid", "https://192.88.99.2/cover.png"); err == nil {
		t.Error("URL gate accepted the non-global 6a44 relay")
	}
	for _, host := range []string{"192.88.99.2", "cdn.example.invalid"} {
		t.Run(host, func(t *testing.T) {
			var dials int
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("192.88.99.2")}, nil
			}
			dial := func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("offline dialer must not be called")
			}
			// Exercise the actual dial guard independently, even when the URL gate already refuses.
			_, err := marketCoverDialContext(false, lookup, dial)(context.Background(), "tcp", net.JoinHostPort(host, "443"))
			if err == nil || dials != 0 {
				t.Fatalf("non-global relay reached dialer: dials=%d err=%v", dials, err)
			}
		})
	}
}

func TestMarketplaceCoverAddressPolicyBoundaries(t *testing.T) {
	// Literal expectations, independent of the production prefix table: both ends of every
	// denied prefix must be refused before either direct or mixed-DNS dialing can occur.
	for _, c := range []struct{ first, last string }{
		{"0.0.0.0", "0.255.255.255"},
		{"10.0.0.0", "10.255.255.255"},
		{"100.64.0.0", "100.127.255.255"},
		{"127.0.0.0", "127.255.255.255"},
		{"169.254.0.0", "169.254.255.255"},
		{"172.16.0.0", "172.31.255.255"},
		{"192.0.0.0", "192.0.0.255"},
		{"192.0.2.0", "192.0.2.255"},
		{"192.88.99.0", "192.88.99.255"},
		{"192.168.0.0", "192.168.255.255"},
		{"198.18.0.0", "198.19.255.255"},
		{"198.51.100.0", "198.51.100.255"},
		{"203.0.113.0", "203.0.113.255"},
		{"240.0.0.0", "255.255.255.255"},
		{"2001::", "2001:1ff:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"2001:db8::", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"2002::", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"3fff::", "3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff"},
	} {
		for _, address := range []string{c.first, c.last} {
			t.Run(address, func(t *testing.T) {
				ip := netip.MustParseAddr(address)
				if marketCoverPublicIP(ip) {
					t.Error("denied-range boundary classified as public")
				}
				var dials int
				lookup := func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8"), ip}, nil
				}
				dial := func(context.Context, string, string) (net.Conn, error) {
					dials++
					return nil, errors.New("offline dialer must not be called")
				}
				connect := marketCoverDialContext(false, lookup, dial)
				for _, host := range []string{address, "cdn.example.invalid"} {
					if _, err := connect(context.Background(), "tcp", net.JoinHostPort(host, "443")); err == nil {
						t.Errorf("denied-range boundary accepted for host %q", host)
					}
				}
				if dials != 0 {
					t.Fatalf("denied-range boundary reached dialer: %d calls", dials)
				}
			})
		}
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "192.88.98.255", "192.88.100.0", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		t.Run("public/"+address, func(t *testing.T) {
			ip := netip.MustParseAddr(address)
			if !marketCoverPublicIP(ip) {
				t.Fatal("public address incorrectly denied")
			}
			var dialed []string
			lookup := func(context.Context, string, string) ([]netip.Addr, error) { return []netip.Addr{ip}, nil }
			dial := func(_ context.Context, _ string, target string) (net.Conn, error) {
				dialed = append(dialed, target)
				client, server := net.Pipe()
				server.Close()
				return client, nil
			}
			connect := marketCoverDialContext(false, lookup, dial)
			for _, host := range []string{address, "cdn.example.invalid"} {
				conn, err := connect(context.Background(), "tcp", net.JoinHostPort(host, "443"))
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
			}
			want := net.JoinHostPort(address, "443")
			if !reflect.DeepEqual(dialed, []string{want, want}) {
				t.Fatalf("dialed=%v want two checked numeric IPs %q", dialed, want)
			}
		})
	}
}

func TestMarketplacePublishAPIFactoryKeepsOperationsIndependent(t *testing.T) {
	developerTestSession(t, "https://example.invalid")
	first := MarketPublishAPI()
	if _, signedIn, err := first.Account(context.Background()); err != nil || !signedIn {
		t.Fatal(err)
	}
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	session.AccountID++
	session.Email = "second@example.com"
	if err = writeSession(session); err != nil {
		t.Fatal(err)
	}
	second := MarketPublishAPI()
	if email, signedIn, err := second.Account(context.Background()); err != nil || !signedIn || email != "second@example.com" {
		t.Fatalf("fresh operation: %s %v %v", email, signedIn, err)
	}
	if _, _, err = first.Account(context.Background()); err == nil {
		t.Fatal("first operation rebound to changed account")
	}
}
