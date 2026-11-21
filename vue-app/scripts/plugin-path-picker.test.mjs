import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { MAX_PICKED_PATHS, filterPickedPaths, parsePathPickRequest, pathPickNeedsConfirmation } from "../src/domain/plugin-ui/path-picker.mjs";

test("legacy single requests stay single", () => {
  assert.deepEqual(parsePathPickRequest({ id: "r1", kind: "file" }), { id: "r1", kind: "file", multiple: false, extensions: [] });
  assert.deepEqual(parsePathPickRequest({ id: "r2", kind: "directory" }), { id: "r2", kind: "directory", multiple: false, extensions: [] });
});

test("multiple only applies to files", () => {
  assert.equal(parsePathPickRequest({ id: "r1", kind: "directory", multiple: true })?.multiple, false);
  assert.equal(parsePathPickRequest({ id: "r1", kind: "file", multiple: true })?.multiple, true);
  assert.equal(parsePathPickRequest({ id: "r1", kind: "file", multiple: "yes" })?.multiple, false);
});

test("extensions use the fileDrop rules", () => {
  assert.deepEqual(parsePathPickRequest({ id: "r", kind: "file", extensions: [".EPUB", "chm", ".txt", ".a-b", 7] })?.extensions, [".epub", ".txt"]);
  assert.equal(parsePathPickRequest({ id: "r", kind: "file", extensions: Array(33).fill(".txt") }), undefined);
  assert.equal(parsePathPickRequest({ id: "r", kind: "file", extensions: ".txt" }), undefined);
});

test("invalid ids and kinds are rejected", () => {
  assert.equal(parsePathPickRequest({ id: "bad id", kind: "file" }), undefined);
  assert.equal(parsePathPickRequest({ id: "r", kind: "any" }), undefined);
  assert.equal(parsePathPickRequest(null), undefined);
});

test("picked paths are filtered and capped", () => {
  assert.deepEqual(filterPickedPaths(["/a/x.EPUB", "/a/y.pdf", "/a/z.txt", "", 3], [".epub", ".txt"], 10), ["/a/x.EPUB", "/a/z.txt"]);
  assert.deepEqual(filterPickedPaths(["/a/x", "C:\\b\\y.chm"], [], 10), ["/a/x", "C:\\b\\y.chm"]);
  assert.equal(MAX_PICKED_PATHS, 1000);
  assert.equal(filterPickedPaths(Array(1200).fill("/a/x.txt"), [], MAX_PICKED_PATHS).length, 1000);
});

test("the host frame and transport pass multi-select through", async () => {
  const frame = await readFile(new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url), "utf8");
  assert.match(frame, /parsePathPickRequest\(data\)/);
  assert.match(frame, /chooseNativeFiles\(/);
  assert.match(frame, /filterPickedPaths\(/);
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  assert.match(transport, /pickPath\(kind, options\)/);
  const bridge = await readFile(new URL("../src/services/desktop-bridge.ts", import.meta.url), "utf8");
  assert.match(bridge, /export async function chooseNativeFiles\(/);
  assert.match(bridge, /AllowsMultipleSelection: true/);
});

test("a click just before the request skips the host confirmation; anything else confirms", () => {
  assert.equal(pathPickNeedsConfirmation({ isActive: true }), false);
  assert.equal(pathPickNeedsConfirmation({ isActive: false, hasBeenActive: true }), true);
  assert.equal(pathPickNeedsConfirmation(undefined), true);
  assert.equal(pathPickNeedsConfirmation({ isActive: "yes" }), true);
});
