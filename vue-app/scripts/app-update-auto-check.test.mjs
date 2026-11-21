import assert from "node:assert/strict";
import test from "node:test";
import {
  UPDATE_CHECK_INTERVAL_MS,
  isAutoCheckDue,
  normalizeUpdatePreferences,
  pendingUpdateVersion,
  shouldNotifyUpdate,
} from "../src/domain/update/auto-check.ts";

test("update preferences default to automatic checks and drop invalid fields", () => {
  assert.deepEqual(normalizeUpdatePreferences(undefined), {
    version: 1,
    autoCheck: true,
    skippedVersion: undefined,
    notifiedVersion: undefined,
    lastCheckedAt: undefined,
  });
  assert.deepEqual(
    normalizeUpdatePreferences({ autoCheck: false, skippedVersion: " 1.2.0 ", notifiedVersion: 3, lastCheckedAt: -5 }),
    { version: 1, autoCheck: false, skippedVersion: "1.2.0", notifiedVersion: undefined, lastCheckedAt: undefined },
  );
  assert.equal(normalizeUpdatePreferences([]).autoCheck, true);
  assert.equal(normalizeUpdatePreferences({ autoCheck: "no" }).autoCheck, true);
});

test("automatic checks run once per interval and never when disabled", () => {
  const now = 1_800_000_000_000;
  const prefs = normalizeUpdatePreferences({});
  assert.equal(isAutoCheckDue(prefs, now), true);
  assert.equal(isAutoCheckDue({ ...prefs, lastCheckedAt: now - 60_000 }, now), false);
  assert.equal(isAutoCheckDue({ ...prefs, lastCheckedAt: now - UPDATE_CHECK_INTERVAL_MS }, now), true);
  assert.equal(isAutoCheckDue({ ...prefs, lastCheckedAt: now + 3_600_000 }, now), true, "clock moved backwards");
  assert.equal(isAutoCheckDue({ ...prefs, autoCheck: false }, now), false);
});

test("a skipped version is not flagged while newer versions still are", () => {
  const prefs = normalizeUpdatePreferences({ skippedVersion: "1.2.0" });
  assert.equal(pendingUpdateVersion({ available: true, version: "1.2.0" }, prefs), undefined);
  assert.equal(pendingUpdateVersion({ available: true, version: "1.3.0" }, prefs), "1.3.0");
  assert.equal(pendingUpdateVersion({ available: false, version: "1.3.0" }, prefs), undefined);
  assert.equal(pendingUpdateVersion({ available: true, version: "" }, prefs), undefined);
  assert.equal(pendingUpdateVersion(undefined, prefs), undefined);
});

test("automatic checks notify only once per version", () => {
  const info = { available: true, version: "1.3.0" };
  assert.equal(shouldNotifyUpdate(info, normalizeUpdatePreferences({})), true);
  assert.equal(shouldNotifyUpdate(info, normalizeUpdatePreferences({ notifiedVersion: "1.3.0" })), false);
  assert.equal(shouldNotifyUpdate(info, normalizeUpdatePreferences({ notifiedVersion: "1.2.0" })), true);
  assert.equal(shouldNotifyUpdate(info, normalizeUpdatePreferences({ skippedVersion: "1.3.0" })), false);
});
