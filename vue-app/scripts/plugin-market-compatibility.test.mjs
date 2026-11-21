import test from "node:test";
import assert from "node:assert/strict";
import { hostRangeMessage, marketCompatibility } from "../src/domain/plugins/market-compatibility.ts";
import { describePluginInstallError } from "../src/domain/plugins/install-error.ts";

const entry = (fields) => ({ latest_published_version: "2.0.0", host_compatibility: "compatible", compatible_version: "2.0.0", ...fields });

test("market compatibility offers the newest release this MinCode can run", () => {
  assert.deepEqual(marketCompatibility(entry({})), { offered: "2.0.0", latestUnsupported: false, reason: "" });
  assert.deepEqual(marketCompatibility(entry({ host_compatibility: "host_too_old", compatible_version: "1.4.0" })),
    { offered: "1.4.0", latestUnsupported: true, reason: "host_too_old" });
  assert.deepEqual(marketCompatibility(entry({ host_compatibility: "host_too_new", compatible_version: null })),
    { offered: null, latestUnsupported: true, reason: "host_too_new" });
  assert.equal(marketCompatibility(entry({ host_compatibility: "", compatible_version: null })).offered, "2.0.0", "unrated entries keep offering the latest");
  assert.equal(marketCompatibility(entry({ latest_published_version: null, compatible_version: null })).offered, null);
});

test("host range picks the message for its bounds", () => {
  assert.equal(hostRangeMessage("0.2.0", "1.0.0").key, "plugins.hostRange.both");
  assert.equal(hostRangeMessage("0.2.0", "").key, "plugins.hostRange.min");
  assert.equal(hostRangeMessage("", "1.0.0").key, "plugins.hostRange.max");
  assert.equal(hostRangeMessage("", "").key, "plugins.hostRange.any");
});

test("installer host-version errors carry the required and running versions", () => {
  assert.deepEqual(describePluginInstallError("Error: market install: requires MinCode 0.3.0 or later (this is 0.1.0)"),
    { kind: "hostTooOld", version: "0.3.0", current: "0.1.0", detail: "requires MinCode 0.3.0 or later (this is 0.1.0)" });
  assert.equal(describePluginInstallError("market install: supports MinCode below 0.2.0 only (this is 0.2.0)").kind, "hostTooNew");
});
