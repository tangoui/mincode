import assert from "node:assert/strict";
import test from "node:test";
import {
  collectTreeViews,
  parseTreeViewResponse,
  treeViewKey,
  treeViewPreviewSummary,
  treeViewTitle,
  TREE_VIEW_LIMITS,
  validateTreeViewEntry,
} from "../src/domain/plugins/tree-views.ts";

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

const view = { id: "deps", title: "Dependencies", titleLocalized: { zh: "依赖" }, action: "tree.deps" };

test("treeViews: re-validates entries, skips inactive plugins and reports issues", () => {
  assert.equal(validateTreeViewEntry({ ...view, id: "Bad" }).ok, false);
  assert.equal(validateTreeViewEntry({ ...view, title: "a\u202eb" }).ok, false);
  assert.equal(validateTreeViewEntry({ ...view, action: "" }).ok, false);
  const { views, issues } = collectTreeViews([
    contribution("z.tools", [view, view, { id: "x" }]),
    contribution("a.tools", [{ ...view, id: "todo", title: "TODO" }]),
    contribution("b.tools", [view], { activeInWorkspace: false }),
    contribution("c.tools", [view], { runtime: "declarative" }),
  ]);
  assert.deepEqual(views.map(treeViewKey), ["a.tools/todo", "z.tools/deps"]);
  assert.equal(views[1].pluginName, "z.tools name");
  assert.deepEqual(issues.map((issue) => [issue.pluginId, issue.entry]), [["z.tools", 1], ["z.tools", 2]]);
  assert.equal(treeViewTitle(views[1], ["zh"]), "依赖");
  assert.equal(treeViewTitle(views[1], ["en"]), "Dependencies");
});

test("treeViews: parses items, drops invalid or duplicate ones and unsafe targets", () => {
  const response = parseTreeViewResponse({
    items: [
      { id: "a", label: "Alpha", collapsible: true, icon: "package", target: { type: "file", path: "src/a.ts", line: 3 } },
      { id: "a", label: "Duplicate" },
      { id: "b", label: "Beta", target: { type: "url", url: "https://example.com/x" } },
      { id: "c", label: "Gamma", target: { type: "url", url: "javascript:alert(1)" } },
      { id: "d", label: "Delta", target: { type: "file", path: "../etc/passwd" } },
      { id: "e", label: "Eps", icon: "nope" },
      { id: "f", label: "a\u0000b" },
      { id: "g", label: "Cmd", target: { type: "command", command: "mincode.open", args: { x: 1 } }, tooltip: "line1\r\nline2" },
    ],
    dropped: 2,
  }, 0, true);
  assert.deepEqual(response.items.map((item) => item.id), ["a", "b", "g"]);
  assert.equal(response.dropped, 7);
  assert.equal(response.truncated, false);
  assert.deepEqual(response.items[0].target, { type: "file", path: "src/a.ts", line: 3 });
  assert.equal(response.items[0].collapsible, true);
  assert.equal(response.items[2].tooltip, "line1\nline2");
});

test("treeViews: file targets need a project and the deepest level is never expandable", () => {
  const items = [{ id: "a", label: "A", collapsible: true, target: { type: "file", path: "a.ts" } }, { id: "b", label: "B", collapsible: true }];
  const noProject = parseTreeViewResponse({ items }, 0, false);
  assert.deepEqual(noProject.items.map((item) => item.id), ["b"]);
  const deepest = parseTreeViewResponse({ items }, TREE_VIEW_LIMITS.depthMax - 1, true);
  assert.deepEqual(deepest.items.map((item) => item.collapsible), [false, false]);
  const many = parseTreeViewResponse({ items: Array.from({ length: TREE_VIEW_LIMITS.itemsMax + 1 }, (_, i) => ({ id: `i${i}`, label: `I${i}` })) }, 0, true);
  assert.equal(many.items.length, TREE_VIEW_LIMITS.itemsMax);
  assert.equal(many.truncated, true);
  assert.deepEqual(parseTreeViewResponse(null, 0, true), { items: [], dropped: 0, truncated: false });
});

test("treeViews: install preview lists titles, falling back to ids", () => {
  assert.deepEqual(treeViewPreviewSummary({ treeViews: [view, { id: "raw", title: "x\u202e" }, { id: "BAD" }] }), ["Dependencies", "raw"]);
  assert.deepEqual(treeViewPreviewSummary({}), []);
});
