import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { URL } from "node:url";

const [splitPaneStyles, splitPaneComponent] = await Promise.all([
  readFile(new URL("../src/styles/shared/split-pane.css", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ui/AppSplitPane.vue", import.meta.url), "utf8"),
]);

function ruleBody(selector) {
  const match = splitPaneStyles.match(
    new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`)
  );
  return match?.[1];
}

test("splitter collapse controls do not overlap the drag handle", () => {
  const horizontalStart = ruleBody(
    ".app-split-pane .el-splitter-bar__horizontal-collapse-icon-start"
  );
  const horizontalEnd = ruleBody(
    ".app-split-pane .el-splitter-bar__horizontal-collapse-icon-end"
  );
  const verticalStart = ruleBody(
    ".app-split-pane .el-splitter-bar__vertical-collapse-icon-start"
  );
  const verticalEnd = ruleBody(
    ".app-split-pane .el-splitter-bar__vertical-collapse-icon-end"
  );

  assert.match(horizontalStart ?? "", /left:\s*-16px;/);
  assert.match(horizontalEnd ?? "", /left:\s*16px;/);
  assert.match(verticalStart ?? "", /top:\s*-16px;/);
  assert.match(verticalEnd ?? "", /top:\s*16px;/);
});

test("splitter collapse state does not depend on stale emitted panel sizes", () => {
  const handler = splitPaneComponent.match(
    /function handleCollapse[\s\S]*?\n}\n\nfunction handleCollapseKeydown/
  )?.[0] ?? "";

  assert.match(handler, /const nextCollapsed = !props\.collapsed;/);
  assert.doesNotMatch(handler, /sideSize|valueFromSizes\(sizes\)/);
});

test("splitter panel refresh is opt-in to avoid remounting unrelated sidebars", () => {
  assert.match(splitPaneComponent, /refreshPanelOnCollapse\?: boolean;/);
  assert.match(
    splitPaneComponent,
    /:key="[\s\S]*?props\.refreshPanelOnCollapse[\s\S]*?'first-panel-collapsed'[\s\S]*?'first-panel-expanded'[\s\S]*?'first-panel-stable'[\s\S]*?"/
  );
});

test("splitter drag handles stay clear of the panels' edge scrollbars", () => {
  // Element Plus sizes the dragger inline (16px on a 0-width bar), 8px into each
  // panel, which covers el-scrollbar's bar (2-8px from the edge).
  const horizontal = ruleBody(".app-split-pane .el-splitter-bar__dragger-horizontal");
  const vertical = ruleBody(".app-split-pane .el-splitter-bar__dragger-vertical");
  assert.match(horizontal ?? "", /width:\s*4px\s*!important/);
  assert.match(vertical ?? "", /height:\s*4px\s*!important/);
  // Terminal split groups have a 6px gutter; their handle matches it.
  assert.match(ruleBody(".app-split-group.app-split-pane--horizontal .el-splitter-bar__dragger-horizontal") ?? "", /width:\s*6px\s*!important/);
  assert.match(ruleBody(".app-split-group.app-split-pane--vertical .el-splitter-bar__dragger-vertical") ?? "", /height:\s*6px\s*!important/);
});
