//go:build !darwin || !cgo

package filewatch

// NativeSource reports whether this platform has a native change source.
// Without one every change is found by rescanning the whole root.
const NativeSource = false

// startNativeWatch has no native source on this platform yet; the watcher
// falls back to bounded periodic rescans of the project root.
func startNativeWatch(string, watchChanged) (watchSource, error) {
	return nil, nil
}
