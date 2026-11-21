package lsp

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

const jdtTestURI = "jdt://contents/java.base/java.lang/Iterable.class?=demo/%5C/jrt-fs.jar%60java.base=/%3Cjava.lang(Iterable.class"

func jdtContents(t *testing.T, root, uri string) (string, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"root": root, "uri": uri})
	if err != nil {
		t.Fatal(err)
	}
	out, err := core.Dispatch("project_lsp_jdt_contents", string(raw))
	if err != nil {
		return "", err
	}
	var text string
	if err := json.Unmarshal([]byte(out), &text); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return text, nil
}

func TestJdtContentsReadsClassFileThroughBuiltinJdtls(t *testing.T) {
	root := t.TempDir()
	if _, err := jdtContents(t, root, jdtTestURI); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("not running: %v", err)
	}
	startFakeJdtls(t, root)
	text, err := jdtContents(t, root, jdtTestURI)
	if err != nil || text != "// source of "+jdtTestURI {
		t.Fatalf("contents = %q (%v)", text, err)
	}
	if _, err := jdtContents(t, root, "jdt://contents/java.base/x/Missing.class"); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("missing class: %v", err)
	}
}

// Only jdt:// class-file URIs are read: the command must not become a way to
// read arbitrary files or send other requests to jdtls.
func TestJdtContentsRejectsOtherURIs(t *testing.T) {
	root := t.TempDir()
	startFakeJdtls(t, root)
	for _, uri := range []string{"", "file:///etc/passwd", "/etc/passwd", "JDT://contents/x", "jdt:/contents/x", "jdt://" + strings.Repeat("a", 8193), "jdt://contents/x\nnext"} {
		if _, err := jdtContents(t, root, uri); err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
			t.Fatalf("uri %q: err = %v", uri, err)
		}
	}
	if _, err := core.Dispatch("project_lsp_jdt_contents", `{"root":"","uri":"jdt://contents/x"}`); err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("empty root: %v", err)
	}
}
