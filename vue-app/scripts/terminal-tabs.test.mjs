import assert from "node:assert/strict";
import test from "node:test";

import {
  MAX_TERMINAL_TABS,
  TERMINAL_TAB_TITLE_MAX,
  activeTerminalTab,
  adjacentTerminalTabId,
  aiCloseTargetForPane,
  insertTerminalTab,
  isBackgroundTerminalPane,
  removeTerminalTab,
  renameTerminalTab,
  selectTerminalTab,
  terminalTabForPane,
  terminalTabLabel,
  updateTerminalTab,
  upgradeLegacyLayout,
} from "../src/domain/terminal/terminal-tabs.ts";

const leaf = (id) => ({ type: "local", id, shellType: "zsh" });
const split = (id, children) => ({
  type: "split",
  id,
  direction: "horizontal",
  children,
  sizes: children.map(() => 1 / children.length),
});
const tab = (id, root = leaf(`${id}-p`), extra = {}) => {
  const first = root.type === "split" ? firstLeaf(root) : root.id;
  return { id, root, activePaneId: first, ...extra };
};
function firstLeaf(node) {
  return node.type === "split" ? firstLeaf(node.children[0]) : node.id;
}
const stateOf = (ids, activeTabId) => ({ tabs: ids.map((id) => tab(id)), activeTabId });
const ids = (state) => state.tabs.map((t) => t.id);

test("constants", () => {
  assert.equal(MAX_TERMINAL_TABS, 12);
  assert.equal(TERMINAL_TAB_TITLE_MAX, 60);
});

test("activeTerminalTab falls back to the first tab for an unknown id", () => {
  const state = stateOf(["a", "b"], "b");
  assert.equal(activeTerminalTab(state).id, "b");
  assert.equal(activeTerminalTab({ ...state, activeTabId: "zz" }).id, "a");
});

test("insertTerminalTab inserts right of the active tab and selects it", () => {
  const state = stateOf(["a", "b", "c"], "b");
  const next = insertTerminalTab(state, tab("n"));
  assert.deepEqual(ids(next), ["a", "b", "n", "c"]);
  assert.equal(next.activeTabId, "n");
  assert.deepEqual(ids(state), ["a", "b", "c"], "input is not mutated");
});

test("insertTerminalTab refuses the 13th tab", () => {
  const full = stateOf(Array.from({ length: 12 }, (_, i) => `t${i}`), "t0");
  assert.equal(insertTerminalTab(full, tab("x")), null);
  const eleven = stateOf(Array.from({ length: 11 }, (_, i) => `t${i}`), "t10");
  assert.equal(insertTerminalTab(eleven, tab("x")).tabs.length, 12);
});

test("removeTerminalTab closing the active middle tab selects the right neighbour", () => {
  const result = removeTerminalTab(stateOf(["a", "b", "c"], "b"), "b");
  assert.deepEqual(ids(result.state), ["a", "c"]);
  assert.equal(result.state.activeTabId, "c");
});

test("removeTerminalTab closing the active rightmost tab selects the left neighbour", () => {
  const result = removeTerminalTab(stateOf(["a", "b", "c"], "c"), "c");
  assert.deepEqual(ids(result.state), ["a", "b"]);
  assert.equal(result.state.activeTabId, "b");
});

test("removeTerminalTab closing an inactive tab keeps the selection", () => {
  const result = removeTerminalTab(stateOf(["a", "b", "c"], "c"), "a");
  assert.deepEqual(ids(result.state), ["b", "c"]);
  assert.equal(result.state.activeTabId, "c");
});

test("removeTerminalTab returns every leaf pane id of the closed tab", () => {
  const root = split("s1", [leaf("p1"), split("s2", [leaf("p2"), leaf("p3")])]);
  const state = { tabs: [tab("a"), tab("b", root)], activeTabId: "a" };
  const result = removeTerminalTab(state, "b");
  assert.deepEqual(result.paneIds, ["p1", "p2", "p3"]);
});

test("removeTerminalTab refuses the last tab and unknown ids", () => {
  assert.equal(removeTerminalTab(stateOf(["a"], "a"), "a"), null);
  assert.equal(removeTerminalTab(stateOf(["a", "b"], "a"), "zz"), null);
});

test("selectTerminalTab selects an existing tab and ignores unknown ids", () => {
  const state = stateOf(["a", "b"], "a");
  assert.equal(selectTerminalTab(state, "b").activeTabId, "b");
  assert.equal(selectTerminalTab(state, "zz"), state);
});

test("adjacentTerminalTabId wraps around both ends", () => {
  assert.equal(adjacentTerminalTabId(stateOf(["a", "b", "c"], "c"), 1), "a");
  assert.equal(adjacentTerminalTabId(stateOf(["a", "b", "c"], "a"), -1), "c");
  assert.equal(adjacentTerminalTabId(stateOf(["a", "b", "c"], "a"), 1), "b");
  assert.equal(adjacentTerminalTabId(stateOf(["a", "b", "c"], "b"), -1), "a");
});

