import assert from "node:assert/strict";
import test from "node:test";
import {
  createProjectDirectoryAttachment,
  createProjectDirectoryContext,
  createProjectFileAttachment,
  createProjectFileContext,
} from "../src/domain/ai/project-context.ts";

test("file context attachment includes its relative path and text content", () => {
  const attachment = createProjectFileAttachment("src/main.ts", "console.log('ok');");

  assert.equal(attachment.kind, "text");
  assert.equal(attachment.name, "src/main.ts");
  assert.equal(attachment.mimeType, "text/plain");
  assert.match(attachment.data, /Path: src\/main\.ts/);
  assert.match(attachment.data, /console\.log\('ok'\);/);
});

test("directory context attachment includes only the selected directory structure", () => {
  const attachment = createProjectDirectoryAttachment("src", [
    { path: "src/components", isDir: true },
    { path: "src/components/App.vue", isDir: false },
    { path: "src/main.ts", isDir: false },
  ]);

  assert.equal(attachment.name, "src");
  assert.match(attachment.data, /Directory: src/);
  assert.match(attachment.data, /components\//);
  assert.match(attachment.data, /App\.vue/);
  assert.doesNotMatch(attachment.data, /console\.log|file content/i);
});

test("project-root directory context uses the project name while preserving relative entries", () => {
  const attachment = createProjectDirectoryAttachment("", [
    { path: "README.md", isDir: false },
    { path: "src", isDir: true },
  ], "okcode");

  assert.equal(attachment.name, "okcode");
  assert.match(attachment.data, /Directory: \./);
  assert.match(attachment.data, /README\.md/);
});

test("large file context is compacted within the attachment budget", () => {
  const content = `HEAD\n${"x".repeat(400 * 1024)}\nTAIL`;
  const result = createProjectFileContext("logs/large.log", {
    content,
    size: new TextEncoder().encode(content).byteLength,
    truncated: false,
  });

  assert.equal(result.truncated, true);
  assert.ok(result.attachment.size <= 256 * 1024);
  assert.match(result.attachment.data, /HEAD/);
  assert.match(result.attachment.data, /attachment compacted/);
  assert.match(result.attachment.data, /TAIL/);
});

test("large directory context becomes a compact manifest instead of throwing", () => {
  const entries = Array.from({ length: 5000 }, (_, index) => ({
    path: `src/generated/deeply-nested-module-${String(index).padStart(4, "0")}/implementation-file-with-a-long-name.ts`,
    isDir: false,
  }));
  const result = createProjectDirectoryContext("src", { entries, truncated: true });

  assert.equal(result.truncated, true);
  assert.ok(result.attachment.size <= 256 * 1024);
  assert.ok((result.omittedEntries ?? 0) > 0);
  assert.match(result.attachment.data, /omitted \d+ entries/);
  assert.match(result.attachment.data, /directory scan limit/);
});

test("backend directory truncation is disclosed even when the manifest fits", () => {
  const result = createProjectDirectoryContext("src", {
    entries: [{ path: "src/main.ts", isDir: false }],
    truncated: true,
  });

  assert.equal(result.truncated, true);
  assert.match(result.attachment.data, /directory scan limit/);
});


test("same-named project contexts retain distinct source roots", () => {
  const workspaceProject = createProjectDirectoryContext("", {
    entries: [{ path: "README.md", isDir: false }],
    truncated: false,
  }, "im-pc-monorepo", "/work/im-email-service-workspace/im-pc-monorepo");
  const standaloneProject = createProjectDirectoryContext("", {
    entries: [{ path: "README.md", isDir: false }],
    truncated: false,
  }, "im-pc-monorepo", "/work/im/im-pc-monorepo");

  assert.notEqual(workspaceProject.attachment.name, standaloneProject.attachment.name);
  assert.equal(
    workspaceProject.attachment.name,
    "/work/im-email-service-workspace/im-pc-monorepo",
  );
  assert.equal(standaloneProject.attachment.name, "/work/im/im-pc-monorepo");
  assert.match(
    workspaceProject.attachment.data,
    /Project root: \/work\/im-email-service-workspace\/im-pc-monorepo/,
  );
  assert.match(
    standaloneProject.attachment.data,
    /Project root: \/work\/im\/im-pc-monorepo/,
  );
});

test("file context uses the absolute source path when a project root is provided", () => {
  const result = createProjectFileContext("src/main.ts", {
    content: "export {};",
    size: 10,
    truncated: false,
  }, "/work/im/im-pc-monorepo/");

  assert.equal(result.attachment.name, "/work/im/im-pc-monorepo/src/main.ts");
  assert.match(result.attachment.data, /Project root: \/work\/im\/im-pc-monorepo/);
  assert.match(result.attachment.data, /Path: src\/main.ts/);
});


test("project context joins Windows drive roots without duplicate separators", () => {
  const result = createProjectFileContext("src/main.ts", {
    content: "export {};",
    size: 10,
    truncated: false,
  }, "C:\\");

  assert.equal(result.attachment.name, "C:/src/main.ts");
  assert.match(result.attachment.data, /Project root: C:\//);
});
