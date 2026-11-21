import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { acceptDrop, parseFileDrop } from "../src/domain/plugin-ui/file-drop.mjs";

const images = parseFileDrop({ mode: "paths", extensions: [".jpg", ".png"], directories: true, maxEntries: 2 });

test("fileDrop declarations are parsed defensively", () => {
  assert.deepEqual(images, { mode: "paths", extensions: [".jpg", ".png"], directories: true, maxEntries: 2 });
  assert.equal(parseFileDrop(undefined), undefined);
  assert.equal(parseFileDrop({ mode: "anything", maxEntries: 4 }), undefined);
  assert.equal(parseFileDrop({ mode: "paths" }), undefined);
  assert.deepEqual(parseFileDrop({ mode: "contents", directories: true, extensions: ["PNG", ".txt"], maxEntries: 4 }),
    { mode: "contents", extensions: [".txt"], directories: false, maxEntries: 4 });
});

test("path drops keep declared extensions and folders only", () => {
  const entries = [
    { path: "/a/One.JPG", name: "One.JPG", kind: "file", size: 3 },
    { path: "/a/notes.txt", name: "notes.txt", kind: "file", size: 3 },
    { path: "/a/album", name: "album", kind: "directory", size: 0 },
    { path: "/a/.png", name: ".png", kind: "file", size: 1 },
    { path: "", name: "x.png", kind: "file", size: 1 },
    { path: "/a/socket.png", name: "socket.png", kind: "other", size: 0 },
  ];
  assert.deepEqual(acceptDrop(images, { entries }), { ok: true, entries: [entries[0], entries[2]] });
  const noFolders = parseFileDrop({ mode: "paths", extensions: [".jpg"], maxEntries: 10 });
  assert.deepEqual(acceptDrop(noFolders, { entries }), { ok: true, entries: [entries[0]] });
  assert.deepEqual(acceptDrop(images, { entries: [entries[1]] }), { ok: false, reason: "none" });
  assert.deepEqual(acceptDrop(images, { entries: [entries[0], entries[0], entries[2]] }), { ok: false, reason: "tooMany", max: 2 });
  assert.deepEqual(acceptDrop(images, { files: [] }), { ok: false, reason: "invalid" });
  assert.deepEqual(acceptDrop(undefined, { entries }), { ok: false, reason: "invalid" });
});

test("content drops are bounded and never carry paths", () => {
  const viewer = parseFileDrop({ mode: "contents", maxEntries: 4 });
  const file = { name: "a.png", size: 3, lastModified: 1, data: "YWJj", path: "/secret/a.png" };
  assert.deepEqual(acceptDrop(viewer, { files: [file] }), { ok: true, files: [{ name: "a.png", size: 3, lastModified: 1, data: "YWJj" }] });
  assert.deepEqual(acceptDrop(viewer, { files: [{ ...file, size: 9 * 1024 * 1024 }] }), { ok: false, reason: "invalid" });
  assert.deepEqual(acceptDrop(viewer, { entries: [] }), { ok: false, reason: "invalid" });
  const textOnly = parseFileDrop({ mode: "contents", extensions: [".txt"], maxEntries: 1 });
  assert.deepEqual(acceptDrop(textOnly, { files: [file] }), { ok: false, reason: "none" });
});

test("the host frame no longer hard-codes a plugin for native drops", async () => {
  const frame = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  const workspace = await readFile(new URL("../src/views/PluginWorkspaceView.vue", import.meta.url), "utf8");
  assert.doesNotMatch(frame + workspace, /file-viewer/);
  assert.match(frame, /plugin-drop-\$\{fileDrop\.value\.mode\}-\$\{props\.dropTargetId\}/);
  assert.match(frame, /!props\.document/);
});
