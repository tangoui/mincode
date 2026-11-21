//go:build darwin

package lsp

import (
	"os"
	"path/filepath"
)

// jdkJavaPatterns are the java executables of JDKs installed in the usual
// macOS places: Homebrew's keg-only openjdk formulae and the folders
// /usr/libexec/java_home lists (globbed instead of running java_home).
func jdkJavaPatterns() []string {
	patterns := []string{
		"/opt/homebrew/opt/openjdk*/bin/java",
		"/usr/local/opt/openjdk*/bin/java",
		"/Library/Java/JavaVirtualMachines/*/Contents/Home/bin/java",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		patterns = append(patterns, filepath.Join(home, "Library", "Java", "JavaVirtualMachines", "*", "Contents", "Home", "bin", "java"))
	}
	return patterns
}

// isJavaInstallerStub reports whether java is macOS's /usr/bin/java, an
// installer stub that may open the "install Java" dialog when run.
func isJavaInstallerStub(java, resolved string) bool {
	return filepath.Clean(java) == "/usr/bin/java" || resolved == "/usr/bin/java"
}
