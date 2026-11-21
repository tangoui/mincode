package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/marketpublish"
)

type devTestPublisher struct {
	plan        func(context.Context, marketpublish.Request) (marketpublish.Preview, error)
	run         func(context.Context, marketpublish.Request, marketpublish.Preview, func(marketpublish.Progress)) (marketpublish.Result, error)
	plans, runs atomic.Int32
}

func (p *devTestPublisher) Plan(ctx context.Context, r marketpublish.Request) (marketpublish.Preview, error) {
	p.plans.Add(1)
	if p.plan != nil {
		return p.plan(ctx, r)
	}
	return marketpublish.Preview{Account: "dev@example.com", NewPlugin: true}, nil
}
func (p *devTestPublisher) Run(ctx context.Context, r marketpublish.Request, v marketpublish.Preview, f func(marketpublish.Progress)) (marketpublish.Result, error) {
	p.runs.Add(1)
	if p.run != nil {
		return p.run(ctx, r, v, f)
	}
	f(marketpublish.Progress{Step: marketpublish.StepDone})
	return marketpublish.Result{ReleaseID: 42, ReviewStatus: "pending", Uploaded: []marketpublish.Target{{OS: "darwin", Arch: "arm64"}}}, nil
}
func devPublishInput(t *testing.T) pluginDevPublishRequest {
	t.Helper()
	root := t.TempDir()
	release := filepath.Join(root, "release")
	if err := os.Mkdir(release, 0700); err != nil {
		t.Fatal(err)
	}
	in := pluginDevPublishRequest{ProtocolVersion: 1, CLIInstanceID: "cli", ProjectRoot: root, ReleaseDir: release, PluginID: "test.plugin", Name: "测试插件", Version: "1.0.0", Category: "other", ManifestAPI: 2, Submit: true}
	for i, target := range []marketpublish.Target{{OS: "darwin", Arch: "arm64"}, {OS: "darwin", Arch: "amd64"}, {OS: "windows", Arch: "amd64"}, {OS: "linux", Arch: "amd64"}} {
		zip := writePluginTestZIP(t, []zipTestEntry{{name: "plugin.json", data: []byte(`{"id":"test.plugin","version":"1.0.0","apiVersion":2}`)}})
		bytes, err := os.ReadFile(zip)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(release, []string{"mac-arm.min", "mac.min", "win.min", "linux.min"}[i])
		if err = os.WriteFile(path, bytes, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(bytes)
		in.Packages = append(in.Packages, pluginDevPublishPackageInput{OS: target.OS, Arch: target.Arch, Path: path, Size: int64(len(bytes)), SHA256: hex.EncodeToString(sum[:])})
	}
	return in
}
func devPublishBridge(t *testing.T, p marketpublish.Publisher) *pluginDevBridge {
	t.Helper()
	b := newPluginDevBridge()
	b.instanceToken = "instance-secret"
	SetDevPublisherFactory(func() marketpublish.Publisher { return p })
	t.Cleanup(func() { b.Close(); SetDevPublisherFactory(nil) })
	return b
}
func devPublishHTTP(b *pluginDevBridge, method, path, token string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://127.0.0.1"+pluginDevBridgePrefix+path, strings.NewReader(string(data)))
	r.RemoteAddr = "127.0.0.1:4567"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	if path == "/publish" {
		b.handlePublish(w, r)
	} else {
		b.handlePublishStatus(w, r)
	}
	return w
}
func devCreate(t *testing.T, b *pluginDevBridge, in pluginDevPublishRequest) string {
	t.Helper()
	w := devPublishHTTP(b, "POST", "/publish", "instance-secret", in)
	if w.Code != 202 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var out struct {
		PublishID string `json:"publishId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	devWaitPrepared(t, b, out.PublishID)
	return out.PublishID
}
func devWaitPrepared(t *testing.T, b *pluginDevBridge, id string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if devStatus(t, b, id).Status != "preparing" {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("preparation did not finish")
		case <-tick.C:
		}
	}
}
func devStatus(t *testing.T, b *pluginDevBridge, id string) pluginDevPublishStatus {
	t.Helper()
	for _, s := range b.Publishes() {
		if s.ID == id {
			return s
		}
	}
	t.Fatal("missing publish")
	return pluginDevPublishStatus{}
}
func devWait(t *testing.T, b *pluginDevBridge, id, status string) pluginDevPublishStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := devStatus(t, b, id)
		if s.Status == status {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("want %s got %+v", status, devStatus(t, b, id))
	return pluginDevPublishStatus{}
}
func TestPluginDevPublishRequiresInstanceToken(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	for _, token := range []string{"", "lease-token"} {
		if w := devPublishHTTP(b, "POST", "/publish", token, devPublishInput(t)); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/publish", nil)
	r.RemoteAddr = "10.0.0.1:123"
	r.Header.Set("Authorization", "Bearer instance-secret")
	w := httptest.NewRecorder()
	b.handlePublish(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if p.plans.Load() != 0 {
		t.Fatal("unauthenticated plan")
	}
}
func TestPluginDevPublishRejectsPathsOutsideReleaseDir(t *testing.T) {
	for _, kind := range []string{"outside", "symlink", "oversize", "hash", "manifest", "externalRelease"} {
		t.Run(kind, func(t *testing.T) {
			p := &devTestPublisher{}
			b := devPublishBridge(t, p)
			in := devPublishInput(t)
			want := "package_invalid"
			switch kind {
			case "outside":
				in.Packages[0].Path = filepath.Join(in.ReleaseDir, "..", "x.min")
				want = "path_outside"
			case "symlink":
				path := in.Packages[0].Path
				os.Remove(path)
				os.Symlink(filepath.Join(t.TempDir(), "x"), path)
			case "oversize":
				f, _ := os.OpenFile(in.Packages[0].Path, os.O_WRONLY, 0600)
				f.Truncate(64<<20 + 1)
				f.Close()
			case "hash":
				in.Packages[0].SHA256 = strings.Repeat("a", 64)
			case "manifest":
				in.PluginID = "other.plugin"
			case "externalRelease":
				in.ReleaseDir = t.TempDir()
				want = "path_outside"
			}
			id := devCreate(t, b, in)
			status := devStatus(t, b, id)
			if status.Status != "failed" || status.ErrorCode != want {
				t.Fatalf("%+v want %s", status, want)
			}
			if p.plans.Load() != 0 {
				t.Fatal("invalid input planned")
			}
		})
	}
}
func TestPluginDevPublishConfirmRunsPublisher(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	if devStatus(t, b, id).Status != "pending_confirmation" {
		t.Fatal("not pending")
	}
	if err := b.ConfirmPublish(id); err != nil {
		t.Fatal(err)
	}
	devWait(t, b, id, "succeeded")
	w := devPublishHTTP(b, "GET", "/publish/"+id, "instance-secret", nil)
	if !strings.Contains(w.Body.String(), `"releaseId":42`) {
		t.Fatal(w.Body.String())
	}
	if p.runs.Load() != 1 {
		t.Fatal("not one run")
	}
}
func TestPluginDevPublishRejectStopsCLI(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	if err := b.RejectPublish(id); err != nil {
		t.Fatal(err)
	}
	b.RejectPublish(id)
	if devStatus(t, b, id).Status != "rejected" || p.runs.Load() != 0 {
		t.Fatal("rejection ignored")
	}
}
func TestPluginDevPublishConfirmationExpiresAfterTwoMinutes(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	b.pruneLocked(pluginDevNow().Add(2*time.Minute + time.Second))
	b.mu.Unlock()
	s := devStatus(t, b, id)
	if s.Status != "expired" || s.ErrorCode != "confirmation_timeout" {
		t.Fatalf("%+v", s)
	}
	if b.ConfirmPublish(id) == nil {
		t.Fatal("expired confirmed")
	}
}
func TestPluginDevPublishAwaitsLoginThenConfirms(t *testing.T) {
	var login atomic.Bool
	p := &devTestPublisher{plan: func(context.Context, marketpublish.Request) (marketpublish.Preview, error) {
		if !login.Load() {
			return marketpublish.Preview{}, &marketpublish.APIError{Reason: "not_signed_in"}
		}
		return marketpublish.Preview{Account: "dev@example.com"}, nil
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	if devStatus(t, b, id).Status != "awaiting_login" {
		t.Fatal("not awaiting")
	}
	login.Store(true)
	if err := b.RefreshPublish(id); err != nil {
		t.Fatal(err)
	}
	if devStatus(t, b, id).Status != "pending_confirmation" {
		t.Fatal("not refreshed")
	}
}

// Isolated login timeout keeps the same id+version exclusion meaningful.
func TestPluginDevPublishLoginTimeout(t *testing.T) {
	p := &devTestPublisher{plan: func(context.Context, marketpublish.Request) (marketpublish.Preview, error) {
		return marketpublish.Preview{}, &marketpublish.APIError{Reason: "not_signed_in"}
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	b.pruneLocked(pluginDevNow().Add(5*time.Minute + time.Second))
	b.mu.Unlock()
	if s := devStatus(t, b, id); s.ErrorCode != "login_timeout" {
		t.Fatalf("%+v", s)
	}
}
func TestPluginDevPublishRevalidatesFilesOnConfirm(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	id := devCreate(t, b, in)
	os.WriteFile(in.Packages[0].Path, []byte("changed"), 0600)
	if b.ConfirmPublish(id) == nil {
		t.Fatal("changed package confirmed")
	}
	if s := devStatus(t, b, id); s.Status != "failed" || s.ErrorCode != "package_changed" {
		t.Fatalf("%+v", s)
	}
	if p.runs.Load() != 0 {
		t.Fatal("ran")
	}
}
func TestPluginDevPublishFailsWhenAccountChanged(t *testing.T) {
	p := &devTestPublisher{run: func(context.Context, marketpublish.Request, marketpublish.Preview, func(marketpublish.Progress)) (marketpublish.Result, error) {
		return marketpublish.Result{}, &marketpublish.APIError{Reason: "account_changed", Message: "secret token"}
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.ConfirmPublish(id)
	s := devWait(t, b, id, "failed")
	if s.ErrorCode != "account_changed" || !strings.Contains(s.ErrorMessage, "账号") {
		t.Fatal(s)
	}
}
func TestPluginDevPublishConfirmRejectsUnknownFields(t *testing.T) {
	for _, args := range []string{`{"id":"x","remember":true}`, `{"id":"x","yes":true}`, `{"id":"x"} {}`} {
		if _, err := pluginHostDevelopmentPublishConfirmHandler("", args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestPluginDevPublishCancelStopsRun(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	p := &devTestPublisher{run: func(ctx context.Context, _ marketpublish.Request, _ marketpublish.Preview, _ func(marketpublish.Progress)) (marketpublish.Result, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return marketpublish.Result{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.ConfirmPublish(id)
	<-started
	if err := b.CancelPublish(id); err != nil {
		t.Fatal(err)
	}
	b.CancelPublish(id)
	<-stopped
	devWait(t, b, id, "canceled")
	if b.ConfirmPublish(id) == nil {
		t.Fatal("canceled confirmed")
	}
}
func TestPluginDevPublishOnePerVersion(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	in := devPublishInput(t)
	devCreate(t, b, in)
	if w := devPublishHTTP(b, "POST", "/publish", "instance-secret", in); w.Code != 409 || !strings.Contains(w.Body.String(), "publish in progress") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPluginDevPublishStatusKeysAreFixed(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	events := make(chan string, 30)
	core.SetEmitter(func(event string, data any) {
		if event == pluginDevPublishEvent {
			bytes, _ := json.Marshal(data)
			events <- string(bytes)
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	id := devCreate(t, b, devPublishInput(t))
	w := devPublishHTTP(b, "GET", "/publish/"+id, "instance-secret", nil)
	var httpValue, eventValue map[string]any
	json.Unmarshal(w.Body.Bytes(), &httpValue)
	select {
	case data := <-events:
		json.Unmarshal([]byte(data), &eventValue)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	for index, value := range []map[string]any{httpValue, eventValue} {
		keys := map[string]bool{}
		for key := range value {
			keys[key] = true
		}
		want := map[string]bool{}
		fields := "id status step pluginId pluginName version releaseDir account newPlugin existingDraft submit releaseNotes packages metadataChanges expiresAt"
		if index == 0 {
			fields = "id status step packages expiresAt"
		}
		for _, key := range strings.Fields(fields) {
			want[key] = true
		}
		if !reflect.DeepEqual(keys, want) {
			t.Fatal(keys)
		}
		bytes, _ := json.Marshal(value)
		for _, secret := range []string{"token", "Token", "ba-user-token", "instance-secret"} {
			if strings.Contains(string(bytes), secret) {
				t.Fatal("secret leaked")
			}
		}
	}
}
func TestPluginDevPublishImmutableSnapshots(t *testing.T) {
	ready := make(chan marketpublish.Request, 1)
	release := make(chan struct{})
	observed := make(chan [][]byte, 1)
	p := &devTestPublisher{run: func(ctx context.Context, r marketpublish.Request, _ marketpublish.Preview, _ func(marketpublish.Progress)) (marketpublish.Result, error) {
		ready <- r
		select {
		case <-release:
		case <-ctx.Done():
			return marketpublish.Result{}, ctx.Err()
		}
		pkg, _ := os.ReadFile(r.Packages[0].Path)
		cover, _ := os.ReadFile(r.Product.CoverPath)
		observed <- [][]byte{pkg, cover}
		return marketpublish.Result{ReleaseID: 1}, nil
	}}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	in.CoverPath = filepath.Join(in.ProjectRoot, "cover.png")
	os.WriteFile(in.CoverPath, []byte("original cover"), 0600)
	expected, _ := os.ReadFile(in.Packages[0].Path)
	alias := filepath.Join(t.TempDir(), "hardlink.min")
	if err := os.Link(in.Packages[0].Path, alias); err != nil {
		t.Fatal(err)
	}
	id := devCreate(t, b, in)
	if err := b.ConfirmPublish(id); err != nil {
		t.Fatal(err)
	}
	snapshot := <-ready
	os.WriteFile(alias, []byte("malicious mutation through hardlink"), 0600)
	os.Remove(in.CoverPath)
	os.WriteFile(in.CoverPath, []byte("replacement cover"), 0600)
	close(release)
	got := <-observed
	if string(got[0]) != string(expected) || string(got[1]) != "original cover" {
		t.Fatal("mutable upload")
	}
	devWait(t, b, id, "succeeded")
	b.Close()
	if _, err := os.Stat(snapshot.Packages[0].Path); !os.IsNotExist(err) {
		t.Fatal("snapshot retained after close")
	}
	if _, err := os.Stat(in.Packages[0].Path); err != nil {
		t.Fatal("user path deleted")
	}
}
func TestPluginDevPublishCoverChangedBeforeConfirm(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	in.CoverPath = filepath.Join(in.ProjectRoot, "cover.png")
	os.WriteFile(in.CoverPath, []byte("cover"), 0600)
	id := devCreate(t, b, in)
	os.WriteFile(in.CoverPath, []byte("other"), 0600)
	if b.ConfirmPublish(id) == nil || p.runs.Load() != 0 {
		t.Fatal("cover mutation accepted")
	}
}

func TestPluginDevPublishFactoryBindsOnlyOneOperation(t *testing.T) {
	var email atomic.Value
	email.Store("first@example.com")
	var created atomic.Int32
	b := devPublishBridge(t, &devTestPublisher{})
	SetDevPublisherFactory(func() marketpublish.Publisher {
		created.Add(1)
		bound := ""
		return &devTestPublisher{
			plan: func(context.Context, marketpublish.Request) (marketpublish.Preview, error) {
				bound = email.Load().(string)
				return marketpublish.Preview{Account: bound}, nil
			},
			run: func(ctx context.Context, r marketpublish.Request, p marketpublish.Preview, f func(marketpublish.Progress)) (marketpublish.Result, error) {
				if bound != email.Load().(string) {
					return marketpublish.Result{}, &marketpublish.APIError{Reason: "account_changed"}
				}
				return marketpublish.Result{ReleaseID: 1}, nil
			},
		}
	})
	in := devPublishInput(t)
	id := devCreate(t, b, in)
	b.ConfirmPublish(id)
	devWait(t, b, id, "succeeded")
	email.Store("second@example.com")
	id = devCreate(t, b, in)
	if devStatus(t, b, id).Account != "second@example.com" {
		t.Fatal("factory reused bound identity")
	}
	b.ConfirmPublish(id)
	devWait(t, b, id, "succeeded")
	id = devCreate(t, b, in)
	email.Store("third@example.com")
	b.ConfirmPublish(id)
	s := devWait(t, b, id, "failed")
	if s.ErrorCode != "account_changed" || created.Load() != 3 {
		t.Fatalf("%+v factories=%d", s, created.Load())
	}
}
func TestPluginDevPublishDeepCopiesInputAndPreview(t *testing.T) {
	tags := []string{"old"}
	p := &devTestPublisher{plan: func(_ context.Context, r marketpublish.Request) (marketpublish.Preview, error) {
		r.Packages[0].SHA256 = "mutated"
		r.Product.Tags[0] = "mutated"
		return marketpublish.Preview{Account: "dev@example.com", MetadataChanges: []marketpublish.FieldChange{{Field: "tags", Old: tags, New: []string{"next"}}}}, nil
	}}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	in.Tags = []string{"next"}
	id := devCreate(t, b, in)
	tags[0] = "mutated"
	s := devStatus(t, b, id)
	if strings.Contains(string(devPublishJSON(t, s)), "mutated") {
		t.Fatal("preview alias")
	}
	s.Packages[0].SHA256 = "other"
	if devStatus(t, b, id).Packages[0].SHA256 == "other" {
		t.Fatal("public alias")
	}
	b.ConfirmPublish(id)
	devWait(t, b, id, "succeeded")
}
func devPublishJSON(t *testing.T, v any) []byte {
	t.Helper()
	bytes, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes
}
func TestPluginDevPublishCancellationHoldsVersionUntilWorkerExits(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p := &devTestPublisher{run: func(ctx context.Context, r marketpublish.Request, p marketpublish.Preview, f func(marketpublish.Progress)) (marketpublish.Result, error) {
		close(started)
		<-release
		return marketpublish.Result{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	id := devCreate(t, b, in)
	b.ConfirmPublish(id)
	<-started
	b.CancelPublish(id)
	w := devPublishHTTP(b, "POST", "/publish", "instance-secret", in)
	close(release)
	if w.Code != 409 {
		t.Fatalf("canceled worker still in flight: %d", w.Code)
	}
}
func TestPluginDevPublishProgressThrottle(t *testing.T) {
	events := make(chan pluginDevPublishStatus, 100)
	core.SetEmitter(func(name string, data any) {
		if name == pluginDevPublishEvent {
			events <- data.(pluginDevPublishStatus)
		}
	})
	defer core.SetEmitter(nil)
	p := &devTestPublisher{run: func(ctx context.Context, r marketpublish.Request, p marketpublish.Preview, f func(marketpublish.Progress)) (marketpublish.Result, error) {
		for i := 0; i < 100; i++ {
			f(marketpublish.Progress{Step: marketpublish.StepUpload, Target: &r.Packages[0].Target, Sent: int64(i), Total: 100})
		}
		f(marketpublish.Progress{Step: marketpublish.StepSubmit})
		return marketpublish.Result{ReleaseID: 1}, nil
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.ConfirmPublish(id)
	devWait(t, b, id, "succeeded")
	close(events)
	uploads, submits := 0, 0
	for s := range events {
		if s.Step == "upload" {
			uploads++
		}
		if s.Step == "submit" {
			submits++
		}
	}
	if uploads != 1 || submits != 1 {
		t.Fatalf("uploads=%d submits=%d", uploads, submits)
	}
}
func TestPluginDevPublishRetainsThenRemovesOnlyOwnedSnapshots(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	id := devCreate(t, b, in)
	b.mu.Lock()
	dir := b.publishes[id].dirs[0]
	b.mu.Unlock()
	b.RejectPublish(id)
	b.mu.Lock()
	b.pruneLocked(pluginDevNow().Add(10*time.Minute + time.Second))
	b.mu.Unlock()
	if len(b.Publishes()) != 0 {
		t.Fatal("expired terminal retained")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("snapshot not removed")
	}
	if _, err := os.Stat(in.Packages[0].Path); err != nil {
		t.Fatal("user path removed")
	}
}

func TestPluginDevPublishCloseWaitsForInitialSnapshotValidation(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	cache := t.TempDir()
	t.Setenv("TMPDIR", cache)
	file, err := os.OpenFile(in.Packages[0].Path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(63 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	in.Packages[0].Size = 63 << 20
	done := make(chan struct{})
	go func() { defer close(done); devPublishHTTP(b, "POST", "/publish", "instance-secret", in) }()
	deadline := time.Now().Add(time.Second)
	for {
		entries, _ := os.ReadDir(cache)
		if len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot not started")
		}
		time.Sleep(time.Millisecond)
	}
	b.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Millisecond):
		<-done
		t.Fatal("Close returned before initial snapshot validation completed")
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 0 {
		t.Fatal("staging snapshot leaked after Close")
	}
}

func TestPluginDevPublishTerminalExpiryCleansAfterCanceledWorkerFinishes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p := &devTestPublisher{run: func(ctx context.Context, _ marketpublish.Request, _ marketpublish.Preview, _ func(marketpublish.Progress)) (marketpublish.Result, error) {
		close(started)
		<-release
		return marketpublish.Result{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.ConfirmPublish(id)
	<-started
	b.CancelPublish(id)
	b.mu.Lock()
	dir := b.publishes[id].dirs[0]
	b.publishes[id].status.ExpiresAt = pluginDevNow().Add(-time.Second)
	b.pruneLocked(pluginDevNow())
	b.mu.Unlock()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("removed snapshot while worker active")
	}
	close(release)
	b.publishWorkers.Wait()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expired snapshot retained after worker completed")
	}
}
func TestPluginDevPublishHTTPDoesNotOfferConfirmationBypass(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	raw := map[string]any{}
	json.Unmarshal(devPublishJSON(t, in), &raw)
	for _, key := range []string{"yes", "remember"} {
		raw[key] = true
		w := devPublishHTTP(b, "POST", "/publish", "instance-secret", raw)
		if w.Code != 400 {
			t.Fatal("accepted bypass", key, w.Code)
		}
		delete(raw, key)
	}
	id := devCreate(t, b, in)
	if w := devPublishHTTP(b, "POST", "/publish/"+id+"/confirm", "instance-secret", nil); w.Code != 404 {
		t.Fatal("HTTP confirmation available")
	}
	if p.runs.Load() != 0 {
		t.Fatal("manual confirmation bypassed")
	}
	w := devPublishHTTP(b, "POST", "/publish/"+id+"/cancel", "instance-secret", nil)
	if w.Code != 200 || devStatus(t, b, id).Status != "canceled" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPluginDevPublishPreservesFixedCloudReasonsWithoutRemoteMessages(t *testing.T) {
	for _, reason := range []string{"artifacts_missing", "cover_invalid", "developer_suspended"} {
		t.Run(reason, func(t *testing.T) {
			p := &devTestPublisher{run: func(context.Context, marketpublish.Request, marketpublish.Preview, func(marketpublish.Progress)) (marketpublish.Result, error) {
				return marketpublish.Result{}, &marketpublish.APIError{Reason: reason, Message: "ba-user-token secret"}
			}}
			b := devPublishBridge(t, p)
			id := devCreate(t, b, devPublishInput(t))
			b.ConfirmPublish(id)
			s := devWait(t, b, id, "failed")
			if s.ErrorCode != reason || strings.Contains(s.ErrorMessage, "secret") {
				t.Fatalf("%+v", s)
			}
		})
	}
}

func TestPluginDevPublishCancelDuringCloseDoesNotRestartTimer(t *testing.T) {
	started := make(chan struct{})
	cancelSeen := make(chan struct{})
	release := make(chan struct{})
	p := &devTestPublisher{run: func(ctx context.Context, _ marketpublish.Request, _ marketpublish.Preview, _ func(marketpublish.Progress)) (marketpublish.Result, error) {
		close(started)
		<-ctx.Done()
		close(cancelSeen)
		<-release
		return marketpublish.Result{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.ConfirmPublish(id)
	<-started
	b.mu.Lock()
	op := b.publishes[id]
	b.mu.Unlock()
	closed := make(chan struct{})
	go func() { b.Close(); close(closed) }()
	<-cancelSeen
	b.CancelPublish(id)
	b.mu.Lock()
	restarted := op.timer != nil
	if op.timer != nil {
		op.timer.Stop()
	}
	b.mu.Unlock()
	close(release)
	<-closed
	if restarted {
		t.Fatal("terminal command restarted timer during bridge close")
	}
}

func TestPluginDevPublishAdmissionLimitsBeforeAnyCopy(t *testing.T) {
	for _, kind := range []string{"active", "records", "stages", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			b := devPublishBridge(t, &devTestPublisher{})
			in := devPublishInput(t)
			switch kind {
			case "active":
				for i := 0; i < 4; i++ {
					next := devPublishInput(t)
					next.PluginID = in.PluginID
					id := devCreate(t, b, next)
					b.mu.Lock()
					b.publishes[id].status.PluginID = string(rune('a'+i)) + ".plugin"
					b.mu.Unlock()
				}
			case "records":
				for i := 0; i < 8; i++ {
					id := devCreate(t, b, in)
					b.CancelPublish(id)
				}
			case "stages":
				b.mu.Lock()
				b.publishStages = 2
				b.mu.Unlock()
				defer func() { b.mu.Lock(); b.publishStages = 0; b.mu.Unlock() }()
			case "bytes":
				b.mu.Lock()
				b.publishReserved = pluginDevPublishMaxSnapshotBytes
				b.mu.Unlock()
				defer func() { b.mu.Lock(); b.publishReserved = 0; b.mu.Unlock() }()
			}
			// If admission reads/copies this path, validation returns package_invalid instead of the limit.
			os.WriteFile(in.Packages[0].Path, []byte("must not be read"), 0600)
			w := devPublishHTTP(b, "POST", "/publish", "instance-secret", in)
			if w.Code != 429 {
				t.Fatalf("limit did not precede file validation: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestPluginDevPublishDuplicatePrecedesCopy(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	in := devPublishInput(t)
	devCreate(t, b, in)
	os.WriteFile(in.Packages[0].Path, []byte("changed"), 0600)
	w := devPublishHTTP(b, "POST", "/publish", "instance-secret", in)
	if w.Code != 409 {
		t.Fatalf("duplicate copied before exclusion: %d %s", w.Code, w.Body.String())
	}
}
func TestPluginDevPublishTerminalReleasesSnapshotAndRoots(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	op := b.publishes[id]
	roots := op.roots
	dir := op.dirs[0]
	b.mu.Unlock()
	b.CancelPublish(id)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("idle terminal retained snapshot")
	}
	if _, err := roots.projectTree.Stat("."); err == nil {
		t.Fatal("project handle leaked")
	}
	if _, err := roots.releaseTree.Stat("."); err == nil {
		t.Fatal("release handle leaked")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.publishReserved != 0 || op.reserved != 0 {
		t.Fatal("snapshot budget leaked")
	}
	if len(b.publishes) != 1 {
		t.Fatal("public result must remain retained")
	}
}
func TestPluginDevPublishConfirmStageLimitIsRetryable(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	b.publishStages = 2
	b.mu.Unlock()
	err := b.ConfirmPublish(id)
	b.mu.Lock()
	b.publishStages = 0
	b.mu.Unlock()
	if err == nil || devStatus(t, b, id).Status != "pending_confirmation" {
		t.Fatal("confirm bypassed stage cap or consumed confirmation")
	}
	if err = b.ConfirmPublish(id); err != nil {
		t.Fatal(err)
	}
	devWait(t, b, id, "succeeded")
}

func TestPluginDevPublishDeclaredSizeCheckedBeforeSnapshotWrite(t *testing.T) {
	in := devPublishInput(t)
	canonical, err := canonicalPluginDevRoot(in.ReleaseDir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, size, e := inspectPublishFileAt(root, canonical, in.Packages[0].Path, pluginDevPublishMaxPackageBytes, ""); e != nil || size != in.Packages[0].Size {
		t.Fatalf("valid fixture rejected: size=%d err=%v", size, e)
	}
	dest := filepath.Join(t.TempDir(), "must-not-exist")
	_, _, err = inspectPublishFileAt(root, canonical, in.Packages[0].Path, pluginDevPublishMaxPackageBytes, dest, 1)
	if err == nil {
		t.Fatal("wrong declared size accepted")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("copied before checking declared size")
	}
}
func TestPluginDevPublishReservationBounds(t *testing.T) {
	in := devPublishInput(t)
	for _, size := range []int64{-1, 0, 1 << 62} {
		in.Packages[0].Size = size
		if _, err := publishSnapshotReservation(in); err == nil {
			t.Fatal("invalid size admitted", size)
		}
	}
	for i := range in.Packages {
		in.Packages[i].Size = 64 << 20
	}
	in.CoverPath = "cover.png"
	bytes, err := publishSnapshotReservation(in)
	if err != nil || bytes != 516<<20 {
		t.Fatal(bytes, err)
	}
}
func TestPluginDevPublishCancelRetainsReservationUntilWorkerExit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p := &devTestPublisher{run: func(ctx context.Context, _ marketpublish.Request, _ marketpublish.Preview, _ func(marketpublish.Progress)) (marketpublish.Result, error) {
		close(started)
		<-release
		return marketpublish.Result{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	op := b.publishes[id]
	roots := op.roots
	dir := op.dirs[0]
	budget := op.reserved
	b.mu.Unlock()
	b.ConfirmPublish(id)
	<-started
	b.CancelPublish(id)
	b.mu.Lock()
	retained := b.publishReserved == budget && op.reserved == budget
	b.mu.Unlock()
	if !retained {
		close(release)
		t.Fatal("released active worker budget")
	}
	if _, err := os.Stat(dir); err != nil {
		close(release)
		t.Fatal("deleted active worker snapshot")
	}
	close(release)
	b.publishWorkers.Wait()
	b.mu.Lock()
	leaked := b.publishReserved != 0 || b.publishStages != 0
	b.mu.Unlock()
	if leaked {
		t.Fatal("worker reservation leaked")
	}
	if _, err := roots.projectTree.Stat("."); err == nil {
		t.Fatal("worker root handle leaked")
	}
}
func TestPluginDevPublishFailedStageAndCloseReleaseAdmission(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	in := devPublishInput(t)
	in.Packages[0].SHA256 = strings.Repeat("a", 64)
	failedID := devCreate(t, b, in)
	if s := devStatus(t, b, failedID); s.Status != "failed" {
		t.Fatalf("%+v", s)
	}
	b.mu.Lock()
	leaked := len(b.publishes) != 1 || b.publishReserved != 0 || b.publishStages != 0
	b.mu.Unlock()
	if leaked {
		t.Fatal("failed initial stage leaked admission (terminal record may remain)")
	}

	id := devCreate(t, b, devPublishInput(t))
	b.mu.Lock()
	roots := b.publishes[id].roots
	b.mu.Unlock()
	b.Close()
	b.mu.Lock()
	leaked = len(b.publishes) != 0 || b.publishReserved != 0 || b.publishStages != 0
	b.mu.Unlock()
	if leaked {
		t.Fatal("Close leaked admission")
	}
	if _, err := roots.releaseTree.Stat("."); err == nil {
		t.Fatal("Close leaked pinned release handle")
	}
}

func TestPluginDevPublishAdmissionAndRootsSurviveCancelDuringInitialPlan(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p := &devTestPublisher{plan: func(ctx context.Context, _ marketpublish.Request) (marketpublish.Preview, error) {
		close(started)
		<-release
		return marketpublish.Preview{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- devPublishHTTP(b, "POST", "/publish", "instance-secret", in) }()
	<-started
	b.mu.Lock()
	var id string
	for key := range b.publishes {
		id = key
	}
	op := b.publishes[id]
	roots := op.roots
	reserved := op.reserved
	b.mu.Unlock()
	os.WriteFile(in.Packages[0].Path, []byte("not readable for duplicate"), 0600)
	duplicate := devPublishHTTP(b, "POST", "/publish", "instance-secret", in)
	b.CancelPublish(id)
	b.mu.Lock()
	held := b.publishReserved == reserved && reserved > 0 && op.busy && b.publishStages == 0
	b.mu.Unlock()
	_, rootErr := roots.projectTree.Stat(".")
	close(release)
	first := <-done
	b.publishWorkers.Wait()
	if duplicate.Code != 409 || !held || rootErr != nil || first.Code != 202 {
		t.Fatalf("duplicate=%d held=%v rootErr=%v first=%d", duplicate.Code, held, rootErr, first.Code)
	}
	b.mu.Lock()
	leaked := b.publishReserved != 0
	b.mu.Unlock()
	if leaked {
		t.Fatal("initial Plan cancellation leaked reservation")
	}
	if _, err := roots.projectTree.Stat("."); err == nil {
		t.Fatal("initial Plan cancellation leaked roots")
	}
}

func TestPluginDevPublishExpiryClosesPinnedRootsAndReservation(t *testing.T) {
	for _, login := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmation", true: "login"}[login], func(t *testing.T) {
			p := &devTestPublisher{}
			if login {
				p.plan = func(context.Context, marketpublish.Request) (marketpublish.Preview, error) {
					return marketpublish.Preview{}, publishError("not_signed_in")
				}
			}
			b := devPublishBridge(t, p)
			id := devCreate(t, b, devPublishInput(t))
			b.mu.Lock()
			op := b.publishes[id]
			roots := op.roots
			op.status.ExpiresAt = pluginDevNow().Add(-time.Second)
			b.pruneLocked(pluginDevNow())
			leaked := b.publishReserved != 0 || b.publishStages != 0
			b.mu.Unlock()
			if leaked {
				t.Fatal("expired request kept resource reservation")
			}
			if _, err := roots.projectTree.Stat("."); err == nil {
				t.Fatal("expired request kept project capability")
			}
			if _, err := roots.releaseTree.Stat("."); err == nil {
				t.Fatal("expired request kept release capability")
			}
		})
	}
}

func TestPluginDevPublishAsyncAdmissionBeforeSlowPlan(t *testing.T) {
	entered := make(chan struct{})
	p := &devTestPublisher{plan: func(ctx context.Context, _ marketpublish.Request) (marketpublish.Preview, error) {
		close(entered)
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
		return marketpublish.Preview{}, ctx.Err()
	}}
	b := devPublishBridge(t, p)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pluginDevBridgePrefix+"/publish" {
			b.handlePublish(w, r)
		} else {
			b.handlePublishStatus(w, r)
		}
	}))
	server.Config.WriteTimeout = 100 * time.Millisecond
	server.Start()
	defer server.Close()
	data, _ := json.Marshal(devPublishInput(t))
	req, _ := http.NewRequest("POST", server.URL+pluginDevBridgePrefix+"/publish", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer instance-secret")
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("admission must return before slow Plan:", err)
	}
	defer response.Body.Close()
	var created struct {
		ID string `json:"publishId"`
	}
	if json.NewDecoder(response.Body).Decode(&created) != nil || response.StatusCode != 202 || created.ID == "" {
		t.Fatal("missing stable ID")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("plan not started")
	}
	get, _ := http.NewRequest("GET", server.URL+pluginDevBridgePrefix+"/publish/"+created.ID, nil)
	get.Header.Set("Authorization", "Bearer instance-secret")
	polled, err := client.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	var status pluginDevPublishStatus
	err = json.NewDecoder(polled.Body).Decode(&status)
	polled.Body.Close()
	if err != nil || polled.StatusCode != 200 {
		t.Fatalf("first GET invalid: %d %v", polled.StatusCode, err)
	}
	if status.Status != "preparing" {
		t.Fatalf("first GET must be pollable preparing: %+v", status)
	}
	if w := devPublishHTTP(b, "GET", "/publish/"+created.ID, "instance-secret", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if b.ConfirmPublish(created.ID) == nil {
		t.Fatal("confirmed before plan")
	}
	if err = b.CancelPublish(created.ID); err != nil {
		t.Fatal(err)
	}
	b.publishWorkers.Wait()
	if p.runs.Load() != 0 || devStatus(t, b, created.ID).Status != "canceled" {
		t.Fatal("cancel during prepare ran publisher")
	}
}

func TestPluginDevPublishCloseBeforeStageStarts(t *testing.T) {
	b := devPublishBridge(t, &devTestPublisher{})
	in := devPublishInput(t)
	reservation, _ := publishSnapshotReservation(in)
	ctx, cancel := context.WithCancel(context.Background())
	op := &pluginDevPublishOperation{input: in, ctx: ctx, cancel: cancel, busy: true, initialized: true, reserved: reservation, status: pluginDevPublishStatus{ID: "prestage", Status: "preparing"}}
	b.mu.Lock()
	b.publishes[op.status.ID] = op
	b.publishReserved = reservation
	b.publishStages = 1
	b.publishWorkers.Add(1)
	b.mu.Unlock()
	closed := make(chan struct{})
	go func() { b.Close(); close(closed) }()
	<-ctx.Done() // Close wins before the worker has begun any root/snapshot work.
	var factories atomic.Int32
	b.preparePublish(op, func() marketpublish.Publisher { factories.Add(1); return &devTestPublisher{} })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close stuck before staging")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if factories.Load() != 0 || b.publishReserved != 0 || b.publishStages != 0 || len(b.publishes) != 0 || op.roots != nil || len(op.dirs) != 0 {
		t.Fatal("pre-stage Close leaked handles or ran Plan")
	}
}

func TestPluginDevPublishPollProjectionBudget(t *testing.T) {
	s := pluginDevPublishStatus{ID: strings.Repeat("a", 32), Status: "succeeded", Step: "done", Account: strings.Repeat("<", 65535), ReleaseNotes: strings.Repeat("<", 10000), MetadataChanges: []pluginDevPublishChange{{Field: "readme", Old: strings.Repeat("<", 65535), New: strings.Repeat("<", 65535)}}, Result: &pluginDevPublishResult{ReleaseID: 42, ReviewStatus: "pending", DeveloperURL: strings.Repeat("<", 2048)}}
	for _, target := range []pluginDevPublishTarget{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}} {
		s.Packages = append(s.Packages, pluginDevPublishPackageStatus{OS: target.OS, Arch: target.Arch, File: strings.Repeat("<", 255), Size: 64 << 20, SHA256: strings.Repeat("a", 64), State: "uploaded", Percent: 100})
		s.Result.Uploaded = append(s.Result.Uploaded, target)
	}
	projection := devPublishJSON(t, publishPollProjection(s))
	if len(projection) > 32<<10 || strings.Contains(string(projection), "metadataChanges") || strings.Contains(string(projection), "account") {
		t.Fatalf("HTTP projection budget: %d", len(projection))
	}
	full := devPublishJSON(t, s)
	if len(full) <= 256<<10 {
		t.Fatal("fixture must exceed CLI limit")
	}
	s.Account = "dev@example.com" // Native budget uses a legitimate bounded account.
	native := make([]pluginDevPublishStatus, 8)
	for i := range native {
		native[i] = s
	}
	if len(devPublishJSON(t, native)) > 8<<20 {
		t.Fatal("legal eight-record native list budget exceeded")
	}
}
