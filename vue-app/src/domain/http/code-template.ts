/**
 * httpCodeGenerators templates (`.tmpl`): the logic-less Mustache subset of
 * plugins/api/codetmpl/codetmpl.go, mirrored exactly (same error
 * codes, standalone-line rule, filters and output cap). The Go host and CLI
 * validate a template at install; the WebView re-parses it and renders it
 * against an already redacted model (domain/http/code-generator-model.ts).
 * The shared corpus tools/verify/fixtures/codetmpl keeps both in step.
 *
 * Grammar: `{{name}}`, `{{name|filter}}`, `{{#section}}…{{/section}}`,
 * `{{^section}}…{{/section}}`, `{{! comment}}`. Rejected: `{{{`, `{{&`,
 * `{{>`, `{{=`, whitespace inside tags, unknown names or filters, item
 * variables and first/last outside a list section, nested list sections,
 * more than `maxDepth` nested sections and more than `maxNodes` nodes.
 * Rendering produces plain text only; callers show it in a `<pre>` (never
 * as HTML).
 */

export interface CodeTemplateLimits {
  maxNodes: number;
  maxDepth: number;
}

export interface CodeTemplateVocabulary {
  variables: readonly string[];
  itemVariables: readonly string[];
  listSections: readonly string[];
  flagSections: readonly string[];
  itemFlagSections: readonly string[];
  filters: readonly string[];
}

export interface CodeItem {
  name: string;
  value: string;
}

export interface CodeModel {
  vars?: Record<string, string>;
  lists?: Record<string, CodeItem[]>;
  flags?: Record<string, boolean>;
}

export type CodeTemplateErrorCode =
  | "syntax"
  | "forbidden_tag"
  | "unknown_variable"
  | "unknown_filter"
  | "unknown_section"
  | "outside_section"
  | "nested_list"
  | "unclosed_section"
  | "mismatched_section"
  | "depth_exceeded"
  | "too_many_nodes"
  | "output_too_large"
  | "unknown_escape";

export class CodeTemplateError extends Error {
  readonly code: CodeTemplateErrorCode;
  readonly line: number;

  constructor(code: CodeTemplateErrorCode, line: number, message: string) {
    super(line > 0 ? `codetmpl: ${code} (line ${line}): ${message}` : `codetmpl: ${code}: ${message}`);
    this.name = "CodeTemplateError";
    this.code = code;
    this.line = line;
  }
}

const OPEN = "{{";
const CLOSE = "}}";
const KIND_TEXT = 0;
const KIND_VAR = 1;
const KIND_SECTION = 2;
const KIND_INVERTED = 3;
const KIND_CLOSE = 4;
const KIND_COMMENT = 5;
const FILTERS = ["raw", "json", "jsonInner", "shell", "powershell", "cstring", "xml", "url", "upper", "lower"];
const HEX = "0123456789ABCDEF";

interface Token {
  kind: number;
  text: string;
  filter: string;
  line: number;
}

interface TemplateNode {
  kind: number;
  text: string;
  filter: string;
  list: boolean;
  children: TemplateNode[];
}

/** A validated template; render it with {@link renderCodeTemplate}. */
export interface CompiledCodeTemplate {
  readonly nodes: readonly TemplateNode[];
}

function isName(name: string): boolean {
  if (!name || name.length > 64) return false;
  for (let index = 0; index < name.length; index += 1) {
    const char = name[index]!;
    const letter = (char >= "a" && char <= "z") || (char >= "A" && char <= "Z");
    if (!letter && (index === 0 || char < "0" || char > "9")) return false;
  }
  return true;
}

function countNewlines(text: string): number {
  let count = 0;
  for (let index = text.indexOf("\n"); index >= 0; index = text.indexOf("\n", index + 1)) count += 1;
  return count;
}

