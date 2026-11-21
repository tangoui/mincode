import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const workspaceStyles = await readFile(
  new URL("../src/styles/workspaces/database/workspace.css", import.meta.url),
  "utf8",
);

test("keeps database explorer toolbar actions square when space is constrained", () => {
  const squareActionRule = workspaceStyles.match(
    /\.db-square-action\.el-button\s*\{(?<body>[\s\S]*?)\n\}/,
  );

  assert.ok(squareActionRule?.groups?.body);
  assert.match(squareActionRule.groups.body, /width:\s*28px;/);
  assert.match(squareActionRule.groups.body, /min-width:\s*28px;/);
  assert.match(squareActionRule.groups.body, /height:\s*28px;/);
  assert.match(squareActionRule.groups.body, /flex:\s*0\s+0\s+28px;/);
  assert.match(squareActionRule.groups.body, /padding:\s*0;/);
});
