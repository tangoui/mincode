//go:build !(e2e && darwin && cgo)

package browser

// Production and development builds: no native panel hooks. File choosers and
// save panels always wait for the user (see e2e_hooks_e2e.go).

func e2eNativePanelHooksCompiled() bool { return false }
