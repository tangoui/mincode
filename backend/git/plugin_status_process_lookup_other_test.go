//go:build !windows

package git

import "os"

// Unix FindProcess constructs a handle even when a PID is gone.
var statusProcessGoneError = os.ErrProcessDone
