import assert from "node:assert/strict";
import test from "node:test";
import DOMPurify from "dompurify";

// Node has no DOM: stand in for DOMPurify and count how many fragments are sanitized.
let sanitizeCalls = 0;
DOMPurify.sanitize = (html) => {
  sanitizeCalls += 1;
  return html;
};

const { renderMarkdownHtml, clearMarkdownHtmlCache, cachedMarkdownHtml } = await import("../src/services/markdown-render.ts");
const { createStreamingMarkdownRenderer } = await import("../src/services/streaming-markdown-render.ts");

// Documents chosen for the block types where splitting on blank lines would go wrong.
const samples = {
  list: "说明：\n\n- 第一项\n- 第二项\n\n- 松散列表第三项\n\n结束。",
  table: "| 名称 | 值 |\n| --- | --- |\n| a | 1 |\n| b | 2 |\n\n表格后正文",
  referenceLink: "先用 [文档][ref] 再定义。\n\n更多段落。\n\n[ref]: https://example.com/doc",
  fence: "看代码：\n\n```go\nfunc main() {\n\n\tprintln(\"hi\")\n}\n```\n\n完。",
  setext: "标题\n===\n\n正文第一行\n第二行",
  html: "<details>\n<summary>更多</summary>\n\n内容\n\n</details>\n\n后续",
  indentedCode: "段落\n\n    缩进代码\n\n    第二段缩进\n\n结尾",
  quote: "> 引用一\n>\n> 引用二\n\n正文",
};

function full(content) {
  return renderMarkdownHtml(content, { memoize: false });
}

test("每个前缀的增量结果都与整篇渲染一致", () => {
  for (const [name, content] of Object.entries(samples)) {
    const renderer = createStreamingMarkdownRenderer();
    for (let end = 1; end <= content.length; end += 1) {
      const prefix = content.slice(0, end);
      assert.equal(renderer.render(prefix), full(prefix), `${name} @ ${end}: ${JSON.stringify(prefix)}`);
    }
  }
});

test("稳定的块只渲染一次，之后只重渲染最后一块", () => {
  clearMarkdownHtmlCache();
  const renderer = createStreamingMarkdownRenderer();
  const head = "第一段\n\n第二段\n\n第三段正在写";
  renderer.render(head);
  sanitizeCalls = 0;
  renderer.render(`${head}入`);
  assert.equal(sanitizeCalls, 1);
});

test("引用式链接定义晚到时，已缓存的段落会重新渲染成链接", () => {
  const renderer = createStreamingMarkdownRenderer();
  const before = "先用 [文档][ref]。\n\n";
  assert.doesNotMatch(renderer.render(before), /<a /);
  const after = `${before}[ref]: https://example.com/doc`;
  assert.match(renderer.render(after), /<a href="https:\/\/example\.com\/doc">文档<\/a>/);
  assert.equal(renderer.render(after), full(after));
});

test("内容被替换（不是追加）时整体重新计算", () => {
  const renderer = createStreamingMarkdownRenderer();
  renderer.render("旧的第一段\n\n旧的第二段");
  assert.equal(renderer.render("全新内容\n\n- 列表"), full("全新内容\n\n- 列表"));
});

test("缓存不进入全局 LRU", () => {
  clearMarkdownHtmlCache();
  const content = "只在流式渲染器里\n\n第二段";
  createStreamingMarkdownRenderer().render(content);
  assert.equal(cachedMarkdownHtml(content), undefined);
  assert.equal(cachedMarkdownHtml("只在流式渲染器里"), undefined);
});
