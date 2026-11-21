import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { cachedWorkspaceViewNames } from "../src/domain/workspace/view-retention.ts";

const tab = (view) => ({ id: view, workspaceId: "w", view, titleKey: "" });

test("a closed workspace view is not kept cached: only views with an open tab (and the dashboard) stay alive", () => {
  assert.deepEqual(cachedWorkspaceViewNames([]), ["DashboardView"]);
  assert.deepEqual(cachedWorkspaceViewNames([tab("terminal"), tab("database"), tab("terminal")]), ["DashboardView", "TerminalWorkspaceView", "DatabaseWorkspaceView"]);
  assert.deepEqual(cachedWorkspaceViewNames([tab("sessions"), tab("plugin-view"), tab("settings")]), ["DashboardView", "SessionsWorkspaceView"]);
});

test("App.vue drives KeepAlive from open tabs instead of a fixed list", async () => {
  const app = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
  assert.match(app, /<KeepAlive\s+:include="cachedWorkspaceViews"/);
  assert.doesNotMatch(app, /include="DashboardView,ProjectWorkspaceView/);
});
