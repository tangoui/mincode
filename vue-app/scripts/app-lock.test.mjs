import assert from "node:assert/strict";
import test from "node:test";
import {
  APP_LOCK_FREE_ATTEMPTS,
  APP_LOCK_MAX_BACKOFF_MS,
  APP_LOCK_MAX_PASSCODE_LENGTH,
  APP_LOCK_MIN_PASSCODE_LENGTH,
  createDefaultAppLockSettings,
  initialLockState,
  normalizeAppLockSettings,
  normalizeAutoLockMinutes,
  shouldAutoLock,
  unlockBackoffMs,
  validateAppLockPasscode,
} from "../src/domain/security/app-lock.ts";

const passcodeRecord = {
  algorithm: "pbkdf2-sha256",
  iterations: 600000,
  salt: "c2FsdA==",
  hash: "aGFzaA==",
};

test("defaults keep the lock disabled and unlocked", () => {
  const settings = createDefaultAppLockSettings();
  assert.equal(settings.enabled, false);
  assert.equal(settings.locked, false);
  assert.equal(settings.passcode, undefined);
});

test("enabled requires a usable passcode record", () => {
  const withoutPasscode = normalizeAppLockSettings({ enabled: true, locked: true });
  assert.equal(withoutPasscode.enabled, false);
  assert.equal(withoutPasscode.locked, false);

  const withBadRecord = normalizeAppLockSettings({
    enabled: true,
    passcode: { ...passcodeRecord, algorithm: "md5" },
  });
  assert.equal(withBadRecord.enabled, false);

  const valid = normalizeAppLockSettings({ enabled: true, passcode: passcodeRecord });
  assert.equal(valid.enabled, true);
  assert.deepEqual(valid.passcode, passcodeRecord);
});

test("auto lock minutes fall back to the default and stay in range", () => {
  assert.equal(normalizeAutoLockMinutes(0), 0);
  assert.equal(normalizeAutoLockMinutes(-5), 0);
  assert.equal(normalizeAutoLockMinutes(15), 15);
  assert.equal(normalizeAutoLockMinutes(10_000), 720);
  assert.equal(normalizeAutoLockMinutes("nope"), createDefaultAppLockSettings().autoLockMinutes);
});

test("a locked session stays locked across restarts", () => {
  assert.equal(
    initialLockState(
      normalizeAppLockSettings({ enabled: true, passcode: passcodeRecord, lockOnLaunch: false })
    ),
    false
  );
  assert.equal(
    initialLockState(
      normalizeAppLockSettings({
        enabled: true,
        passcode: passcodeRecord,
        lockOnLaunch: false,
        locked: true,
      })
    ),
    true
  );
  assert.equal(
    initialLockState(
      normalizeAppLockSettings({ enabled: true, passcode: passcodeRecord, lockOnLaunch: true })
    ),
    true
  );
  // 没有启用锁屏时绝不能因为残留状态把用户挡在门外。
  assert.equal(initialLockState(normalizeAppLockSettings({ locked: true })), false);
});

test("passcode validation reports the specific problem", () => {
  assert.equal(validateAppLockPasscode(""), "empty");
  assert.equal(validateAppLockPasscode("a".repeat(APP_LOCK_MIN_PASSCODE_LENGTH - 1)), "too-short");
  assert.equal(validateAppLockPasscode("a".repeat(APP_LOCK_MAX_PASSCODE_LENGTH + 1)), "too-long");
  assert.equal(validateAppLockPasscode("correct-horse", "correct-hors"), "mismatch");
  assert.equal(validateAppLockPasscode("correct-horse", "correct-horse"), null);
  assert.equal(validateAppLockPasscode("correct-horse"), null);
});

test("unlock backoff is free for the first attempts then grows to a cap", () => {
  for (let attempt = 0; attempt <= APP_LOCK_FREE_ATTEMPTS; attempt += 1) {
    assert.equal(unlockBackoffMs(attempt), 0);
  }
  assert.equal(unlockBackoffMs(APP_LOCK_FREE_ATTEMPTS + 1), 1000);
  assert.equal(unlockBackoffMs(APP_LOCK_FREE_ATTEMPTS + 2), 2000);
  assert.equal(unlockBackoffMs(APP_LOCK_FREE_ATTEMPTS + 30), APP_LOCK_MAX_BACKOFF_MS);
});

test("auto lock only fires for an idle, enabled, unlocked session", () => {
  const base = { enabled: true, locked: false, autoLockMinutes: 10, lastActivityAt: 0, now: 10 * 60_000 };
  assert.equal(shouldAutoLock(base), true);
  assert.equal(shouldAutoLock({ ...base, now: 10 * 60_000 - 1 }), false);
  assert.equal(shouldAutoLock({ ...base, autoLockMinutes: 0 }), false);
  assert.equal(shouldAutoLock({ ...base, enabled: false }), false);
  assert.equal(shouldAutoLock({ ...base, locked: true }), false);
});
