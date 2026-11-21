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
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/marketpublish"
)

// Exercises the real Task12 HTTP handlers with a built CLI subprocess. Only
// this test calls the normal desktop-side Refresh/Confirm methods; neither the
// CLI nor an HTTP request can confirm. No cloud API or desktop runtime is used.
func TestPluginDevPublishCLIRealBridge(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "large-readme-success", true: "large-readme-canceled"}[canceled], func(t *testing.T) { testPluginDevPublishCLIRealBridge(t, canceled) })
	}
}
func testPluginDevPublishCLIRealBridge(t *testing.T, canceled bool) {
	cli := os.Getenv("MINCODE_TEST_PUBLISH_CLI")
	if cli == "" {
		t.Skip("set MINCODE_TEST_PUBLISH_CLI to the built CLI absolute path")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("CLI fixture path must be absolute")
	}
	var signedIn atomic.Bool
	publisher := &devTestPublisher{
		plan: func(context.Context, marketpublish.Request) (marketpublish.Preview, error) {
			if !signedIn.Load() {
				return marketpublish.Preview{}, &marketpublish.APIError{Reason: "not_signed_in"}
			}
			return marketpublish.Preview{Account: "dev@example.com", NewPlugin: true, MetadataChanges: []marketpublish.FieldChange{
				{Field: "readme", Old: strings.Repeat("<", 60000), New: strings.Repeat("界", 21845)},
				{Field: "name", Old: "Old", New: strings.Repeat("n", 120)},
				{Field: "description", Old: "Old", New: strings.Repeat("d", 2000)},
				{Field: "homepage", Old: "", New: "https://example.com/" + strings.Repeat("p", 1000)},
			}}, nil
		},
		run: func(ctx context.Context, req marketpublish.Request, preview marketpublish.Preview, progress func(marketpublish.Progress)) (marketpublish.Result, error) {
			progress(marketpublish.Progress{Step: marketpublish.StepUpload, Target: &req.Packages[0].Target, Sent: req.Packages[0].Size, Total: req.Packages[0].Size})
			return marketpublish.Result{ReleaseID: 42, ReviewStatus: "pending", DeveloperURL: "https://api.mincodeai.cn/marketplace?tab=account", Uploaded: []marketpublish.Target{req.Packages[0].Target}}, nil
		},
	}
	bridge := devPublishBridge(t, publisher)
	in := devPublishInput(t)
	// Valid source package metadata is required by CLI preflight, independently
	// of the bridge's narrower ZIP identity check.
	zipPath := writePluginTestZIP(t, []zipTestEntry{{name: "plugin.json", data: []byte(`{"id":"test.plugin","version":"1.0.0","apiVersion":2,"name":"Test Plugin","description":"A long new description","category":"other","runtime":{"type":"declarative"}}`)}, {name: "README.md", data: []byte(strings.Repeat("界", 21845))}})
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	packages := []map[string]any{}
	for _, pkg := range in.Packages {
		if err = os.WriteFile(pkg.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		packages = append(packages, map[string]any{"os": pkg.OS, "arch": pkg.Arch, "file": filepath.Base(pkg.Path), "size": len(data), "sha256": hex.EncodeToString(hash[:])})
	}
	manifest, _ := json.Marshal(map[string]any{"format": 1, "id": in.PluginID, "version": in.Version, "packages": packages})
	if err = os.WriteFile(filepath.Join(in.ReleaseDir, "release.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	events := make(chan pluginDevPublishStatus, 8)
	var creates, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		switch {
		case r.Method == "POST" && r.URL.Path == pluginDevBridgePrefix+"/publish":
			creates.Add(1)
			bridge.handlePublish(recorder, r)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/cancel"):
			bridge.handlePublishStatus(recorder, r)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, pluginDevBridgePrefix+"/publish/"):
			gets.Add(1)
			bridge.handlePublishStatus(recorder, r)
		default:
			t.Errorf("unexpected CLI route %s %s", r.Method, r.URL.Path)
			recorder.WriteHeader(404)
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
		if r.Method == "GET" && recorder.Code == 200 {
			var status pluginDevPublishStatus
			if json.Unmarshal(recorder.Body.Bytes(), &status) == nil {
				if recorder.Body.Len() > 32<<10 {
					t.Errorf("poll projection exceeds budget: %d", recorder.Body.Len())
				}
				if strings.Contains(recorder.Body.String(), "metadataChanges") || strings.Contains(recorder.Body.String(), "releaseNotes") {
					t.Error("poll exposed confirmation text")
				}
				select {
				case events <- status:
				default:
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	discoveryPath := filepath.Join(t.TempDir(), "plugin-development.json")
	discovery, _ := json.Marshal(map[string]any{"protocolVersion": 1, "endpoint": server.URL + pluginDevBridgePrefix, "instanceId": "real-bridge-test", "instanceToken": bridge.instanceToken, "expiresAt": time.Now().Add(time.Hour)})
	if err = os.WriteFile(discoveryPath, discovery, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "plugin", "publish", in.ProjectRoot, "--dir", in.ReleaseDir)
	command.Env = append(os.Environ(), "MINCODE_PLUGIN_DEV_DISCOVERY="+discoveryPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	sawLogin, sawConfirmation := false, false
	for {
		select {
		case status := <-events:
			switch status.Status {
			case "awaiting_login":
				if !sawLogin {
					if publisher.runs.Load() != 0 {
						t.Error("publisher ran before login/confirmation")
					}
					sawLogin = true
					signedIn.Store(true)
					if err = bridge.RefreshPublish(status.ID); err != nil {
						t.Error(err)
					}
				}
			case "pending_confirmation":
				if !sawConfirmation {
					if !sawLogin || publisher.runs.Load() != 0 {
						t.Error("confirmation boundary skipped")
					}
					sawConfirmation = true
					native := devStatus(t, bridge, status.ID)
					if len(native.MetadataChanges) < 1 || len(native.MetadataChanges[0].Old.(string)) != 60000 {
						t.Error("native confirmation lost full README")
					}
					if canceled {
						err = bridge.CancelPublish(status.ID)
					} else {
						err = bridge.ConfirmPublish(status.ID)
					}
					if err != nil {
						t.Error(err)
					}
				}
			}
		case err = <-done:
			if canceled {
				if err == nil || !strings.Contains(output.String(), "上架已取消") || !sawConfirmation || publisher.runs.Load() != 0 {
					t.Fatalf("cancel failed: %v %s", err, output.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("CLI failed: %v\n%s", err, output.String())
			}
			if creates.Load() != 1 || gets.Load() < 3 || !sawLogin || !sawConfirmation || publisher.runs.Load() != 1 {
				t.Fatalf("creates=%d gets=%d login=%v confirmation=%v runs=%d", creates.Load(), gets.Load(), sawLogin, sawConfirmation, publisher.runs.Load())
			}
			for _, want := range []string{"请先在 MinCode 中登录云账号", "请在 MinCode 窗口中确认提交", "上传 darwin/arm64 100%", "版本草稿编号：42", "审核状态：待审核", "https://api.mincodeai.cn/marketplace?tab=account"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in %s", want, output.String())
				}
			}
			return
		}
	}
}
