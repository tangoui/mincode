package plugin

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/marketpublish"
)

const (
	pluginDevPublishPrepareTTL             = 5 * time.Minute
	pluginDevPublishConfirmTTL             = 2 * time.Minute
	pluginDevPublishLoginTTL               = 5 * time.Minute
	pluginDevPublishRetention              = 10 * time.Minute
	pluginDevPublishEvent                  = "plugin:development-publish"
	pluginDevPublishMaxPackageBytes        = 64 << 20
	pluginDevPublishMaxCoverBytes          = 2 << 20
	pluginDevPublishMaxSnapshotBytes int64 = 1 << 30
	pluginDevPublishMaxRecords             = 8
	pluginDevPublishMaxActive              = 4
	pluginDevPublishMaxStages              = 2
)

var pluginDevNow = time.Now
var devPublisherSource struct {
	sync.RWMutex
	factory func() marketpublish.Publisher
}

// Each operation owns a fresh publisher/API: the API binds its member session at Plan.
func SetDevPublisherFactory(factory func() marketpublish.Publisher) {
	devPublisherSource.Lock()
	devPublisherSource.factory = factory
	devPublisherSource.Unlock()
}

type pluginDevPublishPackageInput struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type pluginDevPublishRequest struct {
	ProtocolVersion int                            `json:"protocolVersion"`
	CLIInstanceID   string                         `json:"cliInstanceId"`
	ProjectRoot     string                         `json:"projectRoot"`
	ReleaseDir      string                         `json:"releaseDir"`
	PluginID        string                         `json:"pluginId"`
	Name            string                         `json:"name"`
	Version         string                         `json:"version"`
	Description     string                         `json:"description"`
	Icon            string                         `json:"icon,omitempty"`
	Homepage        string                         `json:"homepage,omitempty"`
	Category        string                         `json:"category"`
	Tags            []string                       `json:"tags,omitempty"`
	Readme          string                         `json:"readme,omitempty"`
	ReleaseNotes    string                         `json:"releaseNotes,omitempty"`
	CoverPath       string                         `json:"coverPath,omitempty"`
	ManifestAPI     int                            `json:"manifestApi"`
	Packages        []pluginDevPublishPackageInput `json:"packages"`
	Submit          bool                           `json:"submit"`
}
type pluginDevPublishPackageStatus struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	State   string `json:"state"`
	Percent int    `json:"percent"`
}
type pluginDevPublishChange struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}
type pluginDevPublishTarget struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}
type pluginDevPublishResult struct {
	ReleaseID    int64                    `json:"releaseId"`
	ReviewStatus string                   `json:"reviewStatus"`
	DeveloperURL string                   `json:"developerUrl"`
	Uploaded     []pluginDevPublishTarget `json:"uploaded"`
	Skipped      []pluginDevPublishTarget `json:"skipped"`
}
type pluginDevPublishStatus struct {
	ID              string                          `json:"id"`
	Status          string                          `json:"status"`
	Step            string                          `json:"step"`
	PluginID        string                          `json:"pluginId"`
	PluginName      string                          `json:"pluginName"`
	Version         string                          `json:"version"`
	ReleaseDir      string                          `json:"releaseDir"`
	Account         string                          `json:"account"`
	NewPlugin       bool                            `json:"newPlugin"`
	ExistingDraft   bool                            `json:"existingDraft"`
	Submit          bool                            `json:"submit"`
	ReleaseNotes    string                          `json:"releaseNotes"`
	Packages        []pluginDevPublishPackageStatus `json:"packages"`
	MetadataChanges []pluginDevPublishChange        `json:"metadataChanges"`
	Result          *pluginDevPublishResult         `json:"result,omitempty"`
	ErrorCode       string                          `json:"errorCode,omitempty"`
	ErrorMessage    string                          `json:"errorMessage,omitempty"`
	ExpiresAt       time.Time                       `json:"expiresAt"`
}
type pluginDevPublishOperation struct {
	status      pluginDevPublishStatus
	input       pluginDevPublishRequest
	request     marketpublish.Request
	preview     marketpublish.Preview
	publisher   marketpublish.Publisher
	ctx         context.Context
	cancel      context.CancelFunc
	timer       *time.Timer
	dirs        []string // Only MkdirTemp-owned directories, never user supplied paths.
	coverHash   string
	roots       *pluginDevPublishRoots
	reserved    int64
	initialized bool
	busy        bool
	lastEvent   time.Time
}

