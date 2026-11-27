import assert from "node:assert/strict";
import test from "node:test";
import DOMPurify from "dompurify";

// Node has no DOM; mimic DOMPurify's real behaviour of dropping file: URLs from src.
DOMPurify.sanitize = (html) => html.replace(/ src="file:[^"]*"/g, "");

const { clearMarkdownHtmlCache, fileUrlToLocalPath, renderMarkdownHtml } = await import("../src/services/markdown-render.ts");

test("file URL 转为本地路径", () => {
  assert.equal(fileUrlToLocalPath("file:///Users/a/random_images/1_checker.png"), "/Users/a/random_images/1_checker.png");
  assert.equal(fileUrlToLocalPath("file:///tmp/%E4%B8%AD%E6%96%87%20image.png"), "/tmp/中文 image.png");
  assert.equal(fileUrlToLocalPath("file:///C:/Users/Test/a.png"), "C:/Users/Test/a.png");
  assert.equal(fileUrlToLocalPath("file://server/share/a.png"), undefined);
  assert.equal(fileUrlToLocalPath("https://example.test/a.png"), undefined);
});

test("AI 回复中 file: 图片在消毒前改成本地路径，保留 src", () => {
  clearMarkdownHtmlCache();
  const html = renderMarkdownHtml("![1](file:///Users/a/random_images/1_checker.png)", { aiFileLinks: true });
  assert.match(html, /<img src="\/Users\/a\/random_images\/1_checker\.png" alt="1"/);
});

test("非 AI 文档不改写 file: 图片", () => {
  clearMarkdownHtmlCache();
  const html = renderMarkdownHtml("![1](file:///Users/a/b.png)");
  assert.doesNotMatch(html, /src=/);
});
