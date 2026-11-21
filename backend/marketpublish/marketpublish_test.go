package marketpublish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeAPI is an in-memory cloud: it records every call in order and serves a fixed lookup.
type fakeAPI struct {
	email     string
	signedIn  bool
	accounts  []string // successive Account answers; the last one repeats
	lookup    Lookup
	cover     []byte
	releaseID int64
	reused    bool
	calls     []string
	drafts    []DraftInput
	uploads   []Target
	onUpload  func(n int)
	submitErr error
}

func (f *fakeAPI) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeAPI) Account(context.Context) (string, bool, error) {
	f.record("Account")
	email := f.email
	if len(f.accounts) > 0 {
		email = f.accounts[0]
		if len(f.accounts) > 1 {
			f.accounts = f.accounts[1:]
		}
	}
	return email, f.signedIn, nil
}

func (f *fakeAPI) Lookup(context.Context, string) (Lookup, error) {
	f.record("Lookup")
	return f.lookup, nil
}

func (f *fakeAPI) CreateDraft(_ context.Context, in DraftInput) (int64, bool, error) {
	f.record("CreateDraft")
	f.drafts = append(f.drafts, in)
	return f.releaseID, f.reused, nil
}

func (f *fakeAPI) UpdateDraft(_ context.Context, _ int64, in DraftInput) error {
	f.record("UpdateDraft")
	f.drafts = append(f.drafts, in)
	return nil
}

func (f *fakeAPI) FetchCover(context.Context, string) ([]byte, error) {
	f.record("FetchCover")
	return f.cover, nil
}

func (f *fakeAPI) UploadCover(_ context.Context, _ string, progress func(int64, int64)) (string, error) {
	f.record("UploadCover")
	progress(1, 1)
	return "https://cdn.example.com/cover-new.png", nil
}

func (f *fakeAPI) UploadArtifact(ctx context.Context, releaseID int64, target Target, _ string, _ string, progress func(int64, int64)) error {
	f.record("UploadArtifact")
	f.uploads = append(f.uploads, target)
	if f.onUpload != nil {
		f.onUpload(len(f.uploads))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress(10, 10)
	return nil
}

func (f *fakeAPI) Submit(context.Context, int64) error {
	f.record("Submit")
	return f.submitErr
}

func (f *fakeAPI) DeveloperURL(context.Context) string {
	return "https://cloud.example.com/marketplace?tab=account"
}

var allTargets = []Target{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}}

func testRequest(t *testing.T) Request {
	t.Helper()
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(cover, []byte("local cover bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := Request{
		PluginID: "acme.notes", Version: "1.2.0", ReleaseNotes: "- first", ManifestAPI: 2, Submit: true,
		Product: Product{Name: "Notes", Description: "Take notes", Icon: "note", Homepage: "https://acme.example.com",
			Readme: "# Notes", Category: "productivity", Tags: []string{"notes", "text"}, CoverPath: cover},
	}
	for i, target := range allTargets {
		sum := strings.Repeat(string(rune('a'+i)), 64)
		req.Packages = append(req.Packages, Package{Target: target, Path: filepath.Join(dir, target.OS+"-"+target.Arch+".min"), Size: 10, SHA256: sum})
	}
	return req
}

func liveProduct() *RemoteProduct {
	return &RemoteProduct{Name: "Notes", Description: "Take notes", Icon: "note", CoverURL: "https://cdn.example.com/cover.png",
		Homepage: "https://acme.example.com", Readme: "# Notes", Category: "productivity", Tags: []string{"notes", "text"}}
}

func reasonOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Reason
	}
	return ""
}

func planAndRun(t *testing.T, api *fakeAPI, req Request) (Result, error) {
	t.Helper()
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil {
		return Result{}, err
	}
	api.calls = nil
	return publisher.Run(context.Background(), req, preview, func(Progress) {})
}

