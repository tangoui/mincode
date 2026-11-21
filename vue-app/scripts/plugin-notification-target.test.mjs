import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import {
  desktopNotificationTarget,
  parseDesktopPluginTarget,
  readNotificationTarget,
} from "../src/domain/notifications/target.ts";

test("plugin notification targets round-trip through the native desktop value", () => {
  const target = { kind: "plugin", pluginId: "focus-timer", viewId: "main" };
  const native = desktopNotificationTarget(target);
  assert.equal(native.kind, "plugin");
  assert.deepEqual(parseDesktopPluginTarget(native.value), target);
  assert.deepEqual(parseDesktopPluginTarget(JSON.stringify({ pluginId: "focus-timer" })), { kind: "plugin", pluginId: "focus-timer" });
  for (const bad of ["", "not json", JSON.stringify({ pluginId: "../x" }), JSON.stringify({ pluginId: "p", viewId: "a b" })]) {
    assert.equal(parseDesktopPluginTarget(bad), undefined, bad);
  }
});

test("persisted plugin targets are restored only when valid", () => {
  assert.deepEqual(readNotificationTarget({ kind: "plugin", pluginId: "feed-reader" }), { kind: "plugin", pluginId: "feed-reader" });
  assert.equal(readNotificationTarget({ kind: "plugin", pluginId: "" }), undefined);
  assert.equal(readNotificationTarget({ kind: "plugin", pluginId: "x", viewId: 3 }), undefined);
});

test("plugin notifications reach the notification center and can force a system banner", () => {
  const app = readFileSync(new URL("../src/App.vue", import.meta.url), "utf8");
  assert.match(app, /listenAppEvent<PluginNotifyEvent>\("plugin:notify"/);
  assert.match(app, /source: "plugin"/);
  assert.match(app, /\{ system: event\.system === true \}/);
  const service = readFileSync(new URL("../src/services/notifications.ts", import.meta.url), "utf8");
  assert.match(service, /options\.system \|\| document\.visibilityState !== "visible"/);
});
