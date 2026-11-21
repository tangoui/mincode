//go:build !windows

package plugin

func retryablePluginRenameError(error) bool { return false }