func TestRunCreatesNewPluginDraftUploadsAndSubmits(t *testing.T) {
	api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 9}
	req := testRequest(t)
	var steps []Step
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil || !preview.NewPlugin || preview.ExistingDraft || preview.Account != "dev@example.com" || len(preview.MetadataChanges) != 0 {
		t.Fatalf("plan: %+v %v", preview, err)
	}
	api.calls = nil
	result, err := publisher.Run(context.Background(), req, preview, func(p Progress) { steps = append(steps, p.Step) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Account", "Lookup", "UploadCover", "CreateDraft", "UploadArtifact", "UploadArtifact", "UploadArtifact", "UploadArtifact", "Submit"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("calls = %v", api.calls)
	}
	if !reflect.DeepEqual(api.uploads, allTargets) || !reflect.DeepEqual(result.Uploaded, allTargets) || len(result.Skipped) != 0 {
		t.Fatalf("uploads = %v result = %+v", api.uploads, result)
	}
	if result.ReleaseID != 9 || result.ReviewStatus != "pending" || result.DeveloperURL == "" {
		t.Fatalf("result = %+v", result)
	}
	draft := api.drafts[0]
	if draft.CoverURL != "https://cdn.example.com/cover-new.png" || draft.Request.Product.Name != "Notes" || draft.Request.ReleaseNotes != "- first" {
		t.Fatalf("draft input = %+v", draft)
	}
	if steps[0] != StepLookup || steps[len(steps)-1] != StepDone {
		t.Fatalf("steps = %v", steps)
	}
	for _, step := range []Step{StepCover, StepDraft, StepUpload, StepSubmit} {
		found := false
		for _, s := range steps {
			found = found || s == step
		}
		if !found {
			t.Fatalf("missing step %s in %v", step, steps)
		}
	}
}

func TestRunReusesDraftAndSkipsUploadedTargets(t *testing.T) {
	req := testRequest(t)
	cover, _ := os.ReadFile(req.Product.CoverPath)
	api := &fakeAPI{email: "dev@example.com", signedIn: true, cover: cover, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: liveProduct(),
		Releases: []RemoteRelease{
			{ID: 3, Version: "1.1.0", Status: "published", ReviewStatus: "approved"},
			{ID: 7, Version: "1.2.0", Status: "draft", ReviewStatus: "not_submitted", Targets: []RemoteTarget{{Target: allTargets[0], SHA256: strings.ToUpper(req.Packages[0].SHA256)}}},
		}}}
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil || preview.NewPlugin || !preview.ExistingDraft || !reflect.DeepEqual(preview.SkipTargets, allTargets[:1]) || len(preview.MetadataChanges) != 0 {
		t.Fatalf("plan: %+v %v", preview, err)
	}
	api.calls = nil
	result, err := publisher.Run(context.Background(), req, preview, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Account", "Lookup", "FetchCover", "UpdateDraft", "UploadArtifact", "UploadArtifact", "UploadArtifact", "Submit"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("calls = %v", api.calls)
	}
	if !reflect.DeepEqual(result.Skipped, allTargets[:1]) || !reflect.DeepEqual(result.Uploaded, allTargets[1:]) || result.ReleaseID != 7 {
		t.Fatalf("result = %+v", result)
	}
	if api.drafts[0].CoverURL != "https://cdn.example.com/cover.png" {
		t.Fatalf("unchanged cover must keep the live URL: %+v", api.drafts[0])
	}
}

func TestRunRejectsDifferentUploadedPackage(t *testing.T) {
	req := testRequest(t)
	api := &fakeAPI{email: "dev@example.com", signedIn: true, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "draft", Product: liveProduct(),
		Releases: []RemoteRelease{{ID: 7, Version: "1.2.0", Status: "draft", ReviewStatus: "not_submitted",
			Targets: []RemoteTarget{{Target: allTargets[2], SHA256: strings.Repeat("f", 64)}}}}}}
	if _, err := New(api).Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "云端草稿已有 windows/amd64 的另一个安装包") {
		t.Fatalf("plan: %v", err)
	}
	api.calls = nil
	_, err := New(api).Run(context.Background(), req, Preview{Account: "dev@example.com"}, func(Progress) {})
	want := "云端草稿已有 windows/amd64 的另一个安装包，已上传的包不能覆盖；请在开发者后台删除该草稿后提高版本号重新发布"
	if err == nil || err.Error() != want {
		t.Fatalf("run: %v", err)
	}
	for _, call := range api.calls {
		if call == "UploadArtifact" || call == "Submit" || call == "UpdateDraft" {
			t.Fatalf("nothing may be written after a conflicting package: %v", api.calls)
		}
	}
}

