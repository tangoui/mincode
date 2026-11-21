import assert from "node:assert/strict";
import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  isProjectDocumentDirty,
  setProjectDocumentDirty,
} from "../src/domain/projects/document-dirty.ts";

const srcRoot = fileURLToPath(new URL("../src/", import.meta.url));
const read = (relative) => readFileSync(path.join(srcRoot, relative), "utf8");
const projectView = read("views/ProjectWorkspaceView.vue");
const editor = read("components/projects/MonacoTextEditor.vue");
const lspWrapper = read("composables/use-project-language-servers.ts");
const store = read("stores/local-project.ts");

function resolveImport(from, specifier) {
  let base;
  if (specifier.startsWith("@/")) base = path.join(srcRoot, specifier.slice(2));
  else if (specifier.startsWith(".")) base = path.resolve(path.dirname(from), specifier);
  else return `package:${specifier}`;
  for (const candidate of [base, base.replace(/\.js$/, ".ts"), `${base}.ts`, `${base}.vue`, `${base}.js`, `${base}/index.ts`]) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
  }
  return `missing:${base}`;
}

// Static (non-type, non-dynamic) imports only: those are what land in the route's initial chunk graph.
function staticImports(file) {
  let code = readFileSync(file, "utf8");
  if (file.endsWith(".vue")) {
    code = [...code.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/g)].map((match) => match[1]).join("\n");
  }
  code = code.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const specifiers = [];
  for (const match of code.matchAll(/(?:^|\n)\s*(?:import|export)\s+([^;]*?)\s+from\s+["']([^"']+)["']/g)) {
    const clause = match[1].trim();
    if (/^type\s/.test(clause)) continue;
    const named = clause.match(/^\{([\s\S]*)\}$/);
    if (named && named[1].split(",").map((item) => item.trim()).filter(Boolean).every((item) => item.startsWith("type "))) {
      continue;
    }
    specifiers.push(match[2]);
  }
  for (const match of code.matchAll(/(?:^|\n)\s*import\s+["']([^"']+)["']/g)) specifiers.push(match[1]);
  return specifiers;
}

function staticGraph(entry) {
  const parents = new Map([[entry, null]]);
  const queue = [entry];
  while (queue.length) {
    const file = queue.shift();
    if (!file.startsWith(srcRoot) || !/\.(ts|vue|js|mjs)$/.test(file)) continue;
    for (const specifier of staticImports(file)) {
      const resolved = resolveImport(file, specifier);
      if (parents.has(resolved)) continue;
      parents.set(resolved, file);
      queue.push(resolved);
    }
  }
  return parents;
}

test("Project route does not statically reach Monaco", () => {
  const entry = path.join(srcRoot, "views/ProjectWorkspaceView.vue");
  const parents = staticGraph(entry);
  const monacoModules = [...parents.keys()].filter((file) => /monaco-runtime|package:monaco-editor/.test(file));
  const chains = monacoModules.map((file) => {
    const chain = [];
    for (let current = file; current; current = parents.get(current)) chain.push(current.replace(srcRoot, ""));
    return chain.join(" <- ");
  });
  assert.deepEqual(chains, []);
  assert.ok(parents.size > 20, "import graph walker should find the view's dependencies");
});

test("LSP composable is a Monaco-free facade over a lazily imported runtime", () => {
  assert.doesNotMatch(lspWrapper, /monaco-runtime|project-file-uri|monaco-commands/);
  assert.match(lspWrapper, /import type \{ ProjectLanguageServerController \} from "@\/composables\/project-language-server-runtime"/);
  assert.match(lspWrapper, /import\("@\/composables\/project-language-server-runtime"\)/);
  assert.match(lspWrapper, /onBeforeUnmount\(async \(\) => \{[\s\S]*?controller\?\.dispose\(\)/);
  assert.match(lspWrapper, /async function fetchServerList[\s\S]*?return listLspServers\(\)/);
  assert.match(projectView, /@ready="lsp\.ensureLoaded\(\)"/);
  assert.doesNotMatch(projectView, /^import GitBranchPanel /m);
});

test("Project editor keeps the Monaco model authoritative while typing", () => {
  assert.match(projectView, /const openDocumentPaths = computed\(\(\) =>\s*project\.documents\.map/);
  assert.match(projectView, /:open-paths="openDocumentPaths"/);
  assert.match(projectView, /defer-content-sync/);
  assert.match(projectView, /@sync-content="project\.updateDocumentContent"/);
  assert.match(projectView, /@dirty-change="project\.setDocumentDirty"/);
  assert.doesNotMatch(projectView, /document\.content !== document\.savedContent/);
  assert.doesNotMatch(projectView, /@update:model-value="updateActiveContent"/);
  // Readers flush pending editor content first.
  assert.match(projectView, /async function saveDocument[\s\S]*?flushEditorContent\(\);[\s\S]*?const content = document\.content;[\s\S]*?project\.acknowledgeDocumentSaved\(owner, path, session, content\)/);
  assert.match(projectView, /async function requestCloseDocument[\s\S]*?flushEditorContent\(\)/);
  assert.match(projectView, /async function saveAllDocuments[\s\S]*?flushEditorContent\(\)/);
  assert.match(projectView, /watch\(\[previewMode, editorComparison\], flushEditorContent, \{ flush: "sync" \}\)/);
  assert.match(projectView, /project\.registerDocumentContentFlusher\(flushEditorContent\)/);

  const listener = editor.match(/listener: model\.onDidChangeContent\(\(\) => \{([\s\S]*?)\n    \}\),/);
  assert.ok(listener, "deferred models install a per-model change listener");
  assert.doesNotMatch(listener[1], /getValue\(\)/);
  assert.match(listener[1], /getAlternativeVersionId\(\)/);
  assert.match(editor, /if \(!applyingExternalValue && !props\.deferContentSync\) \{\s*emit\("update:modelValue"/);
  assert.match(editor, /defineExpose\(\{[^}]*flushContent/);

  assert.match(store, /function flushDocumentContent\(\)/);
  assert.match(store, /flushDocumentContent\(\);\s*stashActiveWorkspace\(\);/);
  assert.match(store, /function renameDocuments[^{]*\{\s*flushDocumentContent\(\);/);
  assert.match(store, /const dirtyCount = computed\(\(\) => documents\.value\.filter\(isProjectDocumentDirty\)\.length\)/);
});

test("document dirtiness prefers the live flag and falls back to content comparison", () => {
  const document = { path: "/a.ts", name: "a.ts", content: "a", savedContent: "a", loaded: true, loading: false };
  assert.equal(isProjectDocumentDirty(document), false);
  document.content = "ab";
  assert.equal(isProjectDocumentDirty(document), true);
  setProjectDocumentDirty(document, false);
  assert.equal(isProjectDocumentDirty(document), false, "live flag wins over stale content");
  setProjectDocumentDirty(document, true);
  document.content = "a";
  assert.equal(isProjectDocumentDirty(document), true);
  setProjectDocumentDirty(document, undefined);
  assert.equal(isProjectDocumentDirty(document), false);
  assert.equal(isProjectDocumentDirty({ ...document, loaded: false, dirty: true }), false);
});