function tokenize(source: string): Token[] {
  const tokens: Token[] = [];
  let line = 1;
  let src = source;
  while (src.length > 0) {
    const start = src.indexOf(OPEN);
    if (start < 0) {
      tokens.push({ kind: KIND_TEXT, text: src, filter: "", line });
      break;
    }
    if (start > 0) {
      tokens.push({ kind: KIND_TEXT, text: src.slice(0, start), filter: "", line });
      line += countNewlines(src.slice(0, start));
    }
    const rest = src.slice(start + OPEN.length);
    if (rest !== "" && "{&>=".includes(rest[0]!)) {
      throw new CodeTemplateError("forbidden_tag", line, `"${OPEN}${rest[0]}" tags are not supported`);
    }
    const end = rest.indexOf(CLOSE);
    if (end < 0) throw new CodeTemplateError("syntax", line, "unterminated tag");
    const content = rest.slice(0, end);
    if (content.includes(OPEN)) throw new CodeTemplateError("syntax", line, `nested "${OPEN}" inside a tag`);
    const token: Token = { kind: KIND_VAR, text: "", filter: "", line };
    if (content.startsWith("!")) token.kind = KIND_COMMENT;
    else if (content.startsWith("#")) [token.kind, token.text] = [KIND_SECTION, content.slice(1)];
    else if (content.startsWith("^")) [token.kind, token.text] = [KIND_INVERTED, content.slice(1)];
    else if (content.startsWith("/")) [token.kind, token.text] = [KIND_CLOSE, content.slice(1)];
    else {
      const bar = content.indexOf("|");
      token.text = bar >= 0 ? content.slice(0, bar) : content;
      if (bar >= 0) {
        const filter = content.slice(bar + 1);
        if (!isName(filter)) throw new CodeTemplateError("syntax", line, `invalid filter "${filter}"`);
        token.filter = filter;
      }
    }
    if (token.kind !== KIND_COMMENT && !isName(token.text)) {
      throw new CodeTemplateError("syntax", line, `invalid tag "${OPEN}${content}${CLOSE}" (names are letters and digits, without spaces)`);
    }
    line += countNewlines(content);
    tokens.push(token);
    src = rest.slice(end + CLOSE.length);
  }
  return stripStandalone(tokens);
}

function isBlank(text: string): boolean {
  return /^[ \t]*$/.test(text);
}

function trimBlanksLeft(text: string): string {
  return text.replace(/^[ \t]+/, "");
}

/** Removes lines holding only a section/inverted/close/comment tag (as in Mustache). */
function stripStandalone(tokens: Token[]): Token[] {
  const standalone = tokens.map(() => false);
  tokens.forEach((token, index) => {
    if (token.kind === KIND_TEXT || token.kind === KIND_VAR) return;
    let lineStart = index === 0;
    const previous = tokens[index - 1];
    if (previous?.kind === KIND_TEXT) {
      const newline = previous.text.lastIndexOf("\n");
      lineStart = isBlank(previous.text.slice(newline + 1)) && (newline >= 0 || index === 1);
    }
    if (!lineStart) return;
    let lineEnd = index === tokens.length - 1;
    const next = tokens[index + 1];
    if (next?.kind === KIND_TEXT) {
      const text = trimBlanksLeft(next.text);
      lineEnd = text.startsWith("\n") || text.startsWith("\r\n") || (text === "" && index + 1 === tokens.length - 1);
    }
    standalone[index] = lineEnd;
  });
  tokens.forEach((_, index) => {
    if (!standalone[index]) return;
    const previous = tokens[index - 1];
    if (previous?.kind === KIND_TEXT) previous.text = previous.text.slice(0, previous.text.lastIndexOf("\n") + 1);
    const next = tokens[index + 1];
    if (next?.kind === KIND_TEXT) {
      let text = trimBlanksLeft(next.text);
      if (text.startsWith("\r")) text = text.slice(1);
      next.text = text.startsWith("\n") ? text.slice(1) : text;
    }
  });
  return tokens.filter((token) => token.kind !== KIND_COMMENT && !(token.kind === KIND_TEXT && token.text === ""));
}

interface Frame {
  name: string;
  line: number;
  list: boolean;
  kind: number;
  nodes: TemplateNode[];
}