func TestRunStopsAtDraftWhenSubmitFalse(t *testing.T) {
	req := testRequest(t)
	req.Submit = false
	api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 4}
	result, err := planAndRun(t, api, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if call == "Submit" {
			t.Fatalf("submitted although Submit is false: %v", api.calls)
		}
	}
	if result.ReviewStatus != "not_submitted" || len(result.Uploaded) != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPlanReportsMetadataChanges(t *testing.T) {
	req := testRequest(t)
	req.Product.Name = "Notes Pro"
	req.Product.Tags = []string{"Notes", " editor ", "notes"}
	req.Product.Category = ""
	live := liveProduct()
	live.Category = "other"
	req.Product.Readme = "# Notes\r\n"
	api := &fakeAPI{email: "dev@example.com", signedIn: true, cover: []byte("old cover"),
		lookup: Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: live}}
	preview, err := New(api).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := []FieldChange{
		{Field: "name", Old: "Notes", New: "Notes Pro"},
		{Field: "cover_url", Old: "https://cdn.example.com/cover.png", New: "cover.png"},
		{Field: "tags", Old: []string{"notes", "text"}, New: []string{"notes", "editor"}},
	}
	if !reflect.DeepEqual(preview.MetadataChanges, want) {
		t.Fatalf("changes = %#v", preview.MetadataChanges)
	}
	if preview.NewPlugin || preview.ExistingDraft {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestRunRefusesOtherOwnersProduct(t *testing.T) {
	api := &fakeAPI{email: "dev@example.com", signedIn: true, lookup: Lookup{Exists: true}}
	req := testRequest(t)
	if _, err := New(api).Plan(context.Background(), req); reasonOf(err) != "product_owned_by_other" || err.Error() != "该插件 ID 已被其他开发者使用，请换一个 ID" {
		t.Fatalf("plan: %v", err)
	}
	api.calls = nil
	if _, err := New(api).Run(context.Background(), req, Preview{Account: "dev@example.com"}, func(Progress) {}); reasonOf(err) != "product_owned_by_other" {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(api.calls, []string{"Account", "Lookup"}) {
		t.Fatalf("calls = %v", api.calls)
	}
}

func TestRunRefusesUsedVersions(t *testing.T) {
	cases := []struct{ status, review, reason string }{
		{"pending_review", "pending", "version_submitted"},
		{"published", "approved", "version_published"},
		{"approved", "approved", "version_published"},
		{"withdrawn", "not_submitted", "version_retired"},
		{"draft", "rejected", "version_retired"},
	}
	for _, c := range cases {
		req := testRequest(t)
		api := &fakeAPI{email: "dev@example.com", signedIn: true, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: liveProduct(),
			Releases: []RemoteRelease{{ID: 5, Version: "1.2.0", Status: c.status, ReviewStatus: c.review}}}}
		if _, err := New(api).Plan(context.Background(), req); reasonOf(err) != c.reason {
			t.Fatalf("plan %s/%s: %v", c.status, c.review, err)
		}
		api.calls = nil
		if _, err := New(api).Run(context.Background(), req, Preview{Account: "dev@example.com"}, func(Progress) {}); reasonOf(err) != c.reason {
			t.Fatalf("run %s/%s: %v", c.status, c.review, err)
		}
		if !reflect.DeepEqual(api.calls, []string{"Account", "Lookup"}) {
			t.Fatalf("calls = %v", api.calls)
		}
	}
}

func TestRunFailsWhenAccountChanged(t *testing.T) {
	api := &fakeAPI{signedIn: true, accounts: []string{"dev@example.com", "other@example.com"}}
	_, err := planAndRun(t, api, testRequest(t))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "account_changed" || apiErr.Message != "云账号已切换，请重新运行 publish" {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(api.calls, []string{"Account"}) {
		t.Fatalf("calls = %v", api.calls)
	}
}

func TestPlanRequiresSignIn(t *testing.T) {
	api := &fakeAPI{}
	if _, err := New(api).Plan(context.Background(), testRequest(t)); reasonOf(err) != "not_signed_in" {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanValidatesLimitsBeforeNetwork(t *testing.T) {
	cases := map[string]func(*Request){
		"发布说明有 10001 字节，超过云端上限 10000 字节":              func(r *Request) { r.ReleaseNotes = strings.Repeat("a", 10001) },
		"README 有 65536 字节，超过云端上限 65535 字节":           func(r *Request) { r.Product.Readme = strings.Repeat("a", 65536) },
		"插件名称有 121 字节，超过云端上限 120 字节":                  func(r *Request) { r.Product.Name = strings.Repeat("a", 121) },
		"插件简介有 2001 字节，超过云端上限 2000 字节":                func(r *Request) { r.Product.Description = strings.Repeat("a", 2001) },
		"darwin/amd64 安装包有 67108865 字节，超过云端上限 64 MiB": func(r *Request) { r.Packages[1].Size = 64<<20 + 1 },
	}
	for want, mutate := range cases {
		api := &fakeAPI{email: "dev@example.com", signedIn: true}
		req := testRequest(t)
		mutate(&req)
		_, err := New(api).Plan(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), want) || reasonOf(err) != "invalid_request" {
			t.Fatalf("want %q, got %v", want, err)
		}
		if len(api.calls) != 0 {
			t.Fatalf("network called before local validation: %v", api.calls)
		}
	}
}

func TestRunHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 9}
	api.onUpload = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	req := testRequest(t)
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	_, err = publisher.Run(ctx, req, preview, func(Progress) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := api.calls[len(api.calls)-1]; got != "UploadArtifact" || len(api.uploads) != 2 {
		t.Fatalf("calls after cancel: %v", api.calls)
	}
}

func TestPlanRefusesUnavailableProduct(t *testing.T) {
	req := testRequest(t)
	api := &fakeAPI{email: "dev@example.com", signedIn: true, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "disabled", Product: liveProduct()}}
	if _, err := New(api).Plan(context.Background(), req); reasonOf(err) != "product_unavailable" {
		t.Fatalf("plan err=%v", err)
	}
	if _, err := New(api).Run(context.Background(), req, Preview{Account: "dev@example.com"}, nil); reasonOf(err) != "product_unavailable" {
		t.Fatalf("run err=%v", err)
	}
}

func TestRunCancelFromProgressPreventsNextWrite(t *testing.T) {
	req := testRequest(t)
	api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 7}
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.calls = nil
	_, err = publisher.Run(ctx, req, preview, func(p Progress) {
		if p.Step == StepDraft {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(api.calls, []string{"Account", "Lookup", "UploadCover"}) {
		t.Fatalf("calls=%v", api.calls)
	}
}

type draftRaceAPI struct {
	*fakeAPI
	next      Lookup
	nextCover []byte
}

func (f *draftRaceAPI) CreateDraft(ctx context.Context, in DraftInput) (int64, bool, error) {
	id, _, err := f.fakeAPI.CreateDraft(ctx, in)
	f.lookup = f.next
	if f.nextCover != nil {
		f.cover = f.nextCover
	}
	return id, true, err
}
func TestRunHandlesDraftCreatedAfterLookup(t *testing.T) {
	req := testRequest(t)
	req.Product.CoverPath = ""
	api := &draftRaceAPI{fakeAPI: &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 7, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: liveProduct()}}, next: Lookup{Exists: true, Owned: true, ProductStatus: "draft", Product: liveProduct(), Releases: []RemoteRelease{
		{ID: 7, Version: "1.2.0", Status: "draft", ReviewStatus: "not_submitted", Targets: []RemoteTarget{{Target: allTargets[0], SHA256: req.Packages[0].SHA256}}},
	}}}
	publisher := New(api)
	preview, err := publisher.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	result, err := publisher.Run(context.Background(), req, preview, nil)
	if err != nil || !reflect.DeepEqual(result.Skipped, allTargets[:1]) || !reflect.DeepEqual(result.Uploaded, allTargets[1:]) {
		t.Fatalf("result=%+v %v", result, err)
	}
	if len(api.drafts) != 2 || api.drafts[1].CoverURL != "https://cdn.example.com/cover.png" {
		t.Fatalf("reused update cleared concurrent cover: %+v", api.drafts)
	}
	want := []string{"Account", "Lookup", "CreateDraft", "Lookup", "UpdateDraft", "UploadArtifact", "UploadArtifact", "UploadArtifact", "Submit"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("calls=%v", api.calls)
	}
}

func TestRunPublishesExistingHostCloudPluginIDs(t *testing.T) {
	for _, id := range []string{"acme_notes", "2fa-helper", "acme..notes", "acme-_notes", "a_", strings.Repeat("a", 128)} {
		t.Run(id, func(t *testing.T) {
			req := testRequest(t)
			req.PluginID = id
			req.Product.CoverPath = ""
			api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 9, lookup: Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: liveProduct(), Releases: []RemoteRelease{{ID: 3, Version: "1.1.0", Status: "published", ReviewStatus: "approved"}}}}
			result, err := planAndRun(t, api, req)
			if err != nil || result.ReleaseID != 9 || len(api.drafts) != 1 || api.drafts[0].Request.PluginID != id || len(result.Uploaded) != 4 {
				t.Fatalf("ID %q result=%+v err=%v", id, result, err)
			}
		})
	}
}

