import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { monacoDefaultRules } from "../src/domain/keybindings/monaco-defaults.ts";
import { buildMonacoKeymapRules } from "../src/domain/keybindings/monaco-keymap-rules.ts";
import { HOST_KEYBINDING_RULES } from "../src/domain/keybindings/presets/index.ts";
import { ALL_KEYMAP_PRESETS, keymapPresetChainOf } from "./keymap-plugin-presets.mjs";
import { resolveKeybindings } from "../src/domain/keybindings/resolver.ts";
import {
  createVimAttachment,
  isVimCompositionKey,
  planVimExCommand,
  VIM_EX_COMMANDS,
  vimModeIndicator,
  vimOwnsSequence,
} from "../src/domain/keybindings/vim.ts";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const PLATFORMS = ["mac", "win", "linux"];
const owns = (text, platform) => vimOwnsSequence(parseKeySequence(text, platform), platform);

test("Vim owns the plain Ctrl chords monaco-vim binds on every platform", () => {
  for (const platform of PLATFORMS) {
    for (const text of ["ctrl+v", "ctrl+r", "ctrl+w", "ctrl+d", "ctrl+u", "ctrl+o", "ctrl+[", "ctrl+space", "ctrl+backspace"]) {
      assert.equal(owns(text, platform), true, `${platform} ${text}`);
    }
  }
});

test("Vim leaves ⌘, Shift/Alt and unbound Ctrl chords to MinCode / Monaco", () => {
  for (const text of ["cmd+k", "cmd+s", "cmd+p", "cmd+v", "ctrl+shift+v", "alt+v", "ctrl+alt+d"]) {
    assert.equal(owns(text, "mac"), false, `mac ${text}`);
  }
  for (const platform of PLATFORMS) {
    for (const text of ["ctrl+s", "ctrl+k", "ctrl+z", "ctrl+g", "ctrl+shift+p", "f1", "escape", "ctrl+1"]) {
      assert.equal(owns(text, platform), false, `${platform} ${text}`);
    }
  }
  // Chords: the first chord decides.
  assert.equal(owns("ctrl+k ctrl+v", "win"), false);
  assert.equal(owns("ctrl+w ctrl+k", "linux"), true);
});

// Stub of Monaco's KeyMod / KeyCode enums (values match monaco-editor).
const KeyCode = {
  Backspace: 1, Tab: 2, Enter: 3, Escape: 9, Space: 10,
  LeftArrow: 15, UpArrow: 16, RightArrow: 17, DownArrow: 18, Delete: 20,
  ...Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`Digit${i}`, 21 + i])),
  ...Object.fromEntries(Array.from({ length: 26 }, (_, i) => [`Key${String.fromCharCode(65 + i)}`, 31 + i])),
  ...Object.fromEntries(Array.from({ length: 12 }, (_, i) => [`F${i + 1}`, 59 + i])),
  Comma: 87, Slash: 90, BracketLeft: 92, BracketRight: 94, Period: 89, Minus: 88, Equal: 86, Semicolon: 85, Quote: 95, Backquote: 91, Backslash: 93,
  Home: 14, End: 13, PageUp: 11, PageDown: 12,
};
const api = { KeyMod: { CtrlCmd: 2048, Shift: 1024, Alt: 512, WinCtrl: 256 }, KeyCode, chord: (a, b) => (a | (b << 16)) >>> 0 };

