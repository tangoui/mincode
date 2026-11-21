//go:build !darwin && !linux

package proctree

import "errors"

var errUnsupported = errors.New("process listing is not supported on this platform")

// Supported reports whether List and Footprint work on this platform.
func Supported() bool { return false }

// List is not supported on this platform.
func List() ([]Process, error) { return nil, errUnsupported }

// Footprint is not supported on this platform.
func Footprint(int) uint64 { return 0 }

// TTYOf is not supported on this platform.
func TTYOf(string) int64 { return NoTTY }
