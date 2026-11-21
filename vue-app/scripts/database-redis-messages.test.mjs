import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Redis workspace toast messages go through i18n", async () => {
  const panel = await readFile(
    new URL("../src/components/database/RedisWorkspacePanel.vue", import.meta.url),
    "utf8"
  );
  const script = panel.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)?.[1];
  assert.ok(script);
  assert.match(script, /ElMessage\.success\(t\("database\.redis\.created"\)\)/);
  // 提示文案不能写死中文，否则英文界面会直接露出中文。
  for (const call of script.matchAll(/ElMessage\.\w+\(([^)]*)\)/g)) {
    assert.doesNotMatch(call[1], /[\u4e00-\u9fff]/, `hardcoded message: ${call[0]}`);
    assert.doesNotMatch(call[1], /^\s*["'`]/, `literal message: ${call[0]}`);
  }
});
