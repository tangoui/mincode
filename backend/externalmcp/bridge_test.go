package externalmcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientExecutableCommand(t *testing.T) {
	encoded, err := command("external_mcp_client_executable", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable != current || !filepath.IsAbs(result.Executable) {
		t.Fatal("client command must use the running executable's absolute path")
	}
	if info, err := os.Stat(result.Executable); err != nil || !info.Mode().IsRegular() {
		t.Fatal("client command must point to an existing regular file")
	}
	if !IsHelper([]string{"--external-local-mcp"}) {
		t.Fatal("client configuration flag must start the helper")
	}
}

func TestTransientExecutable(t *testing.T) {
	cases := []struct {
		goos, path string
		want       bool
	}{
		{"darwin", "/Volumes/MinCode 7/MinCode.app/Contents/MacOS/mincode", true},
		{"darwin", "/private/var/folders/x/T/AppTranslocation/ABC/d/MinCode.app/Contents/MacOS/mincode", true},
		{"darwin", "/Applications/MinCode.app/Contents/MacOS/mincode", false},
		{"linux", "/Volumes/data/mincode", false},
	}
	for _, c := range cases {
		if got := transientExecutable(c.goos, c.path); got != c.want {
			t.Errorf("transientExecutable(%q, %q) = %v, want %v", c.goos, c.path, got, c.want)
		}
	}
}

func TestHandshakeAuthAndScope(t *testing.T) {
	original := dataDir
	dir := t.TempDir()
	dataDir = func() string { return dir }
	t.Cleanup(func() { Shutdown(); dataDir = original })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "example.go"), []byte("secret content"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"projects": []project{{Name: "Sample", Root: root}}})
	if _, err := command("external_mcp_enable", string(raw)); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"workspace.search_files\",\"arguments\":{\"query\":\"example\"}}}\n{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"shell.execute\"}}\n"
	if err := RunStdio(strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"protocolVersion", "workspace.list_projects", "example.go", "tool not allowed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	for _, secret := range []string{root, "secret content", c.Token} {
		if strings.Contains(text, secret) {
			t.Fatal("sensitive data leaked")
		}
	}
	req, _ := http.NewRequest(http.MethodPost, c.URL, strings.NewReader(`{"name":"workspace.list_projects"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, c.URL, strings.NewReader(`{"name":"shell.execute"}`))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("denied: %d", resp.StatusCode)
	}
	Shutdown()
	if _, err := readConfig(); err == nil {
		t.Fatal("disabled helper connected")
	}
}

func TestEmptyScopeAndUnsafePaths(t *testing.T) {
	original := dataDir
	dir := t.TempDir()
	dataDir = func() string { return dir }
	t.Cleanup(func() { Shutdown(); dataDir = original })
	if _, err := validateProjects([]project{{Root: "relative"}}); err == nil {
		t.Fatal("relative root accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProjects([]project{{Root: link}}); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := command("external_mcp_enable", `{"projects":[]}`); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, c.URL, strings.NewReader(`{"name":"workspace.search_files","arguments":{"query":"x"}}`))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result []any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result) != 0 {
		t.Fatalf("empty scope: %v %v", result, err)
	}
	Shutdown()
	path, err := rendezvousPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := command("external_mcp_enable", `{"projects":[]}`); err == nil {
		t.Fatal("symlink rendezvous accepted")
	}
}
