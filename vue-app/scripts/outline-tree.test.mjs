import assert from "node:assert/strict";
import test from "node:test";
import {
  buildOutlineTree,
  countOutlineNodes,
  filterOutlineTree,
  outlineNodeAtLine,
  symbolKindName,
} from "../src/domain/projects/outline-tree.ts";

const range = (start, end = start, column = 1) => ({ startLineNumber: start, startColumn: column, endLineNumber: end });

const symbols = [
  {
    name: "helper",
    kind: 11,
    range: range(20, 25),
  },
  {
    name: "Widget",
    detail: "class",
    kind: 4,
    range: range(1, 15),
    selectionRange: range(1, 1, 7),
    children: [
      { name: "render", kind: 5, range: range(8, 12) },
      { name: "constructor", kind: 8, range: range(2, 5) },
      { name: "render", kind: 5, range: range(13, 14) },
    ],
  },
];

test("builds a position-sorted tree with stable unique ids", () => {
  const tree = buildOutlineTree(symbols);
  assert.deepEqual(
    tree.map((node) => node.name),
    ["Widget", "helper"],
  );
  const widget = tree[0];
  assert.equal(widget.kindName, "class");
  assert.equal(widget.column, 7);
  assert.equal(widget.endLine, 15);
  assert.deepEqual(
    widget.children.map((node) => [node.name, node.line]),
    [
      ["constructor", 2],
      ["render", 8],
      ["render", 13],
    ],
  );
  const ids = widget.children.map((node) => node.id);
  assert.equal(new Set(ids).size, 3);
  assert.equal(countOutlineNodes(tree), 5);
  assert.deepEqual(buildOutlineTree(undefined), []);
});

test("filter keeps matches with their subtree and the ancestors of matches", () => {
  const tree = buildOutlineTree(symbols);
  assert.equal(filterOutlineTree(tree, "  ").length, 2);
  const byChild = filterOutlineTree(tree, "REND");
  assert.deepEqual(
    byChild.map((node) => [node.name, node.children.map((child) => child.name)]),
    [["Widget", ["render", "render"]]],
  );
  const byParent = filterOutlineTree(tree, "widget");
  assert.equal(byParent[0].children.length, 3);
  assert.deepEqual(filterOutlineTree(tree, "missing"), []);
});

test("finds the innermost symbol containing a line", () => {
  const tree = buildOutlineTree(symbols);
  const widget = tree[0];
  assert.equal(outlineNodeAtLine(tree, 9), widget.children[1].id);
  assert.equal(outlineNodeAtLine(tree, 6), widget.id);
  assert.equal(outlineNodeAtLine(tree, 18), undefined);
  assert.equal(outlineNodeAtLine(tree, 22), tree[1].id);
});

test("unknown kinds fall back to a generic icon name", () => {
  assert.equal(symbolKindName(21), "enum-member");
  assert.equal(symbolKindName(99), "misc");
});
