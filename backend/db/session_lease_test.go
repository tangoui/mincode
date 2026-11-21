package db

import (
	"testing"
	"time"
)

func TestSessionRegistryLeaseProtectsActiveSessionAndTouchesOnRelease(t *testing.T) {
	r := newSessionRegistry()
	r.put("active", &dbSession{})
	r.put("idle", &dbSession{})
	old := time.Now().Add(-time.Hour)
	r.mu.Lock()
	r.timestamps["active"] = old
	r.timestamps["idle"] = old
	r.mu.Unlock()

	_, release, ok := r.acquire("active")
	if !ok {
		t.Fatal("expected active session lease")
	}
	removed := r.removeIdle(30 * 60)
	if len(removed) != 1 || removed[0] != "idle" {
		t.Fatalf("removed while active: %#v", removed)
	}

	release()
	r.mu.Lock()
	touched := r.timestamps["active"]
	activeOperations := r.activeOperations["active"]
	r.mu.Unlock()
	if !touched.After(old) || activeOperations != 0 {
		t.Fatalf("release did not touch/decrement: touched=%s active=%d", touched, activeOperations)
	}
	if removed := r.removeIdle(30 * 60); len(removed) != 0 {
		t.Fatalf("freshly released session removed: %#v", removed)
	}
}
