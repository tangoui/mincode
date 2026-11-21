import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const styles = await readFile(
  new URL("../src/styles/workspaces/settings/dialog.css", import.meta.url),
  "utf8",
);

test("长规则列表和正文预览各自限制纵向溢出", () => {
  assert.match(
    styles,
    /\.settings-rule-list\s*\{(?=[^}]*max-height:)(?=[^}]*overflow-y:\s*auto;)[^}]*\}/s,
  );
  assert.match(
    styles,
    /\.settings-rule-list \.settings-rule-card__content\s*\{(?=[^}]*max-height:)(?=[^}]*overflow-y:\s*auto;)[^}]*\}/s,
  );
});
