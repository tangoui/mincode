import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

async function source(path) {
  return readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
}

function rule(css, selector) {
  const start = css.indexOf(`${selector} {`);
  assert.ok(start >= 0, `Missing rule: ${selector}`);
  return css.slice(start + selector.length + 2, css.indexOf("}", start));
}

test("shared card tabs reserve close-button width without hover or selection", async () => {
  const css = await source("styles/shared/workspace-tabs.css");
  const selector = ".app-workspace-tabs.el-tabs--card > .el-tabs__header .el-tabs__item.is-active.is-closable .is-icon-close";
  // The active and hovered states must be covered: Element Plus forces width: 14px there.
  for (const state of [".is-closable .is-icon-close", ".is-closable:hover .is-icon-close", ".is-active.is-closable .is-icon-close"]) {
    assert.ok(css.includes(`.app-workspace-tabs.el-tabs--card > .el-tabs__header .el-tabs__item${state}`), state);
  }
  const body = rule(css, selector);
  assert.match(body, /width:\s*var\(--app-workspace-tabs-close-size, 18px\);/);
  // Square box: Element Plus gives the icon a fixed 14px height and 50% radius, so a
  // width-only override turned the hover background into an ellipse.
  assert.match(body, /height:\s*var\(--app-workspace-tabs-close-size, 18px\);/);
  assert.match(body, /border-radius:\s*50%;/);
  const database = await source("styles/workspaces/database/workspace.css");
  assert.match(rule(database, ".db-connection-tabs__list .el-tabs__item.is-closable"), /--app-workspace-tabs-close-size:\s*16px;/);
  for (const view of ["DatabaseWorkspaceView", "ProjectWorkspaceView", "SshWorkspaceView"]) {
    assert.match(await source(`views/${view}.vue`), /<AppWorkspaceTabs\b/);
  }
});

for (const [path, selector] of [
  ["styles/workspaces/http/workspace.css", ".http-request-tab__close.el-button"],
  ["styles/workspaces/sessions/workspace.css", ".sessions-live-tab__close"],
  ["views/DashboardView.vue", ".dashboard-session-row__close.el-button"],
]) {
  test(`${selector} is visible at rest`, async () => {
    const body = rule(await source(path), selector);
    assert.match(body, /opacity:\s*1;/);
    assert.doesNotMatch(body, /display:\s*none|visibility:\s*hidden/);
  });
}
