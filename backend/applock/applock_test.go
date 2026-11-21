package applock

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/crypto"
	"mincode/app/backend/internal/applockpolicy"

	_ "modernc.org/sqlite"
)

type emitted struct {
	mu     sync.Mutex
	states []State
}

func (e *emitted) last() (State, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.states) == 0 {
		return State{}, 0
	}
	return e.states[len(e.states)-1], len(e.states)
}

// setup 准备隔离的 app_kv、可控时钟与事件捕获，并重置进程内状态。
func setup(t *testing.T, initial any) (*sql.DB, *emitted, *time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/test.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE app_kv (key TEXT PRIMARY KEY NOT NULL, value BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if initial != nil {
		raw, _ := json.Marshal(initial)
		if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)`, applockpolicy.StorageKey, raw); err != nil {
			t.Fatal(err)
		}
	}
	clock := time.Unix(1_700_000_000, 0)
	events := &emitted{}
	origDB, origNow, origState := openDB, now, state
	openDB = func() (*sql.DB, error) { return db, nil }
	now = func() time.Time { return clock }
	state = &manager{}
	core.SetEmitter(func(name string, data any) {
		if name != StateEvent {
			return
		}
		events.mu.Lock()
		events.states = append(events.states, data.(State))
		events.mu.Unlock()
	})
	t.Cleanup(func() {
		openDB, now, state = origDB, origNow, origState
		core.SetEmitter(nil)
		_ = db.Close()
	})
	return db, events, &clock
}

func storedRecord(t *testing.T, db *sql.DB) map[string]any {
	t.Helper()
	var raw []byte
	if err := db.QueryRow("SELECT value FROM app_kv WHERE key = ?", applockpolicy.StorageKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func call[T any](t *testing.T, h func(string, string) (string, error), args any) T {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := h("", string(raw))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return v
}

func enable(t *testing.T, passcode string) AttemptResult {
	t.Helper()
	res := call[AttemptResult](t, setPasscodeHandler, map[string]string{"passcode": passcode})
	if !res.OK || !res.State.Enabled {
		t.Fatalf("enable failed: %+v", res)
	}
	return res
}

func TestUnlockUsesBackendRecordOnly(t *testing.T) {
	db, _, _ := setup(t, nil)
	enable(t, "first-pass")
	call[State](t, lockHandler, map[string]any{})

	// 旧接口允许调用方“自带记录”；新接口只接受口令，附带的伪造记录字段会被忽略。
	forged, err := crypto.HashPasscode("attacker")
	if err != nil {
		t.Fatal(err)
	}
	res := call[AttemptResult](t, unlockHandler, map[string]any{
		"passcode": "attacker", "algorithm": forged.Algorithm, "iterations": forged.Iterations,
		"salt": forged.Salt, "hash": forged.Hash,
	})
	if res.OK || !res.State.Locked {
		t.Fatalf("forged record must not unlock: %+v", res)
	}
	res = call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "first-pass"})
	if !res.OK || res.State.Locked {
		t.Fatalf("correct passcode must unlock: %+v", res)
	}
	if storedRecord(t, db)["locked"] != false {
		t.Fatal("unlock must be persisted")
	}
}

func TestChangedPasscodeInvalidatesOldOneEverywhere(t *testing.T) {
	db, events, _ := setup(t, nil)
	enable(t, "old-pass")
	changed := call[AttemptResult](t, setPasscodeHandler, map[string]string{"currentPasscode": "old-pass", "passcode": "new-pass"})
	if !changed.OK {
		t.Fatalf("change failed: %+v", changed)
	}
	locked := call[State](t, lockHandler, map[string]any{})
	if !locked.Locked || locked.Revision <= changed.State.Revision {
		t.Fatalf("lock must bump revision: %+v after %+v", locked, changed.State)
	}
	if res := call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "old-pass"}); res.OK {
		t.Fatal("old passcode must be rejected after a change")
	}
	if res := call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "new-pass"}); !res.OK {
		t.Fatal("new passcode must unlock")
	}
	// 持久化的始终是后端最新的校验记录。
	stored := storedRecord(t, db)
	verifier := stored["passcode"].(map[string]any)
	rec := crypto.PasscodeRecord{Algorithm: verifier["algorithm"].(string), Iterations: int(verifier["iterations"].(float64)), Salt: verifier["salt"].(string), Hash: verifier["hash"].(string)}
	if ok, _ := crypto.VerifyPasscode(rec, "new-pass"); !ok {
		t.Fatal("persisted verifier must match the new passcode")
	}
	if last, n := events.last(); n < 4 || last.Revision != uint64(stored["revision"].(float64)) {
		t.Fatalf("every change must be broadcast with the persisted revision: last=%+v n=%d stored=%v", last, n, stored["revision"])
	}
}

func TestChangeAndDisableRequireCurrentPasscode(t *testing.T) {
	_, _, _ = setup(t, nil)
	enable(t, "secret-1")
	if res := call[AttemptResult](t, setPasscodeHandler, map[string]string{"currentPasscode": "wrong", "passcode": "hijack"}); res.OK {
		t.Fatal("changing the passcode must require the current one")
	}
	if res := call[AttemptResult](t, setPasscodeHandler, map[string]string{"passcode": "hijack"}); res.OK {
		t.Fatal("enabling over an existing passcode must require the current one")
	}
	if res := call[AttemptResult](t, disableHandler, map[string]string{"currentPasscode": "wrong"}); res.OK || !res.State.Enabled {
		t.Fatal("disable must require the current passcode")
	}
	res := call[AttemptResult](t, disableHandler, map[string]string{"currentPasscode": "secret-1"})
	if !res.OK || res.State.Enabled || res.State.Locked {
		t.Fatalf("disable failed: %+v", res)
	}
}

func TestFailedAttemptsBackOffAcrossCallers(t *testing.T) {
	_, _, clock := setup(t, nil)
	enable(t, "right-pass")
	call[State](t, lockHandler, map[string]any{})
	var res AttemptResult
	for i := 0; i < freeAttempts+1; i++ {
		res = call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "nope"})
	}
	if res.OK || res.FailedAttempts != freeAttempts+1 || res.RetryAfterMs <= 0 {
		t.Fatalf("expected backoff after free attempts: %+v", res)
	}
	// 退避期内即使口令正确也不放行（计数在后端，换窗口重试不会重置）。
	if blocked := call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "right-pass"}); blocked.OK || blocked.RetryAfterMs <= 0 {
		t.Fatalf("backoff must apply to every caller: %+v", blocked)
	}
	*clock = clock.Add(time.Duration(res.RetryAfterMs) * time.Millisecond)
	if ok := call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "right-pass"}); !ok.OK || ok.FailedAttempts != 0 {
		t.Fatalf("unlock after backoff must succeed and reset: %+v", ok)
	}
}

func TestLegacyRecordMigratesAndLaunchLocks(t *testing.T) {
	legacy, err := crypto.HashPasscode("legacy-pass")
	if err != nil {
		t.Fatal(err)
	}
	db, _, _ := setup(t, map[string]any{
		"enabled": true, "passcode": legacy, "autoLockMinutes": 15, "lockOnLaunch": true, "locked": false,
	})
	s := call[State](t, stateHandler, map[string]any{})
	if !s.Enabled || !s.Locked || s.AutoLockMinutes != 15 || !s.LockOnLaunch {
		t.Fatalf("legacy record must load and lock on launch: %+v", s)
	}
	if storedRecord(t, db)["locked"] != true {
		t.Fatal("launch lock must be persisted so a force-quit cannot bypass it")
	}
	if res := call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "legacy-pass"}); !res.OK {
		t.Fatal("legacy passcode must keep working")
	}
}

func TestCorruptRecordDoesNotLockUserOut(t *testing.T) {
	_, _, _ = setup(t, map[string]any{"enabled": true, "locked": true, "passcode": map[string]any{"algorithm": "md5"}})
	s := call[State](t, stateHandler, map[string]any{})
	if s.Enabled || s.Locked {
		t.Fatalf("unusable verifier must disable the lock: %+v", s)
	}
}

func TestPreferencesAndPublicStateCarryNoVerifier(t *testing.T) {
	_, _, _ = setup(t, nil)
	enable(t, "pref-pass")
	s := call[State](t, setPreferencesHandler, map[string]any{"autoLockMinutes": 10_000, "lockOnLaunch": false})
	if s.AutoLockMinutes != maxAutoLockMinutes || s.LockOnLaunch {
		t.Fatalf("preferences not applied: %+v", s)
	}
	raw, _ := stateHandler("", "{}")
	for _, leaked := range []string{"passcode", "salt", "hash"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("public state must not expose %q: %s", leaked, raw)
		}
	}
}

func TestPasscodeLengthIsValidated(t *testing.T) {
	_, _, _ = setup(t, nil)
	if _, err := setPasscodeHandler("", `{"passcode":"abc"}`); err == nil {
		t.Fatal("too-short passcode must be rejected")
	}
	if _, err := setPasscodeHandler("", `{"passcode":"`+strings.Repeat("a", maxPasscodeLen+1)+`"}`); err == nil {
		t.Fatal("too-long passcode must be rejected")
	}
}

func TestNormalizeAutoLockMinutesMatchesFrontend(t *testing.T) {
	cases := map[string]int{`0`: 0, `-5`: 0, `15`: 15, `10000`: 720, `"nope"`: defaultAutoLockMinutes, `"15"`: 15, `null`: 0, `14.5`: 15}
	for in, want := range cases {
		if got := normalizeAutoLockMinutes(json.RawMessage(in)); got != want {
			t.Errorf("normalizeAutoLockMinutes(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestLockedAndStateObservers(t *testing.T) {
	setup(t, nil)
	var mu sync.Mutex
	var seen []bool
	OnStateChange(func(s State) {
		mu.Lock()
		seen = append(seen, s.Locked)
		mu.Unlock()
		// Observers run without applock locks held, so they may read the state.
		if Locked() != s.Locked {
			t.Errorf("Locked() = %v inside observer for %v", Locked(), s.Locked)
		}
	})
	t.Cleanup(resetStateObservers)
	if Locked() {
		t.Fatal("locked without a passcode")
	}
	enable(t, "first-pass")
	call[State](t, lockHandler, map[string]any{})
	if !Locked() {
		t.Fatal("Locked() false after lock")
	}
	call[AttemptResult](t, unlockHandler, map[string]string{"passcode": "first-pass"})
	if Locked() {
		t.Fatal("Locked() true after unlock")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] || !seen[1] || seen[2] {
		t.Fatalf("observer saw %v", seen)
	}
}

func TestLockedFailsClosedWhenUnreadable(t *testing.T) {
	setup(t, nil)
	openDB = func() (*sql.DB, error) { return nil, errors.New("storage unavailable") }
	if !Locked() {
		t.Fatal("unreadable lock record must count as locked")
	}
}
