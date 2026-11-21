import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { SCHEDULE_LIMITS, resolveSchedules, scheduleIntervalParts, schedulePreviewSummary } from "../src/domain/plugins/schedules.ts";

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));

test("schedule limits mirror the contract", () => {
  const spec = contract.points.schedules;
  assert.equal(spec.status, "enabled");
  assert.deepEqual(spec.kinds, ["process"]);
  assert.equal(SCHEDULE_LIMITS.maxEntries, spec.maxEntries);
  for (const key of ["titleMaxRunes", "localesMax", "intervalMinMinutes", "intervalMaxMinutes"]) assert.equal(SCHEDULE_LIMITS[key], spec.limits[key], key);
});

test("schedules are revalidated and listed per process plugin", () => {
  const contribution = (pluginId, entries, runtime = "process") => ({ pluginId, pluginName: pluginId, version: "1.0.0", source: "local", runtime, activeInWorkspace: true, point: { entries } });
  const schedules = resolveSchedules([
    contribution("cert-watch", [
      { id: "certs", title: "Check certificates", titleLocalized: { zh: "检查证书" }, action: "check", intervalMinutes: 60 },
      { id: "fast", title: "Too fast", action: "check", intervalMinutes: 5 },
      { id: "Bad Id", title: "Bad", action: "check", intervalMinutes: 60 },
    ]),
    contribution("keymaps", [{ id: "x", title: "X", action: "a", intervalMinutes: 60 }], "declarative"),
  ]);
  assert.deepEqual(schedules.map((item) => item.key), ["cert-watch/certs"]);
  assert.equal(schedules[0].titleLocalized.zh, "检查证书");
  assert.deepEqual(schedulePreviewSummary({ schedules: [{ id: "ci", title: "CI", titleLocalized: { zh: "流水线" }, action: "a", intervalMinutes: 30 }, { id: "bad" }] }), [{ title: "CI", titleLocalized: { zh: "流水线" }, intervalMinutes: 30 }]);
  assert.deepEqual(scheduleIntervalParts(120), [2, "hours"]);
  assert.deepEqual(scheduleIntervalParts(45), [45, "minutes"]);
});
