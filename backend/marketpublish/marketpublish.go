// Package marketpublish plans and runs a confirmed marketplace publication.
// It knows no member credentials or UI; the host must obtain confirmation between Plan and Run.
package marketpublish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Target struct{ OS, Arch string }
type Package struct {
	Target Target
	Path   string
	Size   int64
	SHA256 string
}
type Product struct {
	Name, Description, Icon, Homepage, Readme, Category string
	Tags                                                []string
	CoverPath                                           string
}
type Request struct {
	PluginID, Version, ReleaseNotes string
	ManifestAPI                     int
	Product                         Product
	Packages                        []Package
	Submit                          bool
}
type FieldChange struct {
	Field    string
	Old, New any
}
type Preview struct {
	Account                  string
	NewPlugin, ExistingDraft bool
	MetadataChanges          []FieldChange
	SkipTargets              []Target
	// Confirmation binds the normalized remote metadata and cover bytes to the read-only plan.
	Confirmation string
}
type Step string

const (
	StepLookup   Step = "lookup"
	StepDraft    Step = "draft"
	StepCover    Step = "cover"
	StepMetadata Step = "metadata"
	StepUpload   Step = "upload"
	StepSubmit   Step = "submit"
	StepDone     Step = "done"
)

type Progress struct {
	Step        Step
	Target      *Target
	Sent, Total int64
}
type Result struct {
	ReleaseID                  int64
	ReviewStatus, DeveloperURL string
	Uploaded, Skipped          []Target
}
type Lookup struct {
	Exists, Owned bool
	// ProductStatus lets Plan reject unavailable plugins before asking for confirmation.
	ProductStatus string
	Product       *RemoteProduct
	Releases      []RemoteRelease
}
type RemoteProduct struct {
	Name, Description, Icon, CoverURL, Homepage, Readme, Category string
	Tags                                                          []string
}
type RemoteRelease struct {
	ID                            int64
	Version, Status, ReviewStatus string
	Targets                       []RemoteTarget
}
type RemoteTarget struct {
	Target Target
	SHA256 string
}
type DraftInput struct {
	Request  Request
	CoverURL string
}
type APIError struct {
	Reason, Message string
	Missing         []string
}

func (e *APIError) Error() string { return e.Message }

type API interface {
	Account(context.Context) (email string, signedIn bool, err error)
	Lookup(context.Context, string) (Lookup, error)
	CreateDraft(context.Context, DraftInput) (releaseID int64, reused bool, err error)
	UpdateDraft(context.Context, int64, DraftInput) error
	FetchCover(context.Context, string) ([]byte, error)
	UploadCover(context.Context, string, func(sent, total int64)) (string, error)
	UploadArtifact(context.Context, int64, Target, string, string, func(sent, total int64)) error
	Submit(context.Context, int64) error
	DeveloperURL(context.Context) string
}
type Publisher interface {
	Plan(context.Context, Request) (Preview, error)
	Run(context.Context, Request, Preview, func(Progress)) (Result, error)
}
type publisher struct{ api API }

func New(api API) Publisher { return &publisher{api: api} }

func refusal(reason string) error {
	messages := map[string]string{
		"not_signed_in":          "请先在 MinCode 中登录云账号",
		"account_changed":        "云账号已切换，请重新运行 publish",
		"product_owned_by_other": "该插件 ID 已被其他开发者使用，请换一个 ID",
		"version_submitted":      "该版本已提交审核，请提高版本号",
		"version_published":      "该版本已发布，请提高版本号",
		"version_retired":        "该版本号已作废（审核被拒或已删除），请提高版本号后重新提交",
		"product_unavailable":    "插件在云端不可用",
	}
	return &APIError{Reason: reason, Message: messages[reason]}
}

// Publishing preserves the host/cloud identity contract, including existing IDs.
// The stricter create/verify naming rule must not prevent those plugins from publishing updates.
var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
var digestPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var marketTargets = []Target{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}}