test("the Monaco keymap skips only Vim-owned sequences", () => {
  for (const presetId of ALL_KEYMAP_PRESETS.map((preset) => preset.id)) {
    const chain = keymapPresetChainOf(presetId);
    assert.ok(chain.length, presetId);
    for (const platform of PLATFORMS) {
      const resolution = resolveKeybindings({ platform, presets: chain, defaults: monacoDefaultRules(platform), host: HOST_KEYBINDING_RULES });
      const plain = buildMonacoKeymapRules({ platform, resolution, api });
      const vim = buildMonacoKeymapRules({ platform, resolution, api, editorModeOwns: (sequence) => vimOwnsSequence(sequence, platform) });
      const vimSkips = vim.skipped.filter((skip) => skip.reason === "editor-mode");
      for (const skip of vimSkips) assert.ok(vimOwnsSequence(skip.binding.sequence, platform), `${presetId}/${platform} ${skip.detail}`);
      // Vim only removes rules; everything it keeps is identical to the plain rule set.
      const signature = (rule) => `${rule.keybinding}|${rule.command}|${rule.when ?? ""}|${JSON.stringify(rule.commandArgs ?? null)}`;
      const plainSet = new Set(plain.rules.map(signature));
      for (const rule of vim.rules) assert.ok(plainSet.has(signature(rule)), `${presetId}/${platform} extra ${signature(rule)}`);
      const vimSet = new Set(vim.rules.map(signature));
      for (const rule of plain.rules.filter((candidate) => !vimSet.has(signature(candidate)))) {
        assert.equal(rule.command.startsWith("-"), false, `${presetId}/${platform} lost removal ${signature(rule)}`);
      }
      if (platform === "mac") {
        // ⌘ chords (command palette, save, …) keep their Monaco rules under Vim.
        const cmdRules = (result) => result.rules.filter((rule) => (rule.keybinding & 0xffff & 2048) !== 0).length;
        assert.equal(cmdRules(vim), cmdRules(plain), `${presetId} ⌘ rules`);
      }
    }
  }
});

test("ex commands map to save / close with hints", () => {
  assert.deepEqual(VIM_EX_COMMANDS.map((command) => command.prefix), ["q", "wq", "x"]);
  const all = { canSave: true, canClose: true };
  assert.deepEqual(planVimExCommand("write", all), { save: true, close: false, hints: [] });
  assert.deepEqual(planVimExCommand("quit", all), { save: false, close: true, hints: [] });
  assert.deepEqual(planVimExCommand("write-quit", all), { save: true, close: true, hints: [] });
  // SQL editor: no save handler.
  assert.deepEqual(planVimExCommand("write", { canSave: false, canClose: true }), { save: false, close: false, hints: ["no-save"] });
  assert.deepEqual(planVimExCommand("write-quit", { canSave: false, canClose: true }), { save: false, close: false, hints: ["no-save"] });
  // workbench:close-tab not registered yet.
  assert.deepEqual(planVimExCommand("quit", { canSave: true, canClose: false }), { save: false, close: false, hints: ["no-close"] });
  assert.deepEqual(planVimExCommand("write-quit", { canSave: true, canClose: false }), { save: true, close: false, hints: ["no-close"] });
});

test("mode indicator normalises vim-mode-change payloads", () => {
  assert.equal(vimModeIndicator(null), "normal");
  assert.equal(vimModeIndicator({ mode: "normal" }), "normal");
  assert.equal(vimModeIndicator({ mode: "insert" }), "insert");
  assert.equal(vimModeIndicator({ mode: "replace" }), "replace");
  assert.equal(vimModeIndicator({ mode: "visual" }), "visual");
  assert.equal(vimModeIndicator({ mode: "visual", subMode: "linewise" }), "visual-line");
  assert.equal(vimModeIndicator({ mode: "visual", subMode: "blockwise" }), "visual-block");
  assert.equal(vimModeIndicator({ mode: 42 }), "normal");
});

test("IME composition keydowns are recognised", () => {
  assert.equal(isVimCompositionKey({ isComposing: true, key: "a", keyCode: 65 }), true);
  assert.equal(isVimCompositionKey({ key: "Process", keyCode: 229 }), true);
  assert.equal(isVimCompositionKey({ key: "Unidentified", keyCode: 229 }), true);
  assert.equal(isVimCompositionKey({ key: "Escape", keyCode: 27 }), false);
  assert.equal(isVimCompositionKey({ key: "j", keyCode: 74, isComposing: false }), false);
});

function fakeEditor(readOnly = false) {
  const listeners = new Set();
  const log = [];
  let counter = 0;
  const pending = [];
  return {
    log,
    pending,
    get listenerCount() {
      return listeners.size;
    },
    setReadOnly(value) {
      readOnly = value;
      for (const listener of [...listeners]) listener();
    },
    hooks(options = {}) {
      return {
        isReadOnly: () => readOnly,
        onReadOnlyChange(listener) {
          listeners.add(listener);
          return { dispose: () => listeners.delete(listener) };
        },
        start() {
          const id = ++counter;
          log.push(`start:${id}`);
          const session = { dispose: () => log.push(`dispose:${id}`) };
          if (options.fail) return Promise.reject(new Error("chunk failed"));
          if (options.deferred) {
            return new Promise((resolveStart) => pending.push(() => resolveStart(session)));
          }
          return session;
        },
        onError: (error) => log.push(`error:${error.message}`),
      };
    },
  };
}

