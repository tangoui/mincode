import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { KEYBINDING_COMMANDS } from "../src/domain/keybindings/catalog.ts";
import {
  CLOSED_TAB_LIMIT,
  adjacentTabId,
  closedEntryForTab,
  closeTargetKind,
  popClosedEntry,
  pushClosedEntry,
  tabIdAtIndex,
} from "../src/domain/workbench/tab-navigation.ts";

// Phase 6: workbench tab commands (pure logic) and the handler wiring of every
// available app command in the keybinding catalog.

test("adjacentTabId cycles in both directions and starts from the edges without an active tab", () => {
  const ids = ["a", "b", "c"];
  assert.equal(adjacentTabId(ids, "a", 1), "b");
  assert.equal(adjacentTabId(ids, "c", 1), "a");
  assert.equal(adjacentTabId(ids, "a", -1), "c");
  assert.equal(adjacentTabId(ids, "b", -1), "a");
  assert.equal(adjacentTabId(ids, "", 1), "a");
  assert.equal(adjacentTabId(ids, "missing", -1), "c");
  assert.equal(adjacentTabId(["only"], "only", 1), "only");
  assert.equal(adjacentTabId([], "a", 1), undefined);
});

test("tabIdAtIndex is 1-based, ignores indexes past the end and maps 9 to the last tab", () => {
  const ids = ["a", "b", "c"];
  assert.equal(tabIdAtIndex(ids, 1), "a");
  assert.equal(tabIdAtIndex(ids, 3), "c");
  assert.equal(tabIdAtIndex(ids, 4), undefined);
  assert.equal(tabIdAtIndex(ids, 9), "c");
  assert.equal(tabIdAtIndex(ids, 0), undefined);
  assert.equal(tabIdAtIndex([], 9), undefined);
  const many = Array.from({ length: 12 }, (_, index) => `t${index + 1}`);
  assert.equal(tabIdAtIndex(many, 8), "t8");
  assert.equal(tabIdAtIndex(many, 9), "t12");
});

test("closeTargetKind picks document tabs only when the editor area owns (or nothing has) focus", () => {
  const base = { hasDocumentHost: true, documentTabCount: 2 };
  assert.equal(closeTargetKind({ ...base, focus: "editor-area" }), "document");
  assert.equal(closeTargetKind({ ...base, focus: "none" }), "document");
  assert.equal(closeTargetKind({ ...base, focus: "elsewhere" }), "workbench");
  assert.equal(closeTargetKind({ ...base, focus: "editor-area", documentTabCount: 0 }), "workbench");
  assert.equal(closeTargetKind({ ...base, focus: "editor-area", hasDocumentHost: false }), "workbench");
});

test("the closed-tab stack is LIFO, deduplicated per tab and bounded", () => {
  let stack = [];
  stack = pushClosedEntry(stack, { kind: "view", view: "database" });
  stack = pushClosedEntry(stack, { kind: "document", path: "/p/a.ts" });
  stack = pushClosedEntry(stack, { kind: "view", view: "database" });
  assert.deepEqual(stack.map((entry) => entry.kind), ["document", "view"]);

  // Terminals are never merged: each closed terminal is its own entry.
  stack = pushClosedEntry(stack, { kind: "terminal", title: "T" });
  stack = pushClosedEntry(stack, { kind: "terminal", title: "T" });
  assert.equal(stack.length, 4);

  const { entry, rest } = popClosedEntry(stack);
  assert.deepEqual(entry, { kind: "terminal", title: "T" });
  assert.equal(rest.length, 3);
  assert.equal(stack.length, 4, "push/pop return new arrays");
  assert.deepEqual(popClosedEntry([]), { entry: undefined, rest: [] });

  let bounded = [];
  for (let index = 0; index < CLOSED_TAB_LIMIT + 5; index += 1) {
    bounded = pushClosedEntry(bounded, { kind: "document", path: `/f${index}` });
  }
  assert.equal(bounded.length, CLOSED_TAB_LIMIT);
  assert.equal(bounded[0].path, "/f5");
  assert.equal(pushClosedEntry(bounded, { kind: "document", path: "/x" }, 3).length, 3);
});

test("closedEntryForTab reopens singleton views and terminals", () => {
  assert.deepEqual(closedEntryForTab({ id: "ws:database", workspaceId: "ws", view: "database" }), {
    kind: "view",
    view: "database",
  });
  assert.deepEqual(closedEntryForTab({ id: "ws:terminal:uuid", workspaceId: "ws", view: "terminal", title: "Shell 2" }), {
    kind: "terminal",
    title: "Shell 2",
  });
  assert.equal(closedEntryForTab({ id: "ws:sftp:uuid", workspaceId: "ws", view: "sftp" }), null);
  assert.equal(closedEntryForTab({ id: "ws:dashboard", workspaceId: "ws", view: "dashboard" }), null);
});

// ---------------------------------------------------------------------------
// Handler wiring: every available `handler: "app"` command must be registered
// somewhere. Startup = registered once from App.vue / AppTabBar (always on in
// the main window); view = registered by a workspace view while it is active.

const SRC = new URL("../src/", import.meta.url);

async function sourceFiles(directory = SRC) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const url = new URL(entry.name + (entry.isDirectory() ? "/" : ""), directory);
    if (entry.isDirectory()) files.push(...(await sourceFiles(url)));
    else if (/\.(ts|vue)$/.test(entry.name)) files.push(url);
  }
  return files;
}