/** Validates `source` against the limits and vocabulary; throws CodeTemplateError. */
export function compileCodeTemplate(
  source: string,
  limits: CodeTemplateLimits,
  vocabulary: CodeTemplateVocabulary,
): CompiledCodeTemplate {
  const tokens = tokenize(source);
  const stack: Frame[] = [{ name: "", line: 0, list: false, kind: KIND_TEXT, nodes: [] }];
  let count = 0;
  let inList = false;
  for (const token of tokens) {
    count += 1;
    if (count > limits.maxNodes) throw new CodeTemplateError("too_many_nodes", token.line, `template exceeds ${limits.maxNodes} nodes`);
    const top = stack[stack.length - 1]!;
    switch (token.kind) {
      case KIND_TEXT:
        top.nodes.push({ kind: KIND_TEXT, text: token.text, filter: "", list: false, children: [] });
        break;
      case KIND_VAR:
        if (vocabulary.variables.includes(token.text)) {
          // plain variable
        } else if (vocabulary.itemVariables.includes(token.text)) {
          if (!inList) throw new CodeTemplateError("outside_section", token.line, `{{${token.text}}} is only valid inside a list section`);
        } else {
          throw new CodeTemplateError("unknown_variable", token.line, `unknown variable "${token.text}"`);
        }
        if (token.filter && !vocabulary.filters.includes(token.filter)) {
          throw new CodeTemplateError("unknown_filter", token.line, `unknown filter "${token.filter}"`);
        }
        top.nodes.push({ kind: KIND_VAR, text: token.text, filter: token.filter, list: false, children: [] });
        break;
      case KIND_SECTION:
      case KIND_INVERTED: {
        const list = vocabulary.listSections.includes(token.text);
        if (list) {
          if (inList) throw new CodeTemplateError("nested_list", token.line, `list section "${token.text}" cannot be nested in another list section`);
        } else if (vocabulary.flagSections.includes(token.text)) {
          // flag section
        } else if (vocabulary.itemFlagSections.includes(token.text)) {
          if (!inList) throw new CodeTemplateError("outside_section", token.line, `section "${token.text}" is only valid inside a list section`);
        } else {
          throw new CodeTemplateError("unknown_section", token.line, `unknown section "${token.text}"`);
        }
        if (stack.length > limits.maxDepth) throw new CodeTemplateError("depth_exceeded", token.line, `sections nest deeper than ${limits.maxDepth}`);
        const opensList = list && token.kind === KIND_SECTION;
        stack.push({ name: token.text, line: token.line, list: opensList, kind: token.kind, nodes: [] });
        if (opensList) inList = true;
        break;
      }
      case KIND_CLOSE: {
        if (stack.length === 1) throw new CodeTemplateError("mismatched_section", token.line, `{{/${token.text}}} closes no open section`);
        if (top.name !== token.text) throw new CodeTemplateError("mismatched_section", token.line, `{{/${token.text}}} does not close {{${top.name}}}`);
        stack.pop();
        if (top.list) inList = false;
        stack[stack.length - 1]!.nodes.push({
          kind: top.kind,
          text: top.name,
          filter: "",
          list: vocabulary.listSections.includes(top.name),
          children: top.nodes,
        });
        break;
      }
    }
  }
  if (stack.length > 1) {
    const open = stack[stack.length - 1]!;
    throw new CodeTemplateError("unclosed_section", open.line, `section "${open.name}" is not closed`);
  }
  return { nodes: stack[0]!.nodes };
}

function utf8Length(text: string): number {
  let length = 0;
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    length += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
  }
  return length;
}

function hasOwn(record: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key);
}

interface RenderState {
  model: CodeModel;
  escape: string;
  maxBytes: number;
  out: string[];
  bytes: number;
  item: CodeItem | null;
  first: boolean;
  last: boolean;
}

function write(state: RenderState, text: string) {
  const length = utf8Length(text);
  if (state.maxBytes > 0 && state.bytes + length > state.maxBytes) {
    throw new CodeTemplateError("output_too_large", 0, `output exceeds ${state.maxBytes} bytes`);
  }
  state.out.push(text);
  state.bytes += length;
}

function renderNodes(state: RenderState, nodes: readonly TemplateNode[]) {
  for (const node of nodes) {
    if (node.kind === KIND_TEXT) write(state, node.text);
    else if (node.kind === KIND_VAR) {
      let value = hasOwn(state.model.vars ?? {}, node.text) ? (state.model.vars![node.text] ?? "") : "";
      if (state.item && node.text === "name") value = state.item.name;
      else if (state.item && node.text === "value") value = state.item.value;
      write(state, applyCodeFilter(node.filter || state.escape, String(value)));
    } else renderSection(state, node);
  }
}

