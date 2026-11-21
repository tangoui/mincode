import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";

// Static wiring guarantees of the keybinding system (phase 2): one window
// keyboard dispatcher, the palette stays lazy, and no per-widget key hacks.

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

// Window/document key listeners that are not keybindings. Keep this list short;
// every new binding belongs in the keymap catalog + command registry instead.
const WINDOW_KEY_LISTENER_ALLOWLIST = new Set([
  "services/keybinding-dispatcher.ts",
  // Esc-to-dismiss of a transient popup: attached only while the context menu is open, removed on
  // close and unmount; not a keybinding (no command, not rebindable).
  "components/ui/AppContextMenu.vue",
]);

test("only the keybinding dispatcher listens for keys on window/document", async () => {
  const offenders = [];
  for (const file of await sourceFiles()) {
    const path = relative(file);
    if (WINDOW_KEY_LISTENER_ALLOWLIST.has(path)) continue;
    const source = await readFile(file, "utf8");
    if (/\b(window|document)\.addEventListener\(\s*["']key(down|up|press)["']/.test(source)) offenders.push(path);
  }
  assert.deepEqual(offenders, []);
});

test("App.vue installs the dispatcher and app commands without loading the palette", async () => {
  const app = await readFile(new URL("App.vue", SRC), "utf8");
  assert.doesNotMatch(app, /use-command-palette/);
  assert.doesNotMatch(app, /handleGlobalKeydown|handleAppLockKeydown|matchesCommandPaletteShortcut/);
  assert.match(app, /const uninstallKeybindingDispatcher = installKeybindingDispatcher\(\{ isLocked: \(\) => appLock\.locked \}\)/);
  assert.match(app, /uninstallKeybindingDispatcher\(\);/);
  assert.match(app, /void keybindings\.hydrate\(\);/);
  assert.match(app, /useAppCommands\(/);
  const palette = await readFile(new URL("composables/use-command-palette.ts", SRC), "utf8");
  // builtin:* commands are registered at startup, not when the palette loads.
  assert.doesNotMatch(palette, /id: "builtin:/);
});

test("no Monaco addCommand or xterm onKey key hacks", async () => {
  for (const file of await sourceFiles()) {
    const source = await readFile(file, "utf8");
    assert.doesNotMatch(source, /\b(diffEditor|editor)\.addCommand\(/, relative(file));
  }
  for (const name of ["components/terminal/LocalTerminal.vue", "components/terminal/SshTerminal.vue"]) {
    const source = await readFile(new URL(name, SRC), "utf8");
    assert.doesNotMatch(source, /\.onKey\(/, name);
    assert.match(source, /attachCustomKeyEventHandler/, name);
  }
});

// Read-only xterm surfaces without keyboard commands (no stdin, no shell).
const TERMINALS_WITHOUT_KEYMAP = new Set(["components/terminal/TerminalPlaybackDialog.vue"]);

test("every interactive xterm installs the keymap handler and no terminal file uses onKey for shortcuts", async () => {
  const terminals = [];
  for (const file of await sourceFiles()) {
    const path = relative(file);
    const source = await readFile(file, "utf8");
    // Desktop smoke creates an off-screen xterm to probe native keyboard input.
    if (path === "services/e2e-smoke-runner.ts" || !/new Terminal\(/.test(source)) continue;
    terminals.push(path);
    assert.doesNotMatch(source, /\.onKey\(/, path);
    assert.doesNotMatch(source, /isTerminalFindKey/, path);
    if (TERMINALS_WITHOUT_KEYMAP.has(path)) {
      assert.doesNotMatch(source, /attachCustomKeyEventHandler/, path);
      continue;
    }
    assert.match(source, /const handleTerminalKey = useTerminalKeybindings\(\{/, path);
    assert.match(source, /xterm\.attachCustomKeyEventHandler\(handleTerminalKey\)/, path);
    // Native paste (Edit menu, middle click) is routed through the same guarded path.
    assert.match(source, /guardTerminalPasteEvents\(terminalHost\.value, pasteGuarded\)/, path);
    assert.match(source, /stopPasteGuard\?\.\(\)/, path);
    assert.match(source, /analyzeTerminalPaste\(text\)/, path);
  }
  assert.deepEqual(terminals.sort(), [
    "components/ssh/SshDockerTerminal.vue",
    "components/terminal/LocalTerminal.vue",
    "components/terminal/SshTerminal.vue",
    "components/terminal/TerminalPlaybackDialog.vue",
  ]);
  // Local and SSH terminals route keyboard paste through the guarded clipboard path.
  for (const name of ["components/terminal/LocalTerminal.vue", "components/terminal/SshTerminal.vue"]) {
    const source = await readFile(new URL(name, SRC), "utf8");
    assert.match(source, /paste: requestPaste,/, name);
    assert.match(source, /find: openSearch,/, name);
  }
  const menu = await readFile(new URL("components/terminal/TerminalContextMenu.vue", SRC), "utf8");
  for (const command of ["terminal:copy", "terminal:paste", "terminal:select-all", "terminal:clear"]) {
    assert.match(menu, new RegExp(`keybindings\\.label\\("${command}"\\)`), command);
  }
});

test("migrated handlers register catalog commands instead of listening for keys", async () => {
  const project = await readFile(new URL("views/ProjectWorkspaceView.vue", SRC), "utf8");
  assert.match(project, /id: "project:copy-node"/);
  assert.match(project, /id: "project:paste-node"/);
  assert.match(project, /registerKeybindingContext\("tree\.selection"/);
  const appCommands = await readFile(new URL("composables/use-app-commands.ts", SRC), "utf8");
  for (const id of ["builtin:command-palette", "builtin:settings", "builtin:ai", "builtin:lock-app"]) {
    assert.match(appCommands, new RegExp(`id: "${id}"`));
  }
});
