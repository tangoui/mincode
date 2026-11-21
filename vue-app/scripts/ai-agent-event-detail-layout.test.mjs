import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const styles = await readFile(
  new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url),
  "utf8",
);

function rule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return styles.match(new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`))?.[1] ?? "";
}

test("tool result scrollbars size to content instead of stretching the event row", () => {
  const resultScrollbar = rule(
    ".ai-agent-event-detail__scroll.el-scrollbar",
  );

  assert.match(resultScrollbar, /height:\s*auto/);
  assert.doesNotMatch(resultScrollbar, /height:\s*100%/);
});