// Normalize the same text/catalog values as the cloud so the confirmation diff matches the write.
// Local size validation is deliberately before even Account/Lookup, and is repeated by Run.
func normalizeRequest(req Request) (Request, error) {
	invalid := func(message string) (Request, error) {
		return Request{}, &APIError{Reason: "invalid_request", Message: message}
	}
	for _, limit := range []struct {
		name, value string
		max         int
	}{
		{"发布说明", req.ReleaseNotes, 10000}, {"README ", req.Product.Readme, 65535},
		{"插件名称", req.Product.Name, 120}, {"插件简介", req.Product.Description, 2000},
	} {
		if len(limit.value) > limit.max {
			return invalid(fmt.Sprintf("%s有 %d 字节，超过云端上限 %d 字节", limit.name, len(limit.value), limit.max))
		}
		if !utf8.ValidString(limit.value) || strings.ContainsFunc(limit.value, func(r rune) bool { return r < 32 && r != '\t' && r != '\n' && r != '\r' || r == 127 }) {
			return invalid(limit.name + "必须是有效 UTF-8 文本，且不能包含控制字符")
		}
	}
	if len(req.PluginID) < 2 || len(req.PluginID) > 128 || strings.HasPrefix(req.PluginID, "mincode.") || !pluginIDPattern.MatchString(req.PluginID) {
		return invalid("插件 ID 格式无效，且不能以 mincode. 开头")
	}
	if len(req.Version) > 64 || !versionPattern.MatchString(req.Version) {
		return invalid("插件版本号格式无效")
	}
	if req.ManifestAPI != 2 {
		return invalid("插件 Manifest API 必须是 2")
	}
	req.Product.Name = strings.TrimSpace(req.Product.Name)
	if req.Product.Name == "" {
		return invalid("插件名称不能为空")
	}
	req.Product.Description = strings.TrimSpace(req.Product.Description)
	req.ReleaseNotes = strings.TrimSpace(req.ReleaseNotes)
	req.Product.Readme = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(req.Product.Readme, "\r\n", "\n"), "\r", "\n"))
	req.Product.Homepage = strings.TrimSpace(req.Product.Homepage)
	req.Product.Category = strings.ToLower(strings.TrimSpace(req.Product.Category))
	if req.Product.Category == "" {
		req.Product.Category = "other"
	}
	tags := []string{}
	for _, tag := range req.Product.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > 24 || len(tag) > 96 || strings.ContainsFunc(tag, func(r rune) bool { return r < 32 || r >= 127 && r <= 159 || r == ',' }) {
			return invalid("插件标签不得超过 24 个字符（96 字节），且不能含逗号或控制字符")
		}
		found := false
		for _, old := range tags {
			found = found || old == tag
		}
		if !found {
			tags = append(tags, tag)
		}
	}
	if len(tags) > 8 {
		return invalid("插件标签不能超过 8 个")
	}
	req.Product.Tags = tags
	if len(req.Packages) == 0 {
		return invalid("至少需要一个上架平台的安装包")
	}
	previous := -1
	for _, pkg := range req.Packages {
		index := -1
		for i, t := range marketTargets {
			if t == pkg.Target {
				index = i
			}
		}
		if index <= previous {
			return invalid("安装包平台无效、重复或未按上架平台顺序排列")
		}
		previous = index
		if pkg.Size > 64<<20 {
			return invalid(fmt.Sprintf("%s/%s 安装包有 %d 字节，超过云端上限 64 MiB", pkg.Target.OS, pkg.Target.Arch, pkg.Size))
		}
		if pkg.Size <= 0 || pkg.Path == "" || !digestPattern.MatchString(pkg.SHA256) {
			return invalid("安装包路径、大小或 SHA-256 无效")
		}
	}
	return req, nil
}

// releaseFor is shared by Plan and Run. Retired wins over published, exactly as rejectUsedVersion.
func releaseFor(lookup Lookup, version string) (*RemoteRelease, error) {
	if lookup.Exists && !lookup.Owned {
		return nil, refusal("product_owned_by_other")
	}
	if lookup.Exists && lookup.ProductStatus != "" && lookup.ProductStatus != "draft" && lookup.ProductStatus != "published" {
		return nil, refusal("product_unavailable")
	}
	for _, release := range lookup.Releases {
		if release.Version != version {
			continue
		}
		switch {
		case release.Status == "withdrawn" || release.ReviewStatus == "rejected":
			return nil, refusal("version_retired")
		case release.Status == "approved" || release.Status == "published":
			return nil, refusal("version_published")
		case release.Status == "draft" && release.ReviewStatus == "not_submitted":
			return &release, nil
		default:
			return nil, refusal("version_submitted")
		}
	}
	return nil, nil
}

func skipTargets(release *RemoteRelease, req Request) ([]Target, error) {
	var skip []Target
	if release == nil {
		return skip, nil
	}
	for _, pkg := range req.Packages {
		for _, remote := range release.Targets {
			if remote.Target != pkg.Target {
				continue
			}
			if !strings.EqualFold(remote.SHA256, pkg.SHA256) {
				return nil, &APIError{Reason: "artifact_conflict", Message: fmt.Sprintf("云端草稿已有 %s/%s 的另一个安装包，已上传的包不能覆盖；请在开发者后台删除该草稿后提高版本号重新发布", pkg.Target.OS, pkg.Target.Arch)}
			}
			skip = append(skip, pkg.Target)
			break
		}
	}
	return skip, nil
}

