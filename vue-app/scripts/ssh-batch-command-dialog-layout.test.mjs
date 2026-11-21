import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const dialogStyles = await readFile(
  new URL("../src/styles/workspaces/ssh/batch-dialog.css", import.meta.url),
  "utf8",
);
const workspaceView = await readFile(
  new URL("../src/views/SshWorkspaceView.vue", import.meta.url),
  "utf8",
);

function ruleFor(selector) {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = dialogStyles.match(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `Missing CSS rule for ${selector}`);
  return match[1];
}

test("SSH batch command dialog keeps its chrome fixed and scrolls overflowing body content", () => {
  const dialog = ruleFor(".ssh-batch-command-dialog.el-dialog");
  assert.match(dialog, /display:\s*flex;/);
  assert.match(dialog, /height:\s*auto;/);
  assert.match(dialog, /max-height:\s*min\(760px,\s*calc\(100vh\s*-\s*32px\)\);/);
  assert.match(dialog, /flex-direction:\s*column;/);

  const body = ruleFor(".ssh-batch-command-dialog .el-dialog__body");
  assert.match(body, /display:\s*flex;/);
  assert.match(body, /min-height:\s*0;/);
  assert.match(body, /flex:\s*1;/);
  assert.match(body, /flex-direction:\s*column;/);
  assert.match(body, /overflow:\s*hidden;/);

  assert.match(
    workspaceView,
    /<el-scrollbar class="ssh-batch-command-dialog__scrollbar">\s*<div class="ssh-batch-command">/,
  );
  const scrollbar = ruleFor(".ssh-batch-command-dialog__scrollbar");
  assert.match(scrollbar, /min-height:\s*0;/);
  assert.match(scrollbar, /flex:\s*1;/);
  assert.match(
    ruleFor(".ssh-batch-command-dialog__scrollbar .el-scrollbar__wrap"),
    /overflow-x:\s*hidden;/,
  );

  assert.match(ruleFor(".ssh-batch-command-dialog .el-dialog__header"), /flex:\s*none;/);
  assert.match(ruleFor(".ssh-batch-command-dialog .el-dialog__footer"), /flex:\s*none;/);
});