func clonePublishRequest(r marketpublish.Request) marketpublish.Request {
	r.Packages = append([]marketpublish.Package(nil), r.Packages...)
	r.Product.Tags = append([]string(nil), r.Product.Tags...)
	return r
}
func clonePublishPreview(v marketpublish.Preview) marketpublish.Preview {
	data, _ := json.Marshal(v)
	var out marketpublish.Preview
	_ = json.Unmarshal(data, &out)
	return out
}
func (op *pluginDevPublishOperation) publicCopy() pluginDevPublishStatus {
	data, _ := json.Marshal(op.status)
	var out pluginDevPublishStatus
	_ = json.Unmarshal(data, &out)
	return out
}
func publishError(code string) error {
	return &marketpublish.APIError{Reason: code, Message: publishMessage(code)}
}
func publishMessage(code string) string {
	switch code {
	case "busy":
		return "文件暂存繁忙，请稍后重试"
	case "resource_limit":
		return "上架请求或快照资源已达上限，请稍后重试"
	case "invalid_request":
		return "上架请求无效，请检查插件信息"
	case "path_outside":
		return "安装包或封面路径超出项目或发布目录"
	case "package_invalid":
		return "安装包或封面无效，请重新构建"
	case "package_changed":
		return "安装包或封面已变化，请重新运行 publish"
	case "not_signed_in":
		return "请先在 MinCode 中登录云账号"
	case "account_changed":
		return "云账号已切换，请重新运行 publish"
	case "confirmation_changed":
		return "云端插件信息或封面已变化，请重新运行 publish 并确认"
	case "preparation_timeout":
		return "上架准备已超时，请重新运行 publish"
	case "confirmation_timeout":
		return "确认已超时，请重新运行 publish"
	case "login_timeout":
		return "登录等待已超时，请重新运行 publish"
	case "rejected":
		return "用户已拒绝上架"
	case "canceled":
		return "上架已取消"
	case "product_owned_by_other":
		return "该插件 ID 已被其他开发者使用，请换一个 ID"
	case "version_submitted":
		return "该版本已提交审核，请提高版本号"
	case "version_published":
		return "该版本已发布，请提高版本号"
	case "version_retired":
		return "该版本号已作废，请提高版本号"
	case "product_unavailable":
		return "插件在云端不可用"
	case "artifacts_missing":
		return "还有平台的安装包没有上传，请检查支持的平台"
	case "cover_invalid":
		return "封面图片不符合格式、尺寸或大小要求"
	case "developer_suspended":
		return "你的开发者权限已被暂停"
	case "release_not_found":
		return "云端找不到这个版本草稿"
	case "artifact_conflict":
		return "云端已有不同哈希的安装包，请提高版本号"
	default:
		return "上架失败，请检查云端状态后重试"
	}
}
func publishErrorCode(err error) string {
	var api *marketpublish.APIError
	if errors.As(err, &api) { // Never reflect arbitrary remote error messages or credentials.
		switch api.Reason {
		case "busy", "resource_limit", "invalid_request", "path_outside", "package_invalid", "package_changed", "not_signed_in", "account_changed", "confirmation_changed", "confirmation_timeout", "preparation_timeout", "login_timeout", "rejected", "canceled", "product_owned_by_other", "version_submitted", "version_published", "version_retired", "product_unavailable", "artifact_conflict", "artifacts_missing", "cover_invalid", "developer_suspended", "missing_targets", "invalid_artifact", "release_not_found", "unauthorized", "forbidden":
			return api.Reason
		}
	}
	return "publish_failed"
}

// Open relative to an OS-enforced root, without following a final symlink or blocking on a substituted FIFO.
func openPublishFileAt(tree *os.Root, root, path string, max int64) (*os.File, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, publishError("path_outside")
	}
	path = filepath.Join(parent, filepath.Base(path))

	if !pathWithinRoot(root, path) {
		return nil, publishError("path_outside")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, publishError("path_outside")
	}
	info, err := tree.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return nil, publishError("package_invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, publishError("package_invalid")
	}
	if !pathWithinRoot(root, resolved) {
		return nil, publishError("path_outside")
	}
	f, err := tree.OpenFile(rel, publishReadFlags(), 0)
	if err != nil {
		return nil, publishError("package_invalid")
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		f.Close()
		return nil, publishError("package_invalid")
	}
	return f, nil
}
func inspectPublishFile(root, path string, max int64, dest string) (string, int64, error) {
	canonical, err := canonicalPluginDevRoot(root)
	if err != nil {
		return "", 0, publishError("package_invalid")
	}
	tree, err := os.OpenRoot(canonical)
	if err != nil {
		return "", 0, publishError("package_invalid")
	}
	defer tree.Close()
	return inspectPublishFileAt(tree, canonical, path, max, dest)
}
func inspectPublishFileAt(tree *os.Root, root, path string, max int64, dest string, expectedSize ...int64) (string, int64, error) {
	f, err := openPublishFileAt(tree, root, path, max)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", 0, publishError("package_invalid")
	}
	if len(expectedSize) > 0 && before.Size() != expectedSize[0] {
		return "", 0, publishError("package_invalid")
	}
	hash := sha256.New()
	var writer io.Writer = hash
	if dest != "" {
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return "", 0, publishError("package_invalid")
		}
		defer out.Close()
		writer = io.MultiWriter(hash, out)
	}
	n, err := io.Copy(writer, io.LimitReader(f, before.Size()))
	after, e := f.Stat()
	if err != nil || e != nil || n != before.Size() || n > max || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", 0, publishError("package_invalid")
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}
func validatePublishZIP(tree *os.Root, root, path, id, version string) error {
	f, err := openPublishFileAt(tree, root, path, pluginDevPublishMaxPackageBytes)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return publishError("package_invalid")
	}
	z, err := zip.NewReader(f, info.Size())
	if err != nil || validatePluginZIPEntries(z.File) != nil {
		return publishError("package_invalid")
	}
	for _, entry := range z.File {
		if entry.Name != "plugin.json" {
			continue
		}
		r, e := entry.Open()
		if e != nil {
			return publishError("package_invalid")
		}
		data, e := io.ReadAll(io.LimitReader(r, pluginDevRequestMaxBytes+1))
		r.Close()
		var manifest struct {
			ID         string `json:"id"`
			Version    string `json:"version"`
			APIVersion int    `json:"apiVersion"`
		}
		if e != nil || len(data) > pluginDevRequestMaxBytes || json.Unmarshal(data, &manifest) != nil || manifest.ID != id || manifest.Version != version || manifest.APIVersion != 2 {
			return publishError("package_invalid")
		}
		return nil
	}
	return publishError("package_invalid")
}
func validatePluginDevPublish(input pluginDevPublishRequest) (marketpublish.Request, error) {
	return preparePublishRequest(input, "")
}

