import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile(new URL("../src/styles/workspaces/terminal/context-menu.css", import.meta.url), "utf8");

function rule(selector) {
  const start = css.indexOf(`${selector} {`);
  assert.ok(start >= 0, selector);
  return css.slice(start + selector.length + 2, css.indexOf("}", start));
}

test("paste preview uses a thin themed scrollbar without hiding scrolling", () => {
  assert.match(rule(".terminal-paste-dialog .el-textarea__inner"), /scrollbar-width: thin;/);
  assert.match(rule(".terminal-paste-dialog .el-textarea__inner"), /scrollbar-color: var\(--app-border-strong\) transparent;/);
  const webkit = rule(".terminal-paste-dialog .el-textarea__inner::-webkit-scrollbar");
  assert.match(webkit, /width: 6px;/);
  assert.match(webkit, /height: 6px;/);
  assert.match(rule(".terminal-paste-dialog .el-textarea__inner::-webkit-scrollbar-thumb"), /background: var\(--app-border-strong\);/);
  assert.match(rule(".terminal-paste-dialog .el-textarea__inner::-webkit-scrollbar-thumb:hover"), /background: var\(--app-text-muted\);/);
});
