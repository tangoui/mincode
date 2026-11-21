import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { AI_ASSISTANT_DROP_TARGET_ID, readAiAttachmentsFromPaths } from "../src/domain/ai/dropped-files.ts";

const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";

function fakeFiles(files) {
  const calls = [];
  const invoke = async (command, args) => {
    calls.push(`${command}:${args.path}`);
    const file = files[args.path];
    if (!file) throw new Error("stat: no such file");
    if (command === "local_fs_inspect_external_file") return file.info;
    if (command === "local_fs_read_external_text_file") return file.text;
    if (command === "local_fs_read_external_data_url") return file.dataUrl;
    throw new Error(`unexpected ${command}`);
  };
  return { invoke, calls };
}

test("reads dropped text and image files into attachments", async () => {
  const { invoke } = fakeFiles({
    "/tmp/notes.toml": { info: { name: "notes.toml", kind: "text", mimeType: "", size: 12 }, text: "name = \"min\"" },
    "/tmp/shot.png": {
      info: { name: "shot.png", kind: "image", mimeType: "image/png", size: 70 },
      dataUrl: `data:image/png;base64,${PNG_BASE64}`,
    },
  });
  const result = await readAiAttachmentsFromPaths(["/tmp/notes.toml", "/tmp/shot.png", "/tmp/notes.toml"], invoke);
  assert.deepEqual(result.errors, []);
  assert.deepEqual(result.attachments.map(({ name, kind, mimeType }) => ({ name, kind, mimeType })), [
    { name: "notes.toml", kind: "text", mimeType: "text/plain" },
    { name: "shot.png", kind: "image", mimeType: "image/png" },
  ]);
  assert.equal(result.attachments[0].data, "name = \"min\"");
  assert.equal(result.attachments[1].data, PNG_BASE64);
});

test("rejects binaries, folders and oversized files without reading their content", async () => {
  const { invoke, calls } = fakeFiles({
    "/tmp/app.zip": { info: { name: "app.zip", kind: "binary", size: 10 } },
    "/tmp/big.log": { info: { name: "big.log", kind: "text", mimeType: "text/plain", size: 10 * 1024 * 1024 } },
    "/tmp/big.png": { info: { name: "big.png", kind: "image", mimeType: "image/png", size: 10 * 1024 * 1024 } },
  });
  const result = await readAiAttachmentsFromPaths(["/tmp/app.zip", "/tmp/big.log", "/tmp/big.png", "C:\\docs\\folder"], invoke);
  assert.deepEqual(result.attachments, []);
  assert.deepEqual(result.errors, [
    { name: "app.zip", code: "UNSUPPORTED_ATTACHMENT" },
    { name: "big.log", code: "TEXT_ATTACHMENT_TOO_LARGE" },
    { name: "big.png", code: "IMAGE_ATTACHMENT_TOO_LARGE" },
    { name: "folder", code: "UNSUPPORTED_ATTACHMENT" },
  ]);
  assert.ok(calls.every((call) => call.startsWith("local_fs_inspect_external_file:")));
});

test("the AI panel is a native drop target that the app router leaves alone", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const app = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
  assert.match(drawer, /:id="AI_ASSISTANT_DROP_TARGET_ID"[\s\S]*?data-file-drop-target/);
  assert.match(drawer, /event\?\.targetId === AI_ASSISTANT_DROP_TARGET_ID/);
  assert.match(app, /if \(event\?\.targetId === AI_ASSISTANT_DROP_TARGET_ID\) return;/);
});
