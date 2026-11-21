package misc

import (
	"os"
	"testing"
)

func TestValidateKillTargetPid(t *testing.T) {
	self := uint32(os.Getpid())
	for _, pid := range []uint32{0, 1, 1 << 31, ^uint32(0), self} {
		if err := validateKillTargetPid(pid); err == nil {
			t.Fatalf("pid %d should be rejected", pid)
		}
	}
	allowed := uint32(12345)
	if allowed == self {
		allowed++
	}
	if err := validateKillTargetPid(allowed); err != nil {
		t.Fatalf("pid %d should be allowed: %v", allowed, err)
	}
}
