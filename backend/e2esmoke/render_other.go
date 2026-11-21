//go:build !(e2e && darwin)

package e2esmoke

import "unsafe"

// KeepRenderingWhenCovered is a no-op outside macOS smoke builds: release
// builds never change WebKit's occlusion behaviour.
func KeepRenderingWhenCovered(unsafe.Pointer) error { return nil }
