//go:build !darwin && !windows

package lsp

// jdkJavaPatterns are the java executables of JDKs installed in the usual
// Linux places.
func jdkJavaPatterns() []string {
	return []string{"/usr/lib/jvm/*/bin/java", "/home/linuxbrew/.linuxbrew/opt/openjdk*/bin/java"}
}

func isJavaInstallerStub(_, _ string) bool { return false }
