//go:build !windows

package lsp

func decodeInstallOutput(output []byte) string {
	return string(output)
}
