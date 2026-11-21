//go:build windows

package lsp

import (
	"os"
	"path/filepath"
)

// jdkJavaPatterns are the java executables of JDKs installed under the
// Program Files folders of the common JDK vendors.
func jdkJavaPatterns() []string {
	var patterns []string
	seen := map[string]bool{}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		for _, vendor := range []string{"Java", "Eclipse Adoptium", "Microsoft", "Zulu", "Amazon Corretto"} {
			patterns = append(patterns, filepath.Join(base, vendor, "*", "bin", "java.exe"))
		}
	}
	return patterns
}

func isJavaInstallerStub(_, _ string) bool { return false }
