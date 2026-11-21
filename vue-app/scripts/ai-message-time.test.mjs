import assert from "node:assert/strict";
import test from "node:test";
import {
  formatAiMessageFullTime,
  formatAiMessageShortTime,
  normalizeAiMessageTimestamp,
} from "../src/domain/ai/message-time.ts";

const timestamp = Date.UTC(2026, 7, 10, 9, 8, 7);
const locale = "en-CA";

test("normalizes only valid positive AI message timestamps", () => {
  assert.equal(normalizeAiMessageTimestamp(timestamp), timestamp);
  assert.equal(normalizeAiMessageTimestamp(0), undefined);
  assert.equal(normalizeAiMessageTimestamp(Number.NaN), undefined);
  assert.equal(normalizeAiMessageTimestamp("2026-08-10"), undefined);
});

test("formats compact and full timestamps for message metadata", () => {
  const sameDay = new Date(Date.UTC(2026, 7, 10, 12, 0, 0));
  const nextDay = new Date(Date.UTC(2026, 7, 11, 12, 0, 0));
  const expectedShortSameDay = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(new Date(timestamp));
  const expectedShortOtherDay = new Intl.DateTimeFormat(locale, {
    month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
  }).format(new Date(timestamp));
  const expectedFull = new Intl.DateTimeFormat(locale, {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
  }).format(new Date(timestamp));

  assert.equal(formatAiMessageShortTime(timestamp, sameDay, locale), expectedShortSameDay);
  assert.equal(formatAiMessageShortTime(timestamp, nextDay, locale), expectedShortOtherDay);
  assert.equal(formatAiMessageFullTime(timestamp, locale), expectedFull);
  assert.equal(formatAiMessageShortTime(undefined, sameDay, locale), "");
  assert.equal(formatAiMessageFullTime(undefined, locale), "");
});
