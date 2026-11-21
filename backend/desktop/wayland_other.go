//go:build !linux

package desktop

// DetectWayland is always false outside Linux.
func DetectWayland() bool { return false }