func (p *publisher) coverChanged(ctx context.Context, req Request, lookup Lookup) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if req.Product.CoverPath == "" {
		return false, "", nil
	}
	path := req.Product.CoverPath
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, "", errors.New("封面必须是普通文件，不能是符号链接")
	}
	if info.Size() < 1 || info.Size() > 2<<20 {
		return false, "", errors.New("封面图片不能超过 2 MiB，且不能为空")
	}
	file, err := os.Open(path)
	if err != nil {
		return false, "", errors.New("无法读取封面图片")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, "", errors.New("封面文件已发生变化")
	}
	local, err := io.ReadAll(io.LimitReader(file, 2<<20+1))
	if err != nil || int64(len(local)) != info.Size() {
		return false, "", errors.New("封面文件已发生变化")
	}
	if !lookup.Exists || lookup.Product == nil || lookup.Product.CoverURL == "" {
		return true, "", ctx.Err()
	}
	remote, err := p.api.FetchCover(ctx, lookup.Product.CoverURL)
	if err != nil {
		return false, "", err
	}
	return sha256.Sum256(local) != sha256.Sum256(remote), fmt.Sprintf("%x", sha256.Sum256(remote)), ctx.Err()
}

func metadataDiff(req Request, remote *RemoteProduct, coverChanged bool) []FieldChange {
	if remote == nil {
		return nil
	}
	var changes []FieldChange
	// Ordered identically to PENDING_PRODUCT_FIELDS; do not use a map for iteration.
	for _, field := range []struct {
		name      string
		old, next any
	}{
		{"name", remote.Name, req.Product.Name}, {"description", remote.Description, req.Product.Description}, {"icon", remote.Icon, req.Product.Icon},
		{"cover_url", remote.CoverURL, remote.CoverURL}, {"homepage", remote.Homepage, req.Product.Homepage}, {"readme", remote.Readme, req.Product.Readme},
		{"category", remote.Category, req.Product.Category}, {"tags", append([]string{}, remote.Tags...), req.Product.Tags},
	} {
		if field.name == "cover_url" && coverChanged {
			field.next = filepath.Base(req.Product.CoverPath)
		}
		if !reflect.DeepEqual(field.old, field.next) {
			changes = append(changes, FieldChange{Field: field.name, Old: field.old, New: field.next})
		}
	}
	return changes
}