const relative = (url) => fileURLToPath(url).slice(fileURLToPath(SRC).length).replaceAll("\\", "/");

/** Where each available app command's handler is registered. */
const HANDLER_OWNERS = {
  "terminal:new-tab": ["view", "views/TerminalWorkspaceView.vue"],
  "terminal:next-tab": ["view", "views/TerminalWorkspaceView.vue"],
  "terminal:prev-tab": ["view", "views/TerminalWorkspaceView.vue"],
  "builtin:command-palette": ["startup", "composables/use-app-commands.ts"],
  "builtin:search-everywhere": ["startup", "composables/use-app-commands.ts"],
  "builtin:settings": ["startup", "composables/use-app-commands.ts"],
  "builtin:keybindings": ["startup", "composables/use-app-commands.ts"],
  "builtin:ai": ["startup", "composables/use-app-commands.ts"],
  "workbench:toggle-sidebar": ["view", "composables/use-active-view-commands.ts"],
  "workbench:show-view.terminal": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:new-terminal": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:close-tab": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:close-all": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:reopen-closed": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:next-tab": ["startup", "composables/use-workbench-tab-commands.ts"],
  "workbench:prev-tab": ["startup", "composables/use-workbench-tab-commands.ts"],
  ...Object.fromEntries(
    Array.from({ length: 9 }, (_, index) => [`workbench:tab-${index + 1}`, ["startup", "composables/use-workbench-tab-commands.ts"]]),
  ),
  "editor:save": ["editor", "domain/keybindings/monaco-keymap-rules.ts"],
  "database:run-query": ["editor", "domain/keybindings/monaco-keymap-rules.ts"],
  "database:submit-grid-changes": ["view", "views/DatabaseWorkspaceView.vue"],
  "ai:new-conversation": ["view", "components/ai/AiAssistantDrawer.vue"],
  "ai:search-conversations": ["view", "components/ai/AiAssistantDrawer.vue"],
  "ai:previous-conversation": ["view", "components/ai/AiAssistantDrawer.vue"],
  "ai:next-conversation": ["view", "components/ai/AiAssistantDrawer.vue"],
  "ai:stop": ["view", "components/ai/AiAssistantDrawer.vue"],
  "ai:copy-message": ["view", "components/ai/AiAssistantDrawer.vue"],
};

test("every available app command in the catalog has a registered handler", async () => {
  const sources = new Map();
  for (const file of await sourceFiles()) sources.set(relative(file), await readFile(file, "utf8"));

  const available = KEYBINDING_COMMANDS.filter((command) => command.available && command.handler === "app");
  assert.deepEqual(
    available.map((command) => command.id).filter((id) => !HANDLER_OWNERS[id]),
    [],
    "add new available app commands to HANDLER_OWNERS (startup or view)",
  );

  const registered = (source, id) => {
    const quoted = new RegExp(`id:\\s*["'\`]${id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["'\`]`);
    if (quoted.test(source)) return true;
    // Commands registered through a helper take the id as its first argument.
    const helper = new RegExp(`command\\(\\s*["'\`]${id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["'\`]`);
    if (helper.test(source)) return true;
    // …or as the key of a `[id, handler]` table.
    return new RegExp(`\\[\\s*["'\`]${id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["'\`],`).test(source);
  };

  for (const command of available) {
    const [kind, file] = HANDLER_OWNERS[command.id];
    const source = sources.get(file);
    assert.ok(source, `${command.id}: ${file} exists`);
    if (kind === "editor") {
      // Editor host commands run the focused editor's handler through Monaco.
      assert.match(source, new RegExp(`"${command.id}"`), `${command.id} is mapped in ${file}`);
      continue;
    }
    if (/^workbench:tab-\d$/.test(command.id)) {
      assert.match(source, /command\(`workbench:tab-\$\{index \+ 1\}`/, `${command.id} in ${file}`);
      continue;
    }
    if (/^workbench:show-view\./.test(command.id)) {
      assert.match(source, new RegExp(`id: "${command.id.replace(/\./g, "\\.")}"`), `${command.id} in ${file}`);
      continue;
    }
    assert.ok(registered(source, command.id), `${command.id} is registered in ${file}`);
  }
});

test("startup and view registrations are wired into their owners", async () => {
  const read = (path) => readFile(new URL(path, SRC), "utf8");
  const tabBar = await read("components/layout/AppTabBar.vue");
  assert.match(tabBar, /useWorkbenchTabCommands\(\{/);
  const app = await read("App.vue");
  assert.match(app, /useAppCommands\(\{/);
  assert.match(app, /<AppTabBar \/>/);

  // View commands register on mount/activation and dispose on deactivation/unmount.
  const viewCommands = await read("composables/use-active-view-commands.ts");
  for (const hook of ["onMounted", "onActivated", "onDeactivated", "onBeforeUnmount"]) {
    assert.match(viewCommands, new RegExp(`${hook}\\(`), hook);
  }
  for (const view of [
    "views/DatabaseWorkspaceView.vue",
    "views/HttpWorkspaceView.vue",
    "views/SshWorkspaceView.vue",
    "views/SessionsWorkspaceView.vue",
  ]) {
    assert.match(await read(view), /useToggleSidebarCommand\(/, view);
  }
});
