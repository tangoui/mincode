import test from "node:test";
import assert from "node:assert/strict";
import { EditorState } from "@codemirror/state";
import { formatMarkdown } from "@/domain/notes/markdown-format";

// Migrated from official-plugins/knowledge-base/ui-src/markdown-format.test.mjs (the `max`
// length parameter was dropped; labels are now always supplied by the caller from i18n).

const labels = { text: "文本", column: "列" };
const format = (doc, from, to, kind, custom = labels) => formatMarkdown(doc, from, to, kind, custom);

// Apply through a real CodeMirror transaction: it rejects inverted or out-of-range
// changes and selections that a plain string splice would silently accept.
const apply = (doc, change) => {
  const state = EditorState.create({ doc });
  return state.update({ changes: { from: change.from, to: change.to, insert: change.insert }, selection: { anchor: change.anchor, head: change.head } }).state.doc.toString();
};

test("bold on a whole-document selection wraps it and keeps abc selected", () => {
  const change = format("abc", 0, 3, "bold");
  const next = apply("abc", change);
  assert.equal(next, "**abc**");
  assert.equal(next.slice(change.anchor, change.head), "abc");
  assert.deepEqual([change.anchor, change.head], [2, 5]);
});

test("bold wraps the selection and keeps it selected", () => {
  const change = format("abc", 1, 2, "bold");
  assert.equal(apply("abc", change), "a**b**c");
  assert.deepEqual([change.anchor, change.head], [3, 4]);
});

test("empty selection inserts a placeholder and selects it for typing", () => {
  const change = format("x", 1, 1, "italic");
  const next = apply("x", change);
  assert.equal(next, "x*文本*");
  assert.equal(next.slice(change.anchor, change.head), "文本");
});

test("wrapping an already wrapped selection removes the markers", () => {
  const change = format("a**b**c", 3, 4, "bold");
  assert.equal(apply("a**b**c", change), "abc");
});

test("inline code wraps selection", () => {
  assert.equal(apply("run npm", format("run npm", 4, 7, "code")), "run `npm`");
});

test("heading toggles a level-2 prefix on the current line", () => {
  const doc = "one\ntwo";
  const withHeading = apply(doc, format(doc, 5, 5, "heading"));
  assert.equal(withHeading, "one\n## two");
  assert.equal(apply(withHeading, format(withHeading, 8, 8, "heading")), doc);
});

test("bullet list prefixes every selected line and toggles off", () => {
  const doc = "a\nb\nc";
  const once = apply(doc, format(doc, 0, 3, "bulletList"));
  assert.equal(once, "- a\n- b\nc");
  assert.equal(apply(once, format(once, 0, 7, "bulletList")), doc);
});

test("ordered list numbers lines incrementally", () => {
  const doc = "a\nb";
  assert.equal(apply(doc, format(doc, 0, 3, "orderedList")), "1. a\n2. b");
});

test("task list and quote prefix lines", () => {
  assert.equal(apply("todo", format("todo", 0, 0, "taskList")), "- [ ] todo");
  assert.equal(apply("a\nb", format("a\nb", 0, 3, "quote")), "> a\n> b");
});

test("code block fences the selection on its own lines", () => {
  const doc = "before code after";
  assert.equal(apply(doc, format(doc, 7, 11, "codeBlock")), "before \n```\ncode\n```\n after");
});

test("link wraps text and selects the url placeholder", () => {
  const change = format("see docs", 4, 8, "link");
  const next = apply("see docs", change);
  assert.equal(next, "see [docs](https://)");
  assert.equal(next.slice(change.anchor, change.head), "https://");
});

test("table and rule start on a new line", () => {
  const table = apply("text", format("text", 4, 4, "table"));
  assert.match(table, /^text\n\n\| 列 1 \| 列 2 \|\n\| --- \| --- \|\n\|  \|  \|\n$/);
  assert.equal(apply("text", format("text", 4, 4, "rule")), "text\n\n---\n");
});

test("changes are no longer refused by a length limit", () => {
  const doc = "a".repeat(200_000);
  const change = format(doc, 0, doc.length, "bold");
  assert.notEqual(change, null);
  assert.equal(apply(doc, change).length, doc.length + 4);
});

test("unknown format kinds do nothing", () => {
  assert.equal(format("abc", 0, 1, "strike"), null);
});

test("line formats work when the first line is empty and the cursor is at 0", () => {
  for (const kind of ["heading", "bulletList", "orderedList", "taskList", "quote"]) {
    const change = format("\nabc", 0, 0, kind);
    assert.ok(change.from <= change.to, `${kind} range is ordered`);
    assert.notEqual(apply("\nabc", change), null);
  }
  assert.equal(apply("\nabc", format("\nabc", 0, 0, "heading")), "## \nabc");
});

test("heading replaces an existing heading level instead of stacking", () => {
  assert.equal(apply("# T", format("# T", 0, 0, "heading")), "## T");
  assert.equal(apply("### T", format("### T", 0, 0, "heading")), "## T");
});

test("list kinds convert between each other instead of stacking markers", () => {
  assert.equal(apply("- [ ] x", format("- [ ] x", 0, 0, "bulletList")), "- x");
  assert.equal(apply("- x", format("- x", 0, 0, "taskList")), "- [ ] x");
  assert.equal(apply("- x", format("- x", 0, 0, "orderedList")), "1. x");
  assert.equal(apply("1. x", format("1. x", 0, 0, "bulletList")), "- x");
});

test("placeholder text follows the provided labels", () => {
  const english = { text: "text", column: "Column" };
  assert.equal(apply("", format("", 0, 0, "italic", english)), "*text*");
  assert.match(apply("", format("", 0, 0, "table", english)), /\| Column 1 \| Column 2 \|/);
});