// A destination stages newly created, distinct inodes while recomputing hashes and validating ZIPs.
func preparePublishRequest(in pluginDevPublishRequest, dest string) (marketpublish.Request, error) {
	roots, normalized, err := pinPublishRoots(in)
	if err != nil {
		return marketpublish.Request{}, err
	}
	defer roots.close()
	return preparePublishRequestPinned(normalized, dest, roots)
}
func preparePublishRequestPinned(in pluginDevPublishRequest, dest string, roots *pluginDevPublishRoots) (marketpublish.Request, error) {
	if dest != "" {
		canonical, err := canonicalPluginDevRoot(dest)
		if err != nil {
			return marketpublish.Request{}, publishError("package_invalid")
		}
		dest = canonical
	}
	bad := func() (marketpublish.Request, error) { return marketpublish.Request{}, publishError("invalid_request") }
	if admitPublishInput(in) != nil {
		return bad()
	}
	if err := roots.verify(); err != nil {
		return marketpublish.Request{}, err
	}
	project, release := roots.project, roots.release
	projectTree, releaseTree := roots.projectTree, roots.releaseTree
	var err error

	zipTree := releaseTree
	if dest != "" {
		zipTree, err = os.OpenRoot(dest)
		if err != nil {
			return bad()
		}
		defer zipTree.Close()
	}
	req := marketpublish.Request{PluginID: in.PluginID, Version: in.Version, ReleaseNotes: in.ReleaseNotes, ManifestAPI: in.ManifestAPI, Submit: in.Submit, Product: marketpublish.Product{Name: in.Name, Description: in.Description, Icon: in.Icon, Homepage: in.Homepage, Readme: in.Readme, Category: in.Category, Tags: append([]string(nil), in.Tags...)}}
	targets := []marketpublish.Target{{OS: "darwin", Arch: "arm64"}, {OS: "darwin", Arch: "amd64"}, {OS: "windows", Arch: "amd64"}, {OS: "linux", Arch: "amd64"}}
	seen := map[marketpublish.Target]bool{}
	for _, pkg := range in.Packages {
		target := marketpublish.Target{OS: pkg.OS, Arch: pkg.Arch}
		valid := false
		for _, t := range targets {
			valid = valid || t == target
		}
		if !valid || seen[target] {
			return bad()
		}
		seen[target] = true
		path := pkg.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(release, path)
		}
		next := ""
		if dest != "" {
			dir := filepath.Join(dest, pkg.OS+"-"+pkg.Arch)
			if err = os.Mkdir(dir, 0700); err != nil {
				return bad()
			}
			next = filepath.Join(dir, filepath.Base(path))
		}
		hash, size, e := inspectPublishFileAt(releaseTree, release, path, pluginDevPublishMaxPackageBytes, next, pkg.Size)
		if e != nil {
			return marketpublish.Request{}, e
		}
		if size != pkg.Size || !strings.EqualFold(hash, pkg.SHA256) {
			return marketpublish.Request{}, publishError("package_invalid")
		}
		zipRoot, zipPath := release, path
		if next != "" {
			zipRoot = dest
			zipPath = next
		}
		if err = validatePublishZIP(zipTree, zipRoot, zipPath, in.PluginID, in.Version); err != nil {
			return marketpublish.Request{}, err
		}
		if next != "" {
			path = next
		}
		req.Packages = append(req.Packages, marketpublish.Package{Target: target, Path: path, Size: size, SHA256: hash})
	}
	sort.Slice(req.Packages, func(i, j int) bool {
		index := func(t marketpublish.Target) int {
			for k, x := range targets {
				if t == x {
					return k
				}
			}
			return 4
		}
		return index(req.Packages[i].Target) < index(req.Packages[j].Target)
	})
	if in.CoverPath != "" {
		path := in.CoverPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(project, path)
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		default:
			return marketpublish.Request{}, publishError("package_invalid")
		}
		next := ""
		if dest != "" {
			dir := filepath.Join(dest, "cover")
			if err = os.Mkdir(dir, 0700); err != nil {
				return bad()
			}
			next = filepath.Join(dir, filepath.Base(path))
		}
		if _, _, err = inspectPublishFileAt(projectTree, project, path, pluginDevPublishMaxCoverBytes, next); err != nil {
			return marketpublish.Request{}, err
		}
		if next != "" {
			path = next
		}
		req.Product.CoverPath = path
	}
	return req, nil
}
func stagePublishPinned(in pluginDevPublishRequest, roots *pluginDevPublishRoots) (marketpublish.Request, string, string, error) {
	// Check the original directory identities before creating a snapshot or opening any file.
	if err := roots.verify(); err != nil {
		return marketpublish.Request{}, "", "", err
	}
	dir, err := os.MkdirTemp("", "mincode-publish-")
	if err != nil {
		return marketpublish.Request{}, "", "", publishError("package_invalid")
	}
	req, err := preparePublishRequestPinned(in, dir, roots)
	if err != nil {
		os.RemoveAll(dir)
		return req, "", "", err
	}
	hash := ""
	if req.Product.CoverPath != "" {
		hash, _, err = inspectPublishFile(dir, req.Product.CoverPath, pluginDevPublishMaxCoverBytes, "")
	}
	if err != nil {
		os.RemoveAll(dir)
		return req, "", "", err
	}
	return req, dir, hash, nil
}

func publishTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "rejected", "expired", "canceled":
		return true
	}
	return false
}
func (b *pluginDevBridge) emitPublishLocked(op *pluginDevPublishOperation) {
	if !b.closed {
		core.Emit(pluginDevPublishEvent, op.publicCopy())
	}
}
func (b *pluginDevBridge) publishDeadlineLocked(op *pluginDevPublishOperation, ttl time.Duration) {
	if op.timer != nil {
		op.timer.Stop()
	}
	op.status.ExpiresAt = pluginDevNow().Add(ttl)
	op.timer = time.AfterFunc(ttl, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.closed {
			b.prunePublishesLocked(pluginDevNow())
		}
	})
}
func (b *pluginDevBridge) finishPublishLocked(op *pluginDevPublishOperation, status, code string) {
	if publishTerminal(op.status.Status) {
		return
	}
	op.status.Status = status
	op.status.ErrorCode = code
	if code != "" {
		op.status.ErrorMessage = publishMessage(code)
	}
	op.cancel()
	if !op.busy {
		b.releasePublishResourcesLocked(op)
	}
	b.publishDeadlineLocked(op, pluginDevPublishRetention)
	b.emitPublishLocked(op)
}
func (b *pluginDevBridge) prunePublishesLocked(now time.Time) {
	for id, op := range b.publishes {
		if now.Before(op.status.ExpiresAt) {
			continue
		}
		if publishTerminal(op.status.Status) {
			if !op.busy {
				if op.timer != nil {
					op.timer.Stop()
				}
				b.releasePublishResourcesLocked(op)
				delete(b.publishes, id)
			}
		} else if op.status.Status == "preparing" || op.status.Status == "pending_confirmation" || op.status.Status == "awaiting_login" {
			code := "confirmation_timeout"
			if op.status.Status == "preparing" {
				code = "preparation_timeout"
			}
			if op.status.Status == "awaiting_login" {
				code = "login_timeout"
			}
			b.finishPublishLocked(op, "expired", code)
		}
	}
}
func (b *pluginDevBridge) Publishes() []pluginDevPublishStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prunePublishesLocked(pluginDevNow())
	out := []pluginDevPublishStatus{}
	for _, op := range b.publishes {
		if op.initialized {
			out = append(out, op.publicCopy())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (b *pluginDevBridge) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !requireLoopback(r) || !b.authenticateInstance(r) {
		writePluginDevJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in pluginDevPublishRequest
	if decodePluginDevRequest(r, &in) != nil {
		writePluginDevJSON(w, 400, map[string]string{"errorCode": "invalid_request", "error": publishMessage("invalid_request")})
		return
	}
	if err := admitPublishInput(in); err != nil {
		writePluginDevJSON(w, 422, map[string]string{"errorCode": "invalid_request", "error": publishMessage("invalid_request")})
		return
	}
	reservation, err := publishSnapshotReservation(in)
	if err != nil {
		writePluginDevJSON(w, 422, map[string]string{"errorCode": publishErrorCode(err), "error": publishMessage(publishErrorCode(err))})
		return
	}
	devPublisherSource.RLock()
	factory := devPublisherSource.factory
	devPublisherSource.RUnlock()
	if factory == nil {
		writePluginDevJSON(w, 503, map[string]string{"error": "publisher unavailable"})
		return
	}
	b.mu.Lock()
	b.prunePublishesLocked(pluginDevNow())
	if b.closed {
		b.mu.Unlock()
		writePluginDevJSON(w, 503, map[string]string{"error": "bridge closed"})
		return
	}
	active := 0
	for _, op := range b.publishes {
		if !publishTerminal(op.status.Status) || op.busy {
			active++
			if op.status.PluginID == in.PluginID && op.status.Version == in.Version {
				b.mu.Unlock()
				writePluginDevJSON(w, 409, map[string]string{"error": "publish in progress"})
				return
			}
		}
	}
	code := ""
	if len(b.publishes) >= pluginDevPublishMaxRecords || active >= pluginDevPublishMaxActive || reservation > pluginDevPublishMaxSnapshotBytes-b.publishReserved {
		code = "resource_limit"
	} else if b.publishStages >= pluginDevPublishMaxStages {
		code = "busy"
	}
	if code != "" {
		b.mu.Unlock()
		writePluginDevJSON(w, 429, map[string]string{"errorCode": code, "error": code, "errorMessage": publishMessage(code)})
		return
	}
	id, err := randomHex(16)
	if err != nil {
		b.mu.Unlock()
		writePluginDevJSON(w, 500, map[string]string{"error": "publish unavailable"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &pluginDevPublishOperation{input: in, ctx: ctx, cancel: cancel, initialized: true, busy: true, reserved: reservation, status: pluginDevPublishStatus{ID: id, Status: "preparing", Step: "lookup", PluginID: in.PluginID, PluginName: in.Name, Version: in.Version, ReleaseDir: in.ReleaseDir, Submit: in.Submit, ReleaseNotes: in.ReleaseNotes, Packages: []pluginDevPublishPackageStatus{}, MetadataChanges: []pluginDevPublishChange{}}}
	b.publishes[id] = op
	b.publishReserved += reservation
	b.publishStages++
	b.publishWorkers.Add(1)
	b.publishDeadlineLocked(op, pluginDevPublishPrepareTTL)
	expires := op.status.ExpiresAt
	b.emitPublishLocked(op)
	b.mu.Unlock()
	// No filesystem I/O or remote Plan is on the admission response path.
	go b.preparePublish(op, factory)
	writePluginDevJSON(w, 202, map[string]any{"publishId": id, "expiresAt": expires})
}
func (b *pluginDevBridge) preparePublish(op *pluginDevPublishOperation, factory func() marketpublish.Publisher) {
	defer b.publishWorkers.Done()
	in := op.input
	var roots *pluginDevPublishRoots
	normalized := in
	var stageErr error
	if op.ctx.Err() == nil {
		roots, normalized, stageErr = pinPublishRoots(in)
	} else {
		stageErr = op.ctx.Err()
	}
	var req marketpublish.Request
	var dir, coverHash string
	if stageErr == nil && op.ctx.Err() != nil {
		stageErr = op.ctx.Err()
	}
	if stageErr == nil {
		req, dir, coverHash, stageErr = stagePublishPinned(normalized, roots)
	}
	b.mu.Lock()
	b.publishStages--
	op.roots = roots
	if dir != "" {
		op.dirs = append(op.dirs, dir)
	}
	if stageErr != nil || b.closed || publishTerminal(op.status.Status) {
		op.busy = false
		if !b.closed && !publishTerminal(op.status.Status) {
			b.finishPublishLocked(op, "failed", publishErrorCode(stageErr))
		}
		b.releasePublishResourcesLocked(op)
		b.mu.Unlock()
		return
	}
	op.input = normalized
	op.request = req
	op.coverHash = coverHash
	for _, pkg := range req.Packages {
		op.status.Packages = append(op.status.Packages, pluginDevPublishPackageStatus{OS: pkg.Target.OS, Arch: pkg.Target.Arch, File: filepath.Base(pkg.Path), Size: pkg.Size, SHA256: pkg.SHA256, State: "pending"})
	}
	b.mu.Unlock()
	publisher := factory()
	b.mu.Lock()
	op.publisher = publisher
	if b.closed || op.ctx.Err() != nil || publisher == nil {
		op.busy = false
		if !b.closed && !publishTerminal(op.status.Status) {
			b.finishPublishLocked(op, "failed", "publish_failed")
		}
		b.releasePublishResourcesLocked(op)
		b.mu.Unlock()
		return
	}
	b.publishWorkers.Add(1)
	b.mu.Unlock()
	b.planPublish(op, false)
}

func (b *pluginDevBridge) planPublish(op *pluginDevPublishOperation, refresh bool) {
	defer b.publishWorkers.Done()
	preview, err := op.publisher.Plan(op.ctx, clonePublishRequest(op.request))
	b.mu.Lock()
	defer b.mu.Unlock()
	op.busy = false
	op.initialized = true
	if b.closed {
		return
	}
	if publishTerminal(op.status.Status) {
		b.releasePublishResourcesLocked(op)
		b.prunePublishesLocked(pluginDevNow())
		return
	}
	if err != nil {
		if publishErrorCode(err) == "not_signed_in" {
			op.status.Status = "awaiting_login"
			op.status.ErrorCode = "not_signed_in"
			op.status.ErrorMessage = publishMessage("not_signed_in")
			if !refresh {
				b.publishDeadlineLocked(op, pluginDevPublishLoginTTL)
			}
			b.emitPublishLocked(op)
			return
		}
		b.finishPublishLocked(op, "failed", publishErrorCode(err))
		return
	}
	op.preview = clonePublishPreview(preview)
	op.status.Status = "pending_confirmation"
	op.status.Account = preview.Account
	op.status.NewPlugin = preview.NewPlugin
	op.status.ExistingDraft = preview.ExistingDraft
	op.status.ErrorCode = ""
	op.status.ErrorMessage = ""
	op.status.MetadataChanges = []pluginDevPublishChange{}
	for _, change := range op.preview.MetadataChanges {
		op.status.MetadataChanges = append(op.status.MetadataChanges, pluginDevPublishChange{Field: change.Field, Old: change.Old, New: change.New})
	}
	b.publishDeadlineLocked(op, pluginDevPublishConfirmTTL)
	b.emitPublishLocked(op)
}
func (b *pluginDevBridge) RefreshPublish(id string) error {
	b.mu.Lock()
	b.prunePublishesLocked(pluginDevNow())
	op := b.publishes[id]
	if b.closed || op == nil || op.status.Status != "awaiting_login" || op.busy {
		b.mu.Unlock()
		return errors.New("当前上架请求不可刷新")
	}
	op.busy = true
	b.publishWorkers.Add(1)
	b.mu.Unlock()
	b.planPublish(op, true)
	return nil
}
func (b *pluginDevBridge) ConfirmPublish(id string) error {
	b.mu.Lock()
	b.prunePublishesLocked(pluginDevNow())
	op := b.publishes[id]
	if b.closed || op == nil || op.status.Status != "pending_confirmation" || op.busy {
		b.mu.Unlock()
		return errors.New("当前上架请求不可确认")
	}
	if b.publishStages >= pluginDevPublishMaxStages {
		b.mu.Unlock()
		return errors.New("publish_busy")
	}
	b.publishStages++
	op.busy = true
	b.publishWorkers.Add(1)
	b.mu.Unlock()
	req, dir, coverHash, err := stagePublishPinned(op.input, op.roots)
	b.mu.Lock()
	b.publishStages--
	if dir != "" {
		op.dirs = append(op.dirs, dir)
	}
	if b.closed || publishTerminal(op.status.Status) || !pluginDevNow().Before(op.status.ExpiresAt) {
		op.busy = false
		if !b.closed {
			if !publishTerminal(op.status.Status) {
				b.finishPublishLocked(op, "expired", "confirmation_timeout")
			}
			b.releasePublishResourcesLocked(op)
			b.prunePublishesLocked(pluginDevNow())
		}
		b.mu.Unlock()
		b.publishWorkers.Done()
		return errors.New("上架请求已结束")
	}
	if err != nil || coverHash != op.coverHash {
		op.busy = false
		b.finishPublishLocked(op, "failed", "package_changed")
		b.mu.Unlock()
		b.publishWorkers.Done()
		return publishError("package_changed")
	}
	op.request = clonePublishRequest(req)
	op.status.Status = "running"
	op.status.Step = "lookup"
	if op.timer != nil {
		op.timer.Stop()
		op.timer = nil
	}
	op.status.ExpiresAt = time.Time{}
	b.emitPublishLocked(op)
	b.mu.Unlock()
	go func() {
		defer b.publishWorkers.Done()
		result, runErr := op.publisher.Run(op.ctx, clonePublishRequest(req), clonePublishPreview(op.preview), func(progress marketpublish.Progress) { b.publishProgress(op, progress) })
		b.mu.Lock()
		defer b.mu.Unlock()
		op.busy = false
		if b.closed {
			return
		}
		if publishTerminal(op.status.Status) {
			b.releasePublishResourcesLocked(op)
			b.prunePublishesLocked(pluginDevNow())
			return
		}
		if runErr != nil {
			b.finishPublishLocked(op, "failed", publishErrorCode(runErr))
			return
		}
		op.status.Step = "done"
		targets := func(in []marketpublish.Target) []pluginDevPublishTarget {
			out := []pluginDevPublishTarget{}
			for _, v := range in {
				out = append(out, pluginDevPublishTarget{OS: v.OS, Arch: v.Arch})
			}
			return out
		}
		op.status.Result = &pluginDevPublishResult{ReleaseID: result.ReleaseID, ReviewStatus: result.ReviewStatus, DeveloperURL: result.DeveloperURL, Uploaded: targets(result.Uploaded), Skipped: targets(result.Skipped)}
		for i := range op.status.Packages {
			op.status.Packages[i].State = "uploaded"
			op.status.Packages[i].Percent = 100
			for _, target := range result.Skipped {
				if op.status.Packages[i].OS == target.OS && op.status.Packages[i].Arch == target.Arch {
					op.status.Packages[i].State = "skipped"
				}
			}
		}
		b.finishPublishLocked(op, "succeeded", "")
	}()
	return nil
}
func (b *pluginDevBridge) publishProgress(op *pluginDevPublishOperation, p marketpublish.Progress) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || op.status.Status != "running" {
		return
	}
	step := string(p.Step)
	changed := step != op.status.Step
	op.status.Step = step
	if p.Target != nil {
		for i := range op.status.Packages {
			pkg := &op.status.Packages[i]
			if pkg.OS == p.Target.OS && pkg.Arch == p.Target.Arch {
				percent := 0
				if p.Total > 0 {
					percent = int(100 * float64(p.Sent) / float64(p.Total))
				}
				if percent < 0 {
					percent = 0
				}
				if percent > 100 {
					percent = 100
				}
				pkg.Percent = percent
				pkg.State = "uploading"
				if percent == 100 {
					pkg.State = "uploaded"
				}
			}
		}
	}
	now := pluginDevNow()
	if changed || now.Sub(op.lastEvent) >= 250*time.Millisecond {
		op.lastEvent = now
		b.emitPublishLocked(op)
	}
}
func (b *pluginDevBridge) stopPublish(id, status, code string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.prunePublishesLocked(pluginDevNow())
	op := b.publishes[id]
	if op == nil {
		return errors.New("上架请求不存在")
	}
	if publishTerminal(op.status.Status) {
		return nil
	}
	if status == "rejected" && op.status.Status == "running" {
		return errors.New("上架已开始，请取消")
	}
	b.finishPublishLocked(op, status, code)
	return nil
}
func (b *pluginDevBridge) RejectPublish(id string) error {
	return b.stopPublish(id, "rejected", "rejected")
}
func (b *pluginDevBridge) CancelPublish(id string) error {
	return b.stopPublish(id, "canceled", "canceled")
}
func (b *pluginDevBridge) handlePublishStatus(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(r) || !b.authenticateInstance(r) {
		writePluginDevJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, pluginDevBridgePrefix+"/publish/")
	if strings.HasSuffix(path, "/cancel") && r.Method == http.MethodPost {
		id := strings.TrimSuffix(path, "/cancel")
		if b.CancelPublish(id) != nil {
			writePluginDevJSON(w, 404, map[string]string{"error": "publish not found"})
			return
		}
		writePluginDevJSON(w, 200, map[string]bool{"canceled": true})
		return
	}
	if r.Method == http.MethodGet && !strings.Contains(path, "/") {
		for _, s := range b.Publishes() {
			if s.ID == path {
				writePluginDevJSON(w, 200, publishPollProjection(s))
				return
			}
		}
	}
	writePluginDevJSON(w, 404, map[string]string{"error": "publish not found"})
}

// Close waits outside the mutex so workers can finish without retaining or deleting user files.
func (b *pluginDevBridge) closePublishesLocked() {
	for _, op := range b.publishes {
		op.cancel()
		if op.timer != nil {
			op.timer.Stop()
		}
	}
}
func (b *pluginDevBridge) cleanupPublishes() {
	b.publishWorkers.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, op := range b.publishes {
		b.releasePublishResourcesLocked(op)
	}
	b.publishes = map[string]*pluginDevPublishOperation{}
}

// Reserve both potential copies up front. Size bounds make summation/duplication overflow impossible.
func publishSnapshotReservation(in pluginDevPublishRequest) (int64, error) {
	if len(in.Packages) < 1 || len(in.Packages) > 4 {
		return 0, publishError("invalid_request")
	}
	var bytes int64
	for _, pkg := range in.Packages {
		if pkg.Size <= 0 || pkg.Size > pluginDevPublishMaxPackageBytes {
			return 0, publishError("package_invalid")
		}
		bytes += pkg.Size
	}
	if in.CoverPath != "" {
		bytes += pluginDevPublishMaxCoverBytes
	}
	return bytes * 2, nil
}

// Call only after this operation's worker has stopped using its roots/snapshots.
func (b *pluginDevBridge) releasePublishResourcesLocked(op *pluginDevPublishOperation) {
	if op.busy {
		return
	}
	for _, dir := range op.dirs {
		_ = os.RemoveAll(dir)
	}
	op.dirs = nil
	if op.roots != nil {
		op.roots.close()
		op.roots = nil
	}
	b.publishReserved -= op.reserved
	op.reserved = 0
}

// Bounded, allocation-light admission only; local path/ZIP failures are pollable
// terminal failures after the stable operation ID has been returned.
func admitPublishInput(in pluginDevPublishRequest) error {
	if in.ProtocolVersion != 1 || in.CLIInstanceID == "" || !pluginIDPattern.MatchString(in.PluginID) || strings.HasPrefix(in.PluginID, "mincode.") || !pluginVersionPattern.MatchString(in.Version) || len(in.Version) > 64 || in.ManifestAPI != 2 || len(strings.TrimSpace(in.Name)) == 0 || len(in.Name) > 120 || len(in.Description) > 2000 || len(in.Readme) > 65535 || len(in.ReleaseNotes) > 10000 || len(in.Packages) < 1 || len(in.Packages) > 4 {
		return publishError("invalid_request")
	}
	if len(in.CLIInstanceID) > 128 || len(in.Icon) > 255 || len(in.Homepage) > 2048 || len(in.Category) > 64 || len(in.Tags) > 20 {
		return publishError("invalid_request")
	}
	for _, tag := range in.Tags {
		if len(tag) > 256 {
			return publishError("invalid_request")
		}
	}
	seen := map[string]bool{}
	for _, p := range in.Packages {
		target := p.OS + "/" + p.Arch
		if (target != "darwin/arm64" && target != "darwin/amd64" && target != "windows/amd64" && target != "linux/amd64") || seen[target] || len(p.SHA256) != 64 {
			return publishError("invalid_request")
		}
		if _, err := hex.DecodeString(p.SHA256); err != nil {
			return publishError("invalid_request")
		}
		seen[target] = true
	}
	return nil
}

// HTTP polling deliberately excludes confirmation text (README diffs, account,
// paths and notes). The trusted native UI/events retain the complete status.
// At most four package/target rows, filename <=255 bytes, URL <=2048 bytes and
// fixed local errors keep this projection under 32 KiB even with JSON escaping.
// Neither the 256 KiB request cap nor the shared CLI response cap is increased.
type pluginDevPublishPoll struct {
	ID           string                          `json:"id"`
	Status       string                          `json:"status"`
	Step         string                          `json:"step"`
	Packages     []pluginDevPublishPackageStatus `json:"packages"`
	Result       *pluginDevPublishResult         `json:"result,omitempty"`
	ErrorCode    string                          `json:"errorCode,omitempty"`
	ErrorMessage string                          `json:"errorMessage,omitempty"`
	ExpiresAt    time.Time                       `json:"expiresAt"`
}

func publishPollProjection(s pluginDevPublishStatus) pluginDevPublishPoll {
	out := pluginDevPublishPoll{ID: s.ID, Status: s.Status, Step: s.Step, Packages: []pluginDevPublishPackageStatus{}, ErrorCode: s.ErrorCode, ErrorMessage: s.ErrorMessage, ExpiresAt: s.ExpiresAt}
	switch out.Step {
	case "lookup", "draft", "cover", "metadata", "upload", "submit", "done":
	default:
		out.Step = "lookup"
	}
	for i, p := range s.Packages {
		if i == 4 {
			break
		}
		if len(p.File) > 255 {
			p.File = ""
		}
		out.Packages = append(out.Packages, p)
	}
	if s.Result != nil {
		result := *s.Result
		if len(result.DeveloperURL) > 2048 {
			result.DeveloperURL = ""
		}
		switch result.ReviewStatus {
		case "not_submitted", "pending", "approved", "rejected":
		default:
			result.ReviewStatus = ""
		}
		targets := func(in []pluginDevPublishTarget) []pluginDevPublishTarget {
			out := []pluginDevPublishTarget{}
			seen := map[pluginDevPublishTarget]bool{}
			for _, t := range in {
				key := t.OS + "/" + t.Arch
				if !seen[t] && (key == "darwin/arm64" || key == "darwin/amd64" || key == "windows/amd64" || key == "linux/amd64") {
					out = append(out, t)
					seen[t] = true
				}
			}
			return out
		}
		result.Uploaded = targets(result.Uploaded)
		result.Skipped = targets(result.Skipped)
		out.Result = &result
	}
	return out
}