func (p *publisher) Plan(ctx context.Context, req Request) (preview Preview, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	req, err = normalizeRequest(req)
	if err != nil {
		return
	}
	account, signedIn, err := p.api.Account(ctx)
	if err != nil {
		return preview, err
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !signedIn {
		return preview, refusal("not_signed_in")
	}
	lookup, err := p.api.Lookup(ctx, req.PluginID)
	if err != nil {
		return preview, err
	}
	if err = ctx.Err(); err != nil {
		return
	}
	release, err := releaseFor(lookup, req.Version)
	if err != nil {
		return preview, err
	}
	skip, err := skipTargets(release, req)
	if err != nil {
		return preview, err
	}
	changed, coverHash, err := p.coverChanged(ctx, req, lookup)
	if err != nil {
		return preview, err
	}
	return Preview{Account: account, NewPlugin: !lookup.Exists, ExistingDraft: release != nil, SkipTargets: skip, MetadataChanges: metadataDiff(req, lookup.Product, changed), Confirmation: confirmationFingerprint(account, lookup, coverHash)}, nil
}

func (p *publisher) Run(ctx context.Context, req Request, preview Preview, progress func(Progress)) (result Result, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	emit := func(value Progress) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if progress != nil {
			progress(value)
		}
		return ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return
	}
	req, err = normalizeRequest(req)
	if err != nil {
		return
	}
	account, signedIn, err := p.api.Account(ctx)
	if err != nil {
		return result, err
	}
	if !signedIn || account != preview.Account {
		return result, refusal("account_changed")
	}
	if err = emit(Progress{Step: StepLookup}); err != nil {
		return
	}
	lookup, err := p.api.Lookup(ctx, req.PluginID)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return
	}
	release, err := releaseFor(lookup, req.Version)
	if err != nil {
		return result, err
	}
	skip, err := skipTargets(release, req)
	if err != nil {
		return result, err
	}
	changed, coverHash, err := p.coverChanged(ctx, req, lookup)
	if err != nil {
		return result, err
	}
	if preview.Confirmation == "" || preview.Confirmation != confirmationFingerprint(account, lookup, coverHash) {
		return result, &APIError{Reason: "confirmation_changed", Message: "云端插件信息或封面已变化，请重新运行 publish 并确认"}
	}
	coverURL := ""
	if lookup.Product != nil {
		coverURL = lookup.Product.CoverURL
	}
	if changed {
		if err = emit(Progress{Step: StepCover}); err != nil {
			return
		}
		coverURL, err = p.api.UploadCover(ctx, req.Product.CoverPath, func(sent, total int64) { _ = emit(Progress{Step: StepCover, Sent: sent, Total: total}) })
		if err != nil {
			return result, err
		}
	}
	in := DraftInput{Request: req, CoverURL: coverURL}
	if err = emit(Progress{Step: StepDraft}); err != nil {
		return
	}
	if release != nil {
		result.ReleaseID = release.ID
		if err = emit(Progress{Step: StepMetadata}); err != nil {
			return
		}
		err = p.api.UpdateDraft(ctx, release.ID, in)
	} else {
		var reused bool
		result.ReleaseID, reused, err = p.api.CreateDraft(ctx, in)
		if err != nil {
			return result, err
		}
		// Another publisher can create the version between lookup and draft. The cloud's reuse
		// is read-only, so refresh inventory, check conflicts, then explicitly update metadata.
		if reused {
			if err = ctx.Err(); err != nil {
				return
			}
			lookup, err = p.api.Lookup(ctx, req.PluginID)
			if err != nil {
				return result, err
			}
			if err = ctx.Err(); err != nil {
				return
			}
			release, err = releaseFor(lookup, req.Version)
			if err != nil {
				return result, err
			}
			if release == nil || release.ID != result.ReleaseID {
				return result, errors.New("云端草稿发生变化，请重新运行 publish")
			}
			skip, err = skipTargets(release, req)
			if err != nil {
				return result, err
			}
			freshChanged, freshCoverHash, checkErr := p.coverChanged(ctx, req, lookup)
			if checkErr != nil {
				return result, checkErr
			}
			if preview.Confirmation != confirmationFingerprint(account, lookup, freshCoverHash) {
				return result, &APIError{Reason: "confirmation_changed", Message: "云端插件信息或封面已变化，请重新运行 publish 并确认"}
			}
			// Keep a local cover already uploaded by this run. Otherwise rebuild the cover
			// decision from the refreshed product, never the stale pre-create lookup.
			if !changed {
				coverURL = ""
				if lookup.Product != nil {
					coverURL = lookup.Product.CoverURL
				}
				if req.Product.CoverPath != "" {
					changed = freshChanged
					if changed {
						if err = emit(Progress{Step: StepCover}); err != nil {
							return
						}
						coverURL, err = p.api.UploadCover(ctx, req.Product.CoverPath, func(sent, total int64) { _ = emit(Progress{Step: StepCover, Sent: sent, Total: total}) })
						if err != nil {
							return result, err
						}
					}
				}
			}
			in = DraftInput{Request: req, CoverURL: coverURL}
			if err = emit(Progress{Step: StepMetadata}); err != nil {
				return
			}
			err = p.api.UpdateDraft(ctx, result.ReleaseID, in)
		}
	}
	if err != nil {
		return result, err
	}
	if result.ReleaseID < 1 {
		return result, errors.New("云端草稿响应无效")
	}
	result.Skipped = skip
	for _, pkg := range req.Packages {
		if err = ctx.Err(); err != nil {
			return
		}
		target := pkg.Target
		skipped := false
		for _, t := range skip {
			skipped = skipped || target == t
		}
		if skipped {
			if err = emit(Progress{Step: StepUpload, Target: &target, Sent: pkg.Size, Total: pkg.Size}); err != nil {
				return
			}
			continue
		}
		if err = emit(Progress{Step: StepUpload, Target: &target, Total: pkg.Size}); err != nil {
			return
		}
		err = p.api.UploadArtifact(ctx, result.ReleaseID, target, pkg.Path, pkg.SHA256, func(sent, total int64) {
			_ = emit(Progress{Step: StepUpload, Target: &target, Sent: sent, Total: total})
		})
		if err != nil {
			return result, err
		}
		result.Uploaded = append(result.Uploaded, target)
	}
	result.ReviewStatus = "not_submitted"
	if req.Submit {
		if err = emit(Progress{Step: StepSubmit}); err != nil {
			return
		}
		if err = p.api.Submit(ctx, result.ReleaseID); err != nil {
			return
		}
		result.ReviewStatus = "pending"
	}
	if err = ctx.Err(); err != nil {
		return
	}
	result.DeveloperURL = p.api.DeveloperURL(ctx)
	err = emit(Progress{Step: StepDone})
	return
}

func confirmationFingerprint(account string, lookup Lookup, coverHash string) string {
	value := struct {
		Account   string
		Exists    bool
		Product   *RemoteProduct
		CoverHash string
	}{account, lookup.Exists, lookup.Product, coverHash}
	bytes, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(bytes))
}
