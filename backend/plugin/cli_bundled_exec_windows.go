package plugin

import (
	"fmt"
	"os"
)

// RunBundledCLI is the AppImage CLI hand-off; Windows puts the CLI on PATH
// directly, so the mode is never used there.
func RunBundledCLI(_ []string) int {
	fmt.Fprintln(os.Stderr, "mincode: --mincode-cli is not supported on Windows; run bin\\mincode.exe from the MinCode install directory")
	return 1
}
