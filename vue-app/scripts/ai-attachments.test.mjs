import assert from "node:assert/strict";
import test from "node:test";
import {
  createAiAttachment,
  normalizeAiAttachment,
  readAiAttachmentsFromClipboard,
  sanitizeAiAttachments,
  getAttachmentImageSrc,
} from "../src/domain/ai/attachments.ts";

const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";

function imageAttachment(data, mimeType = "image/png") {
  return {
    id: "image-1",
    name: "image.png",
    mimeType,
    size: data.length,
    kind: "image",
    data,
  };
}

test("empty image files are rejected before entering the composer", async () => {
  const emptyImage = {
    name: "empty.png",
    type: "image/png",
    size: 0,
    arrayBuffer: async () => new ArrayBuffer(0),
  };

  await assert.rejects(createAiAttachment(emptyImage), /EMPTY_IMAGE_ATTACHMENT/);

  const invalidImage = {
    name: "invalid.png",
    type: "image/png",
    size: 5,
    arrayBuffer: async () => Uint8Array.from([104, 101, 108, 108, 111]).buffer,
  };
  await assert.rejects(createAiAttachment(invalidImage), /EMPTY_IMAGE_ATTACHMENT/);
});

test("invalid historical image attachments are removed", () => {
  const textAttachment = {
    id: "text-1",
    name: "notes.txt",
    mimeType: "text/plain",
    size: 5,
    kind: "text",
    data: "hello",
  };

  const sanitized = sanitizeAiAttachments([
    imageAttachment(""),
    imageAttachment("not base64"),
    imageAttachment("aGVsbG8="),
    textAttachment,
  ]);

  assert.deepEqual(sanitized, [textAttachment]);
});

test("legacy image data URLs are normalized to raw base64", () => {
  const normalized = normalizeAiAttachment(
    imageAttachment(`data:image/jpeg;base64,${pngBase64}`, "image/jpeg"),
  );

  assert.ok(normalized);
  assert.equal(normalized.mimeType, "image/png");
  assert.equal(normalized.data, pngBase64);
  assert.equal(normalized.previewUrl, undefined);
  assert.equal(getAttachmentImageSrc(normalized), `data:image/png;base64,${pngBase64}`);
});

test("plain text paste ignores rich clipboard file representations", async () => {
  const richTextFile = {
    name: "clipboard.html",
    type: "text/html",
    size: 12,
    text: async () => "<b>hello</b>",
  };
  const clipboard = {
    getData: (type) => type === "text/plain" ? "hello" : "",
    items: [{ kind: "file", getAsFile: () => richTextFile }],
    files: [richTextFile],
  };

  assert.deepEqual(await readAiAttachmentsFromClipboard(clipboard), {
    attachments: [],
    errors: [],
  });
});

test("declared plain text paste ignores files when WebView getData is empty", async () => {
  const richTextFile = {
    name: "clipboard.html",
    type: "text/html",
    size: 12,
    text: async () => "<b>hello</b>",
  };
  const clipboard = {
    getData: () => "",
    types: ["text/plain", "text/html", "Files"],
    items: [
      { kind: "string", type: "text/plain", getAsFile: () => null },
      { kind: "file", type: "text/html", getAsFile: () => richTextFile },
    ],
    files: [richTextFile],
  };

  assert.deepEqual(await readAiAttachmentsFromClipboard(clipboard), {
    attachments: [],
    errors: [],
  });
});
