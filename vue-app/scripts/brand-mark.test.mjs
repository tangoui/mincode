import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const surfaces = [
  "views/DashboardView.vue",
  "views/SettingsWorkspaceView.vue",
  "components/layout/AppTabBar.vue",
  "components/layout/AppLockScreen.vue",
];

for (const surface of surfaces) {
  test(`${surface} renders the shared MinCode brand asset`, async () => {
    const source = await readFile(new URL(`../src/${surface}`, import.meta.url), "utf8");
    assert.match(source, /import MinCodeMark from "@\/assets\/brand\/mincode-mark\.svg"/);
    assert.match(source, /<img\b[^>]*:src="MinCodeMark"/);
    assert.doesNotMatch(source, />\s*(?:M|MC)\s*<\//);
  });
}

test("dashboard logo does not repaint the shared mark with a placeholder background", async () => {
  const source = await readFile(new URL("../src/views/DashboardView.vue", import.meta.url), "utf8");
  assert.match(source, /<img class="dashboard-logo" :src="MinCodeMark" alt="" aria-hidden="true"/);
  const rule = source.match(/\.dashboard-logo\s*\{([^}]+)\}/)?.[1];
  assert.ok(rule);
  assert.match(rule, /width:\s*52px/);
  assert.match(rule, /height:\s*52px/);
  assert.match(rule, /object-fit:\s*contain/);
  assert.doesNotMatch(rule, /background|font-size|letter-spacing/);
});
