//go:build !(e2e && darwin)

package e2esmoke

// KeepRenderingWhenCovered is a no-op outside macOS smoke builds: release
// builds never change WebKit's occlusion behaviour.
func KeepRenderingWhenCovered(uintptr) error { return nil }
