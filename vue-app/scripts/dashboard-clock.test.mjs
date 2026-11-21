import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { formatDashboardClock, greetingKeyForHour, msUntilNextSecond } from "../src/domain/dashboard/clock.ts";

test("the greeting follows the hour of the live clock", () => {
  assert.equal(greetingKeyForHour(0), "dashboard.greetingEvening");
  assert.equal(greetingKeyForHour(5), "dashboard.greetingMorning");
  assert.equal(greetingKeyForHour(11), "dashboard.greetingMorning");
  assert.equal(greetingKeyForHour(12), "dashboard.greetingAfternoon");
  assert.equal(greetingKeyForHour(17), "dashboard.greetingAfternoon");
  assert.equal(greetingKeyForHour(18), "dashboard.greetingEvening");
  assert.equal(greetingKeyForHour(23), "dashboard.greetingEvening");
});

test("the clock ticks on second boundaries", () => {
  assert.equal(msUntilNextSecond(new Date(2026, 9, 22, 14, 3, 5, 0)), 1000);
  assert.equal(msUntilNextSecond(new Date(2026, 9, 22, 14, 3, 5, 250)), 750);
  assert.equal(msUntilNextSecond(new Date(2026, 9, 22, 14, 3, 5, 999)), 1);
});

test("time and date are formatted for the UI language", () => {
  const at = new Date(2026, 9, 22, 14, 3, 5);
  const zh = formatDashboardClock(at, "zh-CN");
  assert.equal(zh.time, "14:03:05");
  assert.match(zh.date, /2026年10月22日/);
  assert.match(zh.date, /星期四/);
  const en = formatDashboardClock(at, "en-US");
  assert.equal(en.time, "14:03:05");
  assert.match(en.date, /Thursday/);
  assert.match(en.date, /October 22, 2026/);
  assert.equal(zh.iso, "2026-10-22T14:03:05");
});

test("the dashboard shows the live clock and stops it when hidden", async () => {
  const view = await readFile(new URL("../src/views/DashboardView.vue", import.meta.url), "utf8");
  assert.match(view, /<time class="dashboard-clock" :datetime="clock\.iso">/);
  assert.match(view, /t\(greetingKeyForHour\(now\.value\.getHours\(\)\)\)/);
  assert.match(view, /onDeactivated\(\(\) => \{[\s\S]{0,120}stopClock\(\)/);
  assert.match(view, /onBeforeUnmount\(stopClock\)/);
});
