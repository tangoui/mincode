import assert from "node:assert/strict";
import test from "node:test";
import { LazyTree, stickyRows, visibleRows, visibleWindow } from "../src/domain/projects/lazy-tree.ts";

const dir = (id) => ({ id, name: id, leaf: false });
const file = (id) => ({ id, name: id, leaf: true });

/** A tree whose load resolves synchronously from a fixture map; records each load. */
function fixtureTree(listing, options = {}) {
  const loads = [];
  const tree = new LazyTree({
    key: (data) => data.id,
    isLeaf: (data) => data.leaf,
    load: (node, resolve) => {
      loads.push(node.level === 0 ? "<root>" : node.data.id);
      const children = listing[node.level === 0 ? "<root>" : node.data.id] ?? [];
      if (options.async) queueMicrotask(() => resolve(children));
      else resolve(children);
    },
    defaultExpandedKeys: options.expanded ?? [],
  });
  return { tree, loads };
}

const listing = {
  "<root>": [dir("a"), dir("b")],
  a: [dir("a/x"), file("a/1"), file("a/2")],
  "a/x": [file("a/x/1")],
  b: [file("b/1")],
};

test("loads the synthetic root on start and creates level-1 nodes", () => {
  const { tree, loads } = fixtureTree(listing);
  tree.start();
  assert.deepEqual(loads, ["<root>"]);
  assert.deepEqual(tree.root.childNodes.map((node) => [node.data.id, node.level, node.isLeaf]), [["a", 1, false], ["b", 1, false]]);
  assert.equal(tree.root.loaded, true);
});

test("expand loads children once, collapse keeps them, re-expand does not reload", () => {
  const { tree, loads } = fixtureTree(listing);
  tree.start();
  const a = tree.getNode("a");
  let called = 0;
  a.expand(() => { called += 1; });
  assert.equal(called, 1);
  assert.equal(a.expanded, true);
  assert.equal(a.loaded, true);
  assert.deepEqual(a.childNodes.map((node) => node.data.id), ["a/x", "a/1", "a/2"]);
  a.collapse();
  assert.equal(a.expanded, false);
  a.expand();
  assert.deepEqual(loads, ["<root>", "a"]);
});

test("an unloaded node reloads after loaded is cleared (in-place refresh)", () => {
  const { tree, loads } = fixtureTree(listing);
  tree.start();
  const a = tree.getNode("a");
  a.expand();
  a.loaded = false;
  a.collapse();
  a.expand();
  assert.deepEqual(loads, ["<root>", "a", "a"]);
});

test("async load marks loading and expands only after resolve", async () => {
  const { tree } = fixtureTree(listing, { async: true });
  tree.start();
  await Promise.resolve();
  const a = tree.getNode("a");
  const done = new Promise((resolve) => a.expand(resolve));
  assert.equal(a.loading, true);
  assert.equal(a.expanded, false);
  await done;
  assert.equal(a.loading, false);
  assert.equal(a.expanded, true);
});

test("default expanded keys expand nodes as they are created", () => {
  const { tree } = fixtureTree(listing, { expanded: ["a", "a/x"] });
  tree.start();
  assert.equal(tree.getNode("a").expanded, true);
  assert.equal(tree.getNode("a/x").expanded, true);
  assert.equal(tree.getNode("b").expanded, false);
});

test("expand with expandParent opens collapsed ancestors", () => {
  const { tree } = fixtureTree(listing);
  tree.start();
  tree.getNode("a").expand();
  tree.getNode("a/x").expand();
  tree.getNode("a").collapse();
  tree.getNode("a/x").expand(undefined, true);
  assert.equal(tree.getNode("a").expanded, true);
});

test("insertChild and remove keep the key index in sync", () => {
  const { tree } = fixtureTree(listing);
  tree.start();
  const b = tree.getNode("b");
  b.expand();
  b.insertChild({ data: file("b/0") }, 0);
  assert.deepEqual(b.childNodes.map((node) => node.data.id), ["b/0", "b/1"]);
  assert.equal(tree.getNode("b/0").parent, b);
  assert.equal(tree.getNode("b/0").level, 2);
  tree.remove(tree.getNode("b/1"));
  assert.equal(tree.getNode("b/1"), undefined);
  tree.remove(b);
  assert.equal(tree.getNode("b/0"), undefined);
  assert.deepEqual(tree.root.childNodes.map((node) => node.data.id), ["a"]);
});

test("reloading a node drops descendants of the replaced children from the index", () => {
  const { tree } = fixtureTree(listing);
  tree.start();
  tree.getNode("a").expand();
  tree.getNode("a/x").expand();
  assert.ok(tree.getNode("a/x/1"));
  const a = tree.getNode("a");
  a.loaded = false;
  a.collapse();
  a.expand();
  assert.equal(tree.getNode("a/x").expanded, false);
  assert.equal(tree.getNode("a/x/1"), undefined);
});

test("visibleRows flattens expanded nodes depth-first", () => {
  const { tree } = fixtureTree(listing, { expanded: ["a", "a/x"] });
  tree.start();
  assert.deepEqual(visibleRows(tree.root).map((node) => node.data.id), ["a", "a/x", "a/x/1", "a/1", "a/2", "b"]);
});

test("visibleWindow returns the overscanned index range for a scroll position", () => {
  assert.deepEqual(visibleWindow({ count: 1000, rowHeight: 28, scrollTop: 0, viewportHeight: 280, overscan: 5 }), { start: 0, end: 15 });
  assert.deepEqual(visibleWindow({ count: 1000, rowHeight: 28, scrollTop: 2800, viewportHeight: 280, overscan: 5 }), { start: 95, end: 115 });
  assert.deepEqual(visibleWindow({ count: 12, rowHeight: 28, scrollTop: 2800, viewportHeight: 280, overscan: 5 }), { start: 0, end: 12 });
  assert.deepEqual(visibleWindow({ count: 0, rowHeight: 28, scrollTop: 0, viewportHeight: 280, overscan: 5 }), { start: 0, end: 0 });
});

test("stickyRows pins expanded ancestors that scrolled above their slot", () => {
  const { tree } = fixtureTree(listing, { expanded: ["a", "a/x"] });
  tree.start();
  const rows = visibleRows(tree.root);
  // Rows: a(0) a/x(1) a/x/1(2) a/1(3) a/2(4) b(5); row height 10.
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 0, maxLevels: 5 }), []);
  // Scrolled one row: "a" is pinned at the top and a/x, now under it, pins to the second slot.
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 10, maxLevels: 5 }).map((row) => [row.node.data.id, row.top]), [["a", 0], ["a/x", 10]]);
  // a/x's subtree is ending, so the next row pushes it up; once past, only "a" stays.
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 15, maxLevels: 5 }).map((row) => [row.node.data.id, row.top]), [["a", 0], ["a/x", 5]]);
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 20, maxLevels: 5 }).map((row) => [row.node.data.id, row.top]), [["a", 0]]);
  // Near the end of a's subtree the pinned row is pushed up by the next sibling.
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 44, maxLevels: 5 }).map((row) => [row.node.data.id, row.top]), [["a", -4]]);
  // Never more levels than allowed.
  assert.deepEqual(stickyRows(rows, { rowHeight: 10, scrollTop: 15, maxLevels: 1 }).map((row) => row.node.data.id), ["a"]);
});
