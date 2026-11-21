//go:build !windows

package plugin

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var benchEpoch = time.Now()

// benchNow is the benchmark clock: the monotonic clock, which has
// nanosecond resolution outside Windows.
func benchNow() time.Duration { return time.Since(benchEpoch) }

// processRSSKiB returns a process's resident set size from ps.
func processRSSKiB(pid int) int {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return value
}
