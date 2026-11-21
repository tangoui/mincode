//go:build windows

package proctree

import "time"

// Terminate is not supported on Windows (List never returns processes there).
func Terminate([]Process, time.Duration) {}
