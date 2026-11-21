import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { messagesZh } from "../src/i18n/locales/zh.ts";
import { messagesEn } from "../src/i18n/locales/en.ts";

test("settings navigation omits cloud, external MCP, app lock and backup sections", () => {
  const settings = readFileSync(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
  const declarations = settings.match(/const categoryList[^\n]*= \[\n([\s\S]*?)\n\];/)?.[1] ?? "";
  const ids = [...declarations.matchAll(/\{ id: "([^"]+)"/g)].map((match) => match[1]);

  assert.ok(ids.includes("about"), "reads the settings navigation with its update entry");
  assert.doesNotMatch(settings, /ConfigSyncDialog|EnvironmentPackDialog/);
  assert.deepEqual(ids.filter((id) => ["cloud", "external-mcp", "security"].includes(id)), []);
});

test("configuration portability explains JSON and optional credentials", () => {
  for (const messages of [messagesZh, messagesEn]) {
    assert.match(messages["settings.data.exportHint"], /JSON/);
    assert.match(messages["settings.data.exportHint"], /密码|password/i);
    assert.match(messages["settings.data.importHint"], /重启|restart/i);
    assert.doesNotMatch(messages["settings.data.exportHint"] + messages["settings.data.importHint"], /项目|插件|projects?|plugins?/i);
  }
});
