import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import ts from "typescript";

const view = readFileSync(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
const source = view.slice(view.indexOf("function canComparePathWithClipboard("), view.indexOf("async function compareResourceNodeWithClipboard("));
const javascript = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

function setup({ text = "clipboard text", loaded = true, externalKind, read, load } = {}) {
  const document = { path: "/project/a.ts", name: "a.ts", loaded, content: "unsaved text", savedContent: "disk text", externalKind };
  const project = { documents: [document], activePath: document.path, openDocument(path) { this.activePath = path; } };
  const state = {
    project,
    clipboardCompareLoading: { value: false },
    activeProjectTerminalId: { value: null },
    editorComparison: { value: null },
    previewMode: { value: false },
    blameEnabled: { value: false },
    blameHunks: { value: [] },
    canUndo: { value: true },
    canRedo: { value: true },
    nextTick: async () => {},
    flushEditorContent: () => {},
    isImageDocument: (path) => /\.png$/.test(path),
    projectRootForPath: () => "/project",
    isDesktopRuntime: () => true,
    invokeBackend: async (command) => { assert.equal(command, "clipboard_read_text"); return read ? read() : text; },
    ensureDocumentLoaded: async () => { if (load) await load(document); else document.loaded = true; },
    openExternalFile: async () => { document.loaded = true; },
    t: (key) => key,
    ElMessage: { info: (key) => messages.push(key), error: (key) => messages.push(key) },
  };
  const messages = [];
  const compare = new Function(...Object.keys(state), `let clipboardCompareRequest = 0; ${javascript}; return compareFileWithClipboard;`)(...Object.values(state));
  return { state, document, messages, compare: () => compare(document.path) };
}

test("uses a clipboard snapshot and unsaved file content without overwriting the document", async () => {
  const { state, document, compare } = setup();
  await compare();
  assert.equal(state.editorComparison.value.kind, "clipboard");
  assert.equal(state.editorComparison.value.modifiedContent, "clipboard text");
  assert.equal(state.editorComparison.value.originalContent, "unsaved text");
  assert.equal(document.content, "unsaved text");
  assert.equal(document.savedContent, "disk text");
  assert.equal(state.clipboardCompareLoading.value, false);
});

test("loads an unopened text file before comparing", async () => {
  const { document, state, compare } = setup({ loaded: false });
  await compare();
  assert.equal(document.loaded, true);
  assert.equal(state.editorComparison.value.originalContent, "unsaved text");
});

test("empty clipboard shows a hint while whitespace remains comparable", async () => {
  const empty = setup({ text: "" });
  await empty.compare();
  assert.equal(empty.state.editorComparison.value, null);
  assert.deepEqual(empty.messages, ["project.editor.clipboardEmpty"]);
  const spaces = setup({ text: " \n" });
  await spaces.compare();
  assert.equal(spaces.state.editorComparison.value.modifiedContent, " \n");
});

test("clipboard failures show a safe error and release the loading state", async () => {
  const fixture = setup({ read: () => { throw new Error("permission denied"); } });
  await fixture.compare();
  assert.deepEqual(fixture.messages, ["project.editor.clipboardReadFailed"]);
  assert.equal(fixture.state.clipboardCompareLoading.value, false);
});

test("ignores a delayed clipboard response after the active file changes", async () => {
  let resolve;
  const fixture = setup({ read: () => new Promise((done) => { resolve = done; }) });
  const pending = fixture.compare();
  await Promise.resolve();
  fixture.state.project.activePath = "/project/b.ts";
  resolve("late clipboard");
  await pending;
  assert.equal(fixture.state.editorComparison.value, null);
});

test("rejects binary documents and NUL content", async () => {
  for (const options of [{ externalKind: "binary" }, { text: "a\0b" }]) {
    const fixture = setup(options);
    await fixture.compare();
    assert.equal(fixture.state.editorComparison.value, null);
  }
});

test("does not create a comparison when file loading fails", async () => {
  const fixture = setup({ loaded: false, load: async () => {} });
  await fixture.compare();
  assert.equal(fixture.state.editorComparison.value, null);
  assert.deepEqual(fixture.messages, ["project.editor.compareOpenFileFailed"]);
});

test("offers clipboard comparison from tree, tabs, and the single-file toolbar", () => {
  assert.match(view, /@click="compareResourceNodeWithClipboard\(contextMenu.node\)"/);
  assert.match(view, /command="compare-with-clipboard"/);
  assert.match(view, /command="clipboard"/);
  assert.doesNotMatch(view, /\(!comparableOpenDocuments.length && !isComparingOpenDocument\)/);
  assert.match(view, /clipboardCompareRequest\+\+;\s*clearGitEditorTools/);
  assert.match(view, /project.editor.clipboardComparisonHint/);
});


test("ignores a delayed file load after switching to another document", async () => {
  let resolve;
  const fixture = setup({ loaded: false, load: () => new Promise((done) => { resolve = done; }) });
  const pending = fixture.compare();
  await Promise.resolve();
  await Promise.resolve();
  fixture.state.project.activePath = "/project/b.ts";
  resolve();
  await pending;
  assert.equal(fixture.state.editorComparison.value, null);
});

test("duplicate clicks do not start another clipboard read", async () => {
  let resolve;
  let calls = 0;
  const fixture = setup({ read: () => { calls++; return new Promise((done) => { resolve = done; }); } });
  const pending = fixture.compare();
  await Promise.resolve();
  await fixture.compare();
  assert.equal(calls, 1);
  resolve("text");
  await pending;
  assert.equal(fixture.state.clipboardCompareLoading.value, false);
});

test("keeps Chinese, emoji and multiline clipboard text unchanged on the right", async () => {
  const text = "修复：空结果可添加筛选条件 🧪\r\n第二行 café\n";
  const { state, document, compare } = setup({ text });
  document.content = "文件当前内容：未保存修改";
  await compare();
  assert.equal(state.editorComparison.value.originalContent, document.content);
  assert.equal(state.editorComparison.value.modifiedContent, text);
});

test("clipboard comparison labels match file-left and clipboard-right models", () => {
  const zh = readFileSync(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
  const en = readFileSync(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");
  assert.match(zh, /"project.editor.clipboardComparisonHint": "左：文件当前内容 · 右：剪贴板（只读）"/);
  assert.match(en, /"project.editor.clipboardComparisonHint": "Left: current file content · Right: clipboard \(read-only\)"/);
});
