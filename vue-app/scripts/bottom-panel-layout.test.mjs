import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile(new URL("../src/styles/shared/bottom-panel.css", import.meta.url), "utf8");

function rule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `missing rule ${selector}`);
  return match[1];
}

test("bottom panel tab header fits its 32px strip instead of the 40px Element Plus default", () => {
  assert.match(rule(".app-bottom-panel__header"), /height:\s*32px;/);
  assert.match(rule(".app-bottom-panel__tabs > .el-tabs__header"), /height:\s*28px;/);
  assert.match(rule(".app-bottom-panel__tabs > .el-tabs__header .el-tabs__item"), /height:\s*28px;/);
});

test("collapsed bottom panel header fills the splitter size including the top border", () => {
  const collapsed = rule(".app-bottom-panel.is-collapsed .app-bottom-panel__header");
  assert.match(collapsed, /height:\s*auto;/);
  assert.match(collapsed, /min-height:\s*0;/);
  assert.match(collapsed, /flex:\s*1;/);
});
