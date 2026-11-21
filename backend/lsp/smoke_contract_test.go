package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmokeClientContract(t *testing.T) {
	var caps struct {
		General struct {
			PositionEncodings []string `json:"positionEncodings"`
		} `json:"general"`
		Workspace struct {
			Configuration bool `json:"configuration"`
			Watcher       struct {
				Dynamic bool `json:"dynamicRegistration"`
			} `json:"didChangeWatchedFiles"`
		} `json:"workspace"`
		TextDocument struct {
			Symbol struct {
				Hierarchical bool `json:"hierarchicalDocumentSymbolSupport"`
			} `json:"documentSymbol"`
			Sync struct {
				DidSave  bool `json:"didSave"`
				WillSave bool `json:"willSave"`
			} `json:"synchronization"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(smokeClientCapabilities(), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Workspace.Configuration || caps.Workspace.Watcher.Dynamic || !caps.TextDocument.Symbol.Hierarchical || !caps.TextDocument.Sync.DidSave || caps.TextDocument.Sync.WillSave || len(caps.General.PositionEncodings) != 1 || caps.General.PositionEncodings[0] != "utf-16" {
		t.Fatal("smoke contract drifted")
	}
	if got := string(smokeConfigurationReply([]json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)})); got != "[null,null]" {
		t.Fatal(got)
	}
	if got := string(smokeConfigurationReply(nil)); got != "[]" {
		t.Fatal(got)
	}
}

func TestInitializeFailureCategoriesDoNotLeakParams(t *testing.T) {
	for _, tc := range []struct{ params, sdk, category string }{
		{`{}`, "", "typescript-sdk-unavailable"},
		{`["SECRET"]`, "/private/SECRET", "initialize-params-invalid"},
		{`{"initializationOptions":"SECRET"}`, "/private/SECRET", "initialize-options-invalid"},
		{`{"initializationOptions":{"typescript":"SECRET"}}`, "/private/SECRET", "initialize-typescript-options-invalid"},
		{`{"initializationOptions":{"vue":"SECRET"}}`, "/private/SECRET", "initialize-vue-options-invalid"},
	} {
		s := &session{definition: serverDefinition{ID: "vue-ls"}}
		if _, err := s.prepareSDKInitialize(json.RawMessage(tc.params), tc.sdk); err == nil {
			t.Fatal("invalid initialization accepted")
		}
		logs := s.logs()
		if !strings.Contains(logs, "category="+tc.category) || strings.Contains(logs, "SECRET") || strings.Contains(logs, "/private/") {
			t.Fatalf("unsafe/missing category: %s", logs)
		}
	}
}

func TestVueVersionRejectionHasSafeTrace(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "vue-language-server")
	if err := os.WriteFile(executable, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	// 2.2.12 and 3.x are supported; any other release is rejected before start.
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@vue/language-server","version":"2.2.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	lookPathMu.Lock()
	old, exists := lookPathCache["vue-language-server"]
	lookPathMu.Unlock()
	// Use a fresh lookup through a real PATH fixture; preserve any caller cache.
	t.Setenv("PATH", root)
	invalidateLookPathCache("vue-language-server")
	t.Cleanup(func() {
		lookPathMu.Lock()
		if exists {
			lookPathCache["vue-language-server"] = old
		} else {
			delete(lookPathCache, "vue-language-server")
		}
		lookPathMu.Unlock()
	})
	s := &session{root: root, definition: serverDefinition{ID: "vue-ls", Command: "vue-language-server"}}
	if err := s.start(); err == nil {
		t.Fatal("incompatible version accepted")
	}
	if logs := s.logs(); !strings.Contains(logs, "category=vue-version-rejected") || strings.Contains(logs, root) {
		t.Fatalf("unsafe/missing category: %s", logs)
	}
}