test("renameTerminalTab trims, truncates to 60 and deletes blank titles", () => {
  const state = stateOf(["a", "b"], "a");
  assert.equal(renameTerminalTab(state, "a", "  build  ").tabs[0].title, "build");
  const long = "x".repeat(70);
  assert.equal(renameTerminalTab(state, "a", long).tabs[0].title, "x".repeat(60));
  const titled = renameTerminalTab(state, "a", "build");
  const cleared = renameTerminalTab(titled, "a", "   ");
  assert.equal("title" in cleared.tabs[0], false);
  assert.equal(titled.tabs[0].title, "build", "input is not mutated");
});

test("updateTerminalTab replaces only the matching tab", () => {
  const state = stateOf(["a", "b"], "a");
  const next = updateTerminalTab(state, "b", (t) => ({ ...t, activePaneId: "other" }));
  assert.equal(next.tabs[1].activePaneId, "other");
  assert.equal(next.tabs[0], state.tabs[0]);
  assert.equal(state.tabs[1].activePaneId, "b-p");
});

test("terminalTabForPane finds the tab containing a nested pane", () => {
  const root = split("s1", [leaf("p1"), split("s2", [leaf("p2"), leaf("p3")])]);
  const state = { tabs: [tab("a"), tab("b", root)], activeTabId: "a" };
  assert.equal(terminalTabForPane(state, "p3").id, "b");
  assert.equal(terminalTabForPane(state, "a-p").id, "a");
  assert.equal(terminalTabForPane(state, "s2"), undefined, "split ids are not panes");
  assert.equal(terminalTabForPane(state, "nope"), undefined);
});

test("terminalTabLabel uses title, else active pane label with +N", () => {
  const paneLabel = (pane) => (pane.id === "p1" ? "zsh" : `pane-${pane.id}`);
  assert.equal(terminalTabLabel(tab("a", leaf("p1")), paneLabel), "zsh");
  const three = { id: "b", root: split("s", [leaf("p1"), leaf("p2"), leaf("p3")]), activePaneId: "p1" };
  assert.equal(terminalTabLabel(three, paneLabel), "zsh +2");
  assert.equal(terminalTabLabel({ ...three, title: "build" }, paneLabel), "build");
});

test("upgradeLegacyLayout wraps a single-tree layout into one tab", () => {
  const root = split("s", [leaf("p1"), leaf("p2")]);
  const upgraded = upgradeLegacyLayout({ version: 3, root, activePaneId: "p2" }, () => "t1");
  assert.deepEqual(upgraded, {
    version: 3,
    tabs: [{ id: "t1", root, activePaneId: "p2" }],
    activeTabId: "t1",
  });
});

test("upgradeLegacyLayout leaves other values untouched", () => {
  const already = { version: 4, tabs: [], activeTabId: "x", root: leaf("p"), activePaneId: "p" };
  assert.equal(upgradeLegacyLayout(already, () => "t1"), already);
  assert.equal(upgradeLegacyLayout(null, () => "t1"), null);
  assert.equal(upgradeLegacyLayout("str", () => "t1"), "str");
});

test("aiCloseTargetForPane: pane in a split tab, tab when alone, null for the last pane", () => {
  const multi = {
    tabs: [tab("a", split("s", [leaf("a1"), leaf("a2")])), tab("b")],
    activeTabId: "b",
  };
  assert.equal(aiCloseTargetForPane(multi, "a2"), "pane");
  assert.equal(aiCloseTargetForPane(multi, "b-p"), "tab");
  const single = { tabs: [tab("a")], activeTabId: "a" };
  assert.equal(aiCloseTargetForPane(single, "a-p"), null);
  assert.equal(aiCloseTargetForPane(multi, "missing"), null);
});

test("aiCloseTargetForPane: a split pane of the only tab closes just that pane", () => {
  const single = { tabs: [tab("a", split("s", [leaf("a1"), leaf("a2")]))], activeTabId: "a" };
  assert.equal(aiCloseTargetForPane(single, "a1"), "pane");
  assert.equal(aiCloseTargetForPane(single, "a2"), "pane");
});

test("isBackgroundTerminalPane: only panes of a non-current terminal tab", () => {
  const state = { tabs: [tab("a", split("s", [leaf("a1"), leaf("a2")])), tab("b")], activeTabId: "b" };
  assert.equal(isBackgroundTerminalPane(state, "a2"), true);
  assert.equal(isBackgroundTerminalPane(state, "b-p"), false);
  assert.equal(isBackgroundTerminalPane(state, "missing"), false);
});
