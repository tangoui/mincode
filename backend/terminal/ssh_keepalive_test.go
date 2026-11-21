package terminal

import (
	"testing"
	"time"
)

func TestSSHKeepAliveFailureWindow(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, tc := range []struct {
		name              string
		misses, maxMisses uint32
		since             time.Time
		elapsed           time.Duration
		want              bool
	}{
		{"legacy ninety seconds", 3, 3, start, 90 * time.Second, false},
		{"before thirty minutes", 3, 3, start, 30*time.Minute - time.Nanosecond, false},
		{"thirty minutes", 3, 3, start, 30 * time.Minute, true},
		{"after thirty minutes", 10, 10, start, 31 * time.Minute, true},
		{"miss threshold still required", 2, 3, start, time.Hour, false},
		{"successful probe resets window", 3, 3, time.Time{}, time.Hour, false},
		{"new failure starts new window", 3, 3, start.Add(time.Hour), time.Hour + time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sshKeepAliveShouldClose(tc.misses, tc.maxMisses, tc.since, start.Add(tc.elapsed)); got != tc.want {
				t.Fatalf("should close = %v, want %v", got, tc.want)
			}
		})
	}
}