func TestPlanAndRunRejectInvalidOrReservedPluginIDs(t *testing.T) {
	for _, id := range []string{"a", "Acme_notes", "acme/notes", "acme notes", "_acme", "mincode.notes", strings.Repeat("a", 129)} {
		t.Run(id, func(t *testing.T) {
			req := testRequest(t)
			req.PluginID = id
			api := &fakeAPI{email: "dev@example.com", signedIn: true}
			publisher := New(api)
			if _, err := publisher.Plan(context.Background(), req); reasonOf(err) != "invalid_request" {
				t.Fatalf("plan err=%v", err)
			}
			if _, err := publisher.Run(context.Background(), req, Preview{Account: "dev@example.com"}, nil); reasonOf(err) != "invalid_request" {
				t.Fatalf("run err=%v", err)
			}
			if len(api.calls) != 0 {
				t.Fatalf("invalid ID reached cloud: %v", api.calls)
			}
		})
	}
}

func TestRunReusedDraftRejectsUnconfirmedRemoteCover(t *testing.T) {
	for _, c := range []struct {
		name           string
		existing       bool
		refreshedCover []byte
	}{
		{"already uploaded local cover", false, []byte("concurrent bytes")},
		{"new URL with same local bytes", true, []byte("local cover bytes")},
		{"concurrent cover differs from local", true, []byte("concurrent bytes")},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := testRequest(t)
			api := &draftRaceAPI{fakeAPI: &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 7, cover: []byte("local cover bytes")}, nextCover: c.refreshedCover,
				next: Lookup{Exists: true, Owned: true, ProductStatus: "draft", Product: liveProduct(), Releases: []RemoteRelease{{ID: 7, Version: "1.2.0", Status: "draft", ReviewStatus: "not_submitted"}}}}
			api.next.Product.CoverURL = "https://cdn.example.com/concurrent.png"
			if c.existing {
				api.lookup = Lookup{Exists: true, Owned: true, ProductStatus: "published", Product: liveProduct()}
			}
			publisher := New(api)
			preview, err := publisher.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			api.calls = nil
			api.drafts = nil
			_, err = publisher.Run(context.Background(), req, preview, nil)
			if reasonOf(err) != "confirmation_changed" {
				t.Fatalf("changed product accepted: %v", err)
			}
			if len(api.drafts) != 1 || len(api.uploads) != 0 {
				t.Fatalf("unconfirmed update: %+v", api.drafts)
			}
		})
	}
}

func TestRunRequiresReconfirmationForRemoteMetadataOrCoverChanges(t *testing.T) {
	for _, kind := range []string{"metadata", "cover_url", "cover_bytes"} {
		t.Run(kind, func(t *testing.T) {
			req := testRequest(t)
			api := &fakeAPI{email: "dev@example.com", signedIn: true, releaseID: 9, lookup: Lookup{Exists: true, Owned: true, Product: liveProduct()}, cover: []byte("remote old")}
			p := New(api)
			preview, err := p.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "metadata":
				api.lookup.Product.Name = "changed elsewhere"
			case "cover_url":
				api.lookup.Product.CoverURL = "https://cdn.example.com/other.png"
			case "cover_bytes":
				api.cover = []byte("remote replacement")
			}
			api.calls = nil
			_, err = p.Run(context.Background(), req, preview, nil)
			if reasonOf(err) != "confirmation_changed" {
				t.Fatalf("changed approval accepted: %v", err)
			}
			for _, call := range api.calls {
				if call == "UploadCover" || call == "CreateDraft" || call == "UpdateDraft" || call == "UploadArtifact" || call == "Submit" {
					t.Fatal("side effect before reconfirmation", call)
				}
			}
		})
	}
}
