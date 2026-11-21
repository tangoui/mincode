import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const panelStyles = await readFile(
  new URL("../src/styles/workspaces/terminal/tools.css", import.meta.url),
  "utf8"
);

function lastRuleBody(selector) {
  const marker = `${selector} {`;
  const start = panelStyles.lastIndexOf(marker);
  if (start < 0) return undefined;
  const bodyStart = start + marker.length;
  const end = panelStyles.indexOf("\n}", bodyStart);
  return end < 0 ? undefined : panelStyles.slice(bodyStart, end);
}

test("SFTP panel scrollbars participate in the column flex layout", () => {
  const actions = lastRuleBody(".ssh-sftp-panel__actions");
  const list = lastRuleBody(".ssh-sftp-panel__list");

  assert.ok(actions);
  assert.match(actions, /height:\s*auto;/);
  assert.ok(list);
  assert.match(list, /height:\s*auto;/);
  assert.match(list, /min-height:\s*0;/);
  assert.match(list, /flex:\s*1;/);
});

test("SFTP path input relies on the Element Plus focus ring", () => {
  assert.doesNotMatch(panelStyles, /\.ssh-sftp-panel input:focus-visible/);
  assert.match(panelStyles, /\.ssh-sftp-panel button:focus-visible\s*\{/);
});
