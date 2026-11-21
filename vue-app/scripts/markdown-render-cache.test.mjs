import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import DOMPurify from "dompurify";
import { marked } from "marked";

// Node has no DOM, so stand in for DOMPurify and count how often it runs.
let sanitizeCalls = 0;
DOMPurify.sanitize = (html) => {
  sanitizeCalls += 1;
  return `<sanitized>${html}`;
};

const {
  cachedMarkdownHtml,
  clearMarkdownHtmlCache,
  renderMarkdownHtml,
} = await import("../src/services/markdown-render.ts");
const previewSource = await readFile(
  new URL("../src/components/ui/MarkdownPreview.vue", import.meta.url),
  "utf8",
);

test("渲染结果与原 marked + DOMPurify 管线一致", () => {
  clearMarkdownHtmlCache();
  const content = "# Title\n\n- a\n- b\nline one\nline two";
  const expected = `<sanitized>${marked.parse(content, { async: false, gfm: true, breaks: true })}`;
  assert.equal(renderMarkdownHtml(content), expected);
});

test("相同内容命中 LRU，不重复解析和净化", () => {
  clearMarkdownHtmlCache();
  sanitizeCalls = 0;
  const first = renderMarkdownHtml("**cached**");
  const second = renderMarkdownHtml("**cached**");
  assert.equal(first, second);
  assert.equal(sanitizeCalls, 1);
  assert.equal(cachedMarkdownHtml("**cached**"), first);
});

test("流式中间内容可跳过缓存，超大内容不缓存", () => {
  clearMarkdownHtmlCache();
  renderMarkdownHtml("partial", { memoize: false });
  assert.equal(cachedMarkdownHtml("partial"), undefined);
  const huge = "x".repeat(200_001);
  renderMarkdownHtml(huge);
  assert.equal(cachedMarkdownHtml(huge), undefined);
});

test("LRU 淘汰最久未使用的条目", () => {
  clearMarkdownHtmlCache();
  renderMarkdownHtml("entry-0");
  for (let index = 1; index < 128; index += 1) renderMarkdownHtml(`entry-${index}`);
  assert.ok(cachedMarkdownHtml("entry-0"));
  renderMarkdownHtml("entry-128");
  assert.equal(cachedMarkdownHtml("entry-1"), undefined);
  assert.ok(cachedMarkdownHtml("entry-0"));
});

test("MarkdownPreview 节流连续更新并保证尾部渲染", () => {
  assert.match(previewSource, /const MIN_RENDER_INTERVAL_MS = 1\d\d;/);
  assert.match(previewSource, /trailingRender \?\?= setTimeout\(\(\) => renderContent\(false\)/);
  assert.match(previewSource, /onBeforeUnmount\(cancelTrailingRender\)/);
  assert.doesNotMatch(previewSource, /computed\(\(\) =>\s*DOMPurify\.sanitize/);
});