function renderSection(state: RenderState, node: TemplateNode) {
  const inverted = node.kind === KIND_INVERTED;
  if (node.list) {
    const items = hasOwn(state.model.lists ?? {}, node.text) ? (state.model.lists![node.text] ?? []) : [];
    if (inverted) {
      if (items.length === 0) renderNodes(state, node.children);
      return;
    }
    items.forEach((item, index) => {
      state.item = item;
      state.first = index === 0;
      state.last = index === items.length - 1;
      try {
        renderNodes(state, node.children);
      } finally {
        state.item = null;
      }
    });
    return;
  }
  let truthy: boolean;
  if (node.text === "first") truthy = state.first;
  else if (node.text === "last") truthy = state.last;
  else truthy = hasOwn(state.model.flags ?? {}, node.text) && state.model.flags![node.text] === true;
  if (truthy !== inverted) renderNodes(state, node.children);
}

/**
 * Renders a compiled template; bare variables use `escape` ("none" or a
 * filter name). Output beyond `maxBytes` (> 0) throws output_too_large.
 */
export function renderCodeTemplate(template: CompiledCodeTemplate, model: CodeModel, escape: string, maxBytes: number): string {
  if (escape !== "none" && !FILTERS.includes(escape)) throw new CodeTemplateError("unknown_escape", 0, `unknown escape "${escape}"`);
  const state: RenderState = { model, escape, maxBytes, out: [], bytes: 0, item: null, first: false, last: false };
  renderNodes(state, template.nodes);
  return state.out.join("");
}

function jsonInner(value: string): string {
  let result = "";
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    switch (char) {
      case '"': result += '\\"'; break;
      case "\\": result += "\\\\"; break;
      case "\n": result += "\\n"; break;
      case "\r": result += "\\r"; break;
      case "\t": result += "\\t"; break;
      case "\b": result += "\\b"; break;
      case "\f": result += "\\f"; break;
      default:
        result += code < 0x20 || code === 0x2028 || code === 0x2029 ? `\\u${code.toString(16).padStart(4, "0")}` : char;
    }
  }
  return result;
}

const XML_ENTITIES: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&apos;" };
const URL_SAFE = /^[A-Za-z0-9\-_.!~*'()]$/;
const encoder = new TextEncoder();

/** Applies a filter or escape; "none", "raw" and unknown names return the value unchanged. */
export function applyCodeFilter(filter: string, value: string): string {
  switch (filter) {
    case "json":
      return `"${jsonInner(value)}"`;
    case "jsonInner":
      return jsonInner(value);
    case "shell":
      return `'${value.split("'").join("'\\''")}'`;
    case "powershell": {
      let result = "'";
      for (const char of value) result += "'\u2018\u2019\u201a\u201b".includes(char) ? char + char : char;
      return `${result}'`;
    }
    case "cstring": {
      let result = '"';
      for (const char of value) {
        const code = char.codePointAt(0) ?? 0;
        if (char === "\\") result += "\\\\";
        else if (char === '"') result += '\\"';
        else if (char === "\n") result += "\\n";
        else if (char === "\r") result += "\\r";
        else if (char === "\t") result += "\\t";
        else if (code < 0x20 || code === 0x7f) result += `\\${code.toString(8).padStart(3, "0")}`;
        else result += char;
      }
      return `${result}"`;
    }
    case "xml":
      return value.replace(/[&<>"']/g, (char) => XML_ENTITIES[char] ?? char);
    case "url": {
      let result = "";
      for (const byte of encoder.encode(value)) {
        const char = String.fromCharCode(byte);
        result += byte < 0x80 && URL_SAFE.test(char) ? char : `%${HEX[byte >> 4]}${HEX[byte & 15]}`;
      }
      return result;
    }
    case "upper":
      return value.replace(/[a-z]+/g, (text) => text.toUpperCase());
    case "lower":
      return value.replace(/[A-Z]+/g, (text) => text.toLowerCase());
  }
  return value;
}
