import assert from "node:assert/strict";
import test from "node:test";
import { messagesZh } from "../src/i18n/locales/zh.ts";
import { messagesEn } from "../src/i18n/locales/en.ts";

test("workspace resource and deletion prompts describe the retained features", () => {
  for (const messages of [messagesZh, messagesEn]) {
    for (const key of ["commandPalette.mode.resources", "tab.workspaceDeleteConfirm", "tab.workspacePermanentDeleteConfirm"]) {
      assert.ok(messages[key], key);
      assert.doesNotMatch(messages[key], /项目|浏览器|projects?|browser/i);
    }
  }
});