const flush = () => new Promise((resolveFlush) => setTimeout(resolveFlush, 0));

test("attachment starts on writable editors and detaches on dispose", async () => {
  const editor = fakeEditor();
  const attachment = createVimAttachment(editor.hooks());
  await flush();
  assert.equal(attachment.active, true);
  assert.deepEqual(editor.log, ["start:1"]);
  attachment.dispose();
  attachment.dispose();
  assert.equal(attachment.active, false);
  assert.deepEqual(editor.log, ["start:1", "dispose:1"]);
  assert.equal(editor.listenerCount, 0);
});

test("read-only editors stay plain and follow read-only toggles", async () => {
  const editor = fakeEditor(true);
  const attachment = createVimAttachment(editor.hooks());
  await flush();
  assert.equal(attachment.active, false);
  assert.deepEqual(editor.log, []);
  editor.setReadOnly(false);
  await flush();
  assert.deepEqual(editor.log, ["start:1"]);
  editor.setReadOnly(true);
  assert.deepEqual(editor.log, ["start:1", "dispose:1"]);
  editor.setReadOnly(false);
  await flush();
  assert.deepEqual(editor.log, ["start:1", "dispose:1", "start:2"]);
  attachment.dispose();
  assert.deepEqual(editor.log.at(-1), "dispose:2");
});

test("stale asynchronous starts are disposed when they resolve", async () => {
  const editor = fakeEditor();
  const attachment = createVimAttachment(editor.hooks({ deferred: true }));
  assert.equal(attachment.active, true);
  attachment.dispose();
  editor.pending.shift()();
  await flush();
  assert.deepEqual(editor.log, ["start:1", "dispose:1"]);

  const toggled = fakeEditor();
  const second = createVimAttachment(toggled.hooks({ deferred: true }));
  toggled.setReadOnly(true);
  toggled.setReadOnly(false);
  // Two starts in flight: the first became stale when the editor turned read-only.
  toggled.pending.shift()();
  toggled.pending.shift()();
  await flush();
  assert.deepEqual(toggled.log, ["start:1", "start:2", "dispose:1"]);
  second.dispose();
  assert.deepEqual(toggled.log.at(-1), "dispose:2");
});

test("a failed lazy load reports and leaves the editor plain", async () => {
  const editor = fakeEditor();
  const attachment = createVimAttachment(editor.hooks({ fail: true }));
  await flush();
  assert.equal(attachment.active, false);
  assert.deepEqual(editor.log, ["start:1", "error:chunk failed"]);
  attachment.dispose();
});

function sourceFiles(directory) {
  return readdirSync(directory).flatMap((name) => {
    const path = join(directory, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(ts|js|vue|mjs)$/.test(name) ? [path] : [];
  });
}

test("monaco-vim is only reachable through a dynamic import", () => {
  const files = sourceFiles(join(root, "src"));
  const importers = files.filter((file) => /from\s+["']monaco-vim["']|import\(\s*["']monaco-vim["']/.test(readFileSync(file, "utf8")));
  assert.deepEqual(importers.map((file) => relative(root, file).replaceAll("\\", "/")), ["src/services/vim/monaco-vim-runtime.ts"]);
  const runtimeRefs = files.filter((file) => readFileSync(file, "utf8").includes("vim/monaco-vim-runtime"));
  for (const file of runtimeRefs) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/^.*vim\/monaco-vim-runtime.*$/gm)) {
      const line = match[0].trim();
      const ok = line.includes("import(") || /^import\s+type\s/.test(line) || line.startsWith("*") || line.startsWith("//");
      assert.ok(ok, `${relative(root, file)}: static runtime import: ${line}`);
    }
  }
  const config = readFileSync(join(root, "vite.config.ts"), "utf8");
  assert.match(config, /"monaco-vim":\s*fileURLToPath\(new URL\("\.\/node_modules\/monaco-vim\/dist\/index\.mjs"/);
  assert.match(config, /exclude:\s*\["monaco-vim"\]/);
});
