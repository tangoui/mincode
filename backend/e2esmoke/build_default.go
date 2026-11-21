//go:build !e2e

package e2esmoke

// buildEnabled: release and development builds never honour the harness environment.
const buildEnabled = false
