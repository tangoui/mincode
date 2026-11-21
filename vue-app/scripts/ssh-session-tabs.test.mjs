import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const styles = await readFile(
  new URL("../src/styles/workspaces/ssh/session-pane.css", import.meta.url),
  "utf8",
);
const view = await readFile(
  new URL("../src/views/SshWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("SSH session close buttons override card-tab hiding without requiring hover or selection", () => {
  const selector = ".ssh-session-tabs.el-tabs--card > .el-tabs__header .el-tabs__item.is-closable .is-icon-close";
  const start = styles.indexOf(`${selector} {`);
  assert.ok(start >= 0);
  const body = styles.slice(start + selector.length + 2, styles.indexOf("}", start));
  assert.match(body, /width:\s*18px;/);
  assert.match(body, /height:\s*18px;/);
  assert.match(styles, /\.ssh-session-tabs \.el-tabs__item \.is-icon-close:hover\s*\{/);
});

test("SSH overview stays non-closable while session tabs retain their remove handler", () => {
  assert.match(view, /@tab-remove="removeSshSessionTab"/);
  const overview = view.match(/<el-tab-pane :name="SSH_OVERVIEW_TAB_ID">[\s\S]*?<\/el-tab-pane>/);
  assert.ok(overview);
  assert.doesNotMatch(overview[0], /\bclosable\b/);
  assert.match(view, /v-for="session in workspaceSessionTabs"[\s\S]*?\sclosable\s/);
});

test("SSH session tabs fill the strip height without a blank band above them", () => {
  const rule = (selector) => {
    const start = styles.indexOf(`${selector} {`);
    assert.ok(start >= 0, selector);
    return styles.slice(start + selector.length + 2, styles.indexOf("}", start));
  };
  const strip = rule(".ssh-session-tabs");
  assert.match(strip, /--el-tabs-header-height:\s*30px;/);
  assert.match(strip, /flex:\s*0 0 30px;/);
  assert.match(rule(".ssh-session-tabs.el-tabs--card > .el-tabs__header"), /height:\s*30px;/);
  // No top padding: the tabs start at the top of the strip.
  assert.match(rule(".ssh-session-tabs .el-tabs__nav-wrap"), /padding:\s*0 6px;/);
  // 29px tabs + the 1px strip border fill the 30px strip, like the database object tabs.
  assert.match(rule(".ssh-session-tabs.el-tabs--card > .el-tabs__header .el-tabs__item"), /height:\s*29px;/);
});
