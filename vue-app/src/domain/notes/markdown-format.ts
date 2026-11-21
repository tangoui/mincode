/**
 * 笔记编辑器的 Markdown 源码格式化（工具栏与快捷键）。
 *
 * 纯函数：输入文档字符串与选区，输出一次替换 {from, to, insert} 及替换后的选区，
 * 不依赖 Vue / DOM，便于测试。移植自 official-plugins/knowledge-base/ui-src/markdown-format.mjs，
 * 去掉了长度上限参数；占位文字由调用方从 i18n 传入。
 */

export type MarkdownFormatKind =
  | "heading"
  | "bold"
  | "italic"
  | "code"
  | "codeBlock"
  | "bulletList"
  | "orderedList"
  | "taskList"
  | "quote"
  | "link"
  | "table"
  | "rule";

/** 占位文字：空选区套用行内格式 / 链接时的文本，以及表格列名前缀（「列 1」「Column 1」）。 */
export interface MarkdownFormatLabels {
  text: string;
  column: string;
}

/** 一次替换及替换后的选区（anchor / head 为替换后文档中的位置）。 */
export interface MarkdownEdit {
  from: number;
  to: number;
  insert: string;
  anchor: number;
  head: number;
}

type InlineKind = "bold" | "italic" | "code";
type LineKind = "heading" | "bulletList" | "taskList" | "orderedList" | "quote";

interface LineRule {
  /** 行首已有的同族块标记；套用本格式时被替换。 */
  existing: RegExp;
  isSame: (line: string) => boolean;
  prefix: (index: number) => string;
}

const INLINE: Record<InlineKind, string> = { bold: "**", italic: "*", code: "`" };
const HEADING = /^#{1,6} /;
const LIST = /^(?:[-*+] \[[ xX]\] |[-*+] |\d+[.)] )/;
const QUOTE = /^> ?/;
const LINE_KINDS: Record<LineKind, LineRule> = {
  heading: { existing: HEADING, isSame: (line) => /^## /.test(line), prefix: () => "## " },
  bulletList: { existing: LIST, isSame: (line) => /^[-*+] (?!\[[ xX]\] )/.test(line), prefix: () => "- " },
  taskList: { existing: LIST, isSame: (line) => /^[-*+] \[[ xX]\] /.test(line), prefix: () => "- [ ] " },
  orderedList: { existing: LIST, isSame: (line) => /^\d+[.)] /.test(line), prefix: (index) => `${index + 1}. ` },
  quote: { existing: QUOTE, isSame: (line) => QUOTE.test(line), prefix: () => "> " },
};

function isInlineKind(kind: string): kind is InlineKind {
  return Object.prototype.hasOwnProperty.call(INLINE, kind);
}

function isLineKind(kind: string): kind is LineKind {
  return Object.prototype.hasOwnProperty.call(LINE_KINDS, kind);
}

function lineRange(doc: string, from: number, to: number) {
  const start = from === 0 ? 0 : doc.lastIndexOf("\n", from - 1) + 1;
  // 选区止于某行行首时不包含该行。
  const effectiveTo = to > from && doc[to - 1] === "\n" ? to - 1 : to;
  const nextBreak = doc.indexOf("\n", Math.max(effectiveTo, start));
  return { start, end: nextBreak === -1 ? doc.length : nextBreak };
}

function inline(doc: string, from: number, to: number, marker: string, labels: MarkdownFormatLabels): MarkdownEdit {
  const before = doc.slice(Math.max(0, from - marker.length), from);
  const after = doc.slice(to, to + marker.length);
  const wrapped = from < to && before === marker && after === marker;
  // ** 包含 *：粗体包裹的选区不视为斜体包裹。
  if (wrapped && !(marker === "*" && doc.slice(from - 2, from) === "**" && doc.slice(to, to + 2) === "**")) {
    const start = from - marker.length;
    return { from: start, to: to + marker.length, insert: doc.slice(from, to), anchor: start, head: start + (to - from) };
  }
  const text = from < to ? doc.slice(from, to) : labels.text;
  return { from, to, insert: marker + text + marker, anchor: from + marker.length, head: from + marker.length + text.length };
}

function prefixLines(doc: string, from: number, to: number, rule: LineRule): MarkdownEdit {
  const { start, end } = lineRange(doc, from, to);
  const lines = doc.slice(start, end).split("\n");
  // 所有行都已是本格式时整体取消；否则每行套用本格式，并替换同族的标题 / 列表 / 引用标记。
  const remove = lines.every((line) => rule.isSame(line));
  const next = lines
    .map((line, index) => {
      const body = line.replace(rule.existing, "");
      return remove ? body : rule.prefix(index) + body;
    })
    .join("\n");
  return { from: start, to: end, insert: next, anchor: start, head: start + next.length };
}

function block(doc: string, from: number, to: number, content: string, cursorOffset = content.length): MarkdownEdit {
  const lead = from > 0 && doc[from - 1] !== "\n" ? "\n" : "";
  const position = from + lead.length + cursorOffset;
  return { from, to, insert: lead + content, anchor: position, head: position };
}

/**
 * 对 [from, to) 选区套用一种格式；未知格式返回 null。
 * 行内格式在选区已被同一标记包裹时取消包裹；行格式在所有选中行都已是该格式时取消。
 */
export function formatMarkdown(
  doc: string,
  from: number,
  to: number,
  kind: MarkdownFormatKind,
  labels: MarkdownFormatLabels,
): MarkdownEdit | null {
  if (isInlineKind(kind)) return inline(doc, from, to, INLINE[kind], labels);
  if (isLineKind(kind)) return prefixLines(doc, from, to, LINE_KINDS[kind]);
  switch (kind) {
    case "codeBlock": {
      const text = from < to ? doc.slice(from, to) : "";
      const lead = from > 0 && doc[from - 1] !== "\n" ? "\n" : "";
      const tail = to < doc.length && doc[to] !== "\n" ? "\n" : "";
      const cursor = from + lead.length + 4;
      return { from, to, insert: `${lead}\`\`\`\n${text}\n\`\`\`${tail}`, anchor: cursor, head: cursor + text.length };
    }
    case "link": {
      const text = from < to ? doc.slice(from, to) : labels.text;
      const url = "https://";
      const anchor = from + text.length + 3;
      return { from, to, insert: `[${text}](${url})`, anchor, head: anchor + url.length };
    }
    case "table": {
      const head = `| ${labels.column} 1 | ${labels.column} 2 |\n| --- | --- |\n| `;
      return block(doc, from, to, `\n${head} |  |\n`, head.length + 1);
    }
    case "rule":
      return block(doc, from, to, "\n---\n");
    default:
      return null;
  }
}
