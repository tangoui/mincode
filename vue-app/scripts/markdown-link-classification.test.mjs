import assert from "node:assert/strict";
import test from "node:test";
import { classifyMarkdownLinkHref } from "../src/domain/markdown-links.ts";

test("absolute http(s) links are routed to the workspace browser", () => {
  assert.deepEqual(classifyMarkdownLinkHref("https://example.com/a?b=1"), {
    kind: "http",
    url: "https://example.com/a?b=1",
  });
  assert.deepEqual(
    classifyMarkdownLinkHref("http://giving-helpful-turning-william.trycloudflare.com/v1/chat/completions"),
    {
      kind: "http",
      url: "http://giving-helpful-turning-william.trycloudflare.com/v1/chat/completions",
    }
  );
  assert.deepEqual(classifyMarkdownLinkHref("  https://example.com/Path  "), {
    kind: "http",
    url: "https://example.com/Path",
  });
});

test("in-page anchors keep their default scroll behaviour", () => {
  assert.deepEqual(classifyMarkdownLinkHref("#usage"), { kind: "anchor" });
});

test("local project paths are routed to local navigation", () => {
  for (const path of [
    "./README.md",
    "docs/notes.md",
    "/absolute/local/path.md",
    "C:\\projects\\mincode\\README.md",
    "file:///Volumes/work/mincode/readme.md",
  ]) {
    assert.deepEqual(classifyMarkdownLinkHref(path), { kind: "local", path }, `href: ${path}`);
  }
});

test("unsupported or unsafe link schemes are blocked", () => {
  for (const href of [
    null,
    undefined,
    "",
    "   ",
    "mailto:someone@example.com",
    "javascript:alert(1)",
    "data:text/html,hello",
    "https://",
  ]) {
    assert.deepEqual(classifyMarkdownLinkHref(href), { kind: "blocked" }, `href: ${JSON.stringify(href)}`);
  }
});
