import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

test("settings navigation omits cloud, external MCP, app lock and backup sections", () => {
  const settings = readFileSync(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  const declarations = settings.match(/const categoryList[^\n]*= \[\n([\s\S]*?)\n\];/)?.[1] ?? "";
  const ids = [...declarations.matchAll(/\{ id: "([^"]+)"/g)].map((match) => match[1]);

  assert.ok(ids.includes("about"), "reads the settings navigation with its update entry");
  assert.doesNotMatch(settings, /ConfigSyncDialog|EnvironmentPackDialog/);
  assert.deepEqual(ids.filter((id) => ["cloud", "external-mcp", "security"].includes(id)), []);
});
