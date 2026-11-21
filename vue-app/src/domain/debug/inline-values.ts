/**
 * Inline debug values and hover expressions: which variable values to show
 * at the end of the lines above the paused line, and which expression the
 * mouse is over. Plain text scanning, no language server: string literals
 * and comments (`#`, `//`) are skipped, names match whole words only.
 */

export interface InlineValueOptions {
  /** Lines above the paused line to look at (default 30). */
  lookBack?: number;
  /** Values per line (default 3). */
  maxPerLine?: number;
  /** Characters of a value, the `…` included (default 60). */
  maxValueChars?: number;
}

export interface InlineValue {
  line: number;
  text: string;
}

export interface HoverExpression {
  expression: string;
  /** 1-based columns of the expression; the end is exclusive. */
  startColumn: number;
  endColumn: number;
}

export const INLINE_VALUES_LOOK_BACK = 30;
const MAX_PER_LINE = 3;
const MAX_VALUE_CHARS = 60;

const WORD = /[\p{L}\p{N}_$]+/gu;
const WORD_CHAR = /[\p{L}\p{N}_$]/u;
const DIGIT = /\p{N}/u;
const IDENTIFIER = /^[\p{L}_$][\p{L}\p{N}_$]*$/u;

/**
 * The line with the contents of string literals blanked out and everything
 * from a comment start (`#`, `//`) on removed. Quote characters stay, so
 * columns are kept.
 */
export function codeOfLine(line: string): string {
  let result = "";
  let quote: string | null = null;
  for (let index = 0; index < line.length; index += 1) {
    const char = line[index]!;
    if (quote) {
      if (char === "\\") {
        result += index + 1 < line.length ? "  " : " ";
        index += 1;
      } else if (char === quote) {
        quote = null;
        result += char;
      } else {
        result += " ";
      }
      continue;
    }
    if (char === "#" || (char === "/" && line[index + 1] === "/")) break;
    if (char === '"' || char === "'" || char === "`") quote = char;
    result += char;
  }
  return result;
}

/** Whole-word names of code, in order, without attribute names (`obj.x` is not `x`). */
function namesIn(code: string): string[] {
  const names: string[] = [];
  for (const match of code.matchAll(WORD)) {
    const word = match[0];
    if (DIGIT.test(word[0]!)) continue;
    if (code.slice(0, match.index).trimEnd().endsWith(".")) continue;
    names.push(word);
  }
  return names;
}

function clip(value: string, max: number): string {
  const single = value.replace(/\r?\n/g, " ");
  return single.length > max ? `${single.slice(0, Math.max(0, max - 1))}…` : single;
}

/**
 * `name = value` texts for the lines `[pausedLine - lookBack, pausedLine]`
 * that use a variable (lines are 1-based: `lines[0]` is line 1). A line
 * lists its names in the order they first appear, joined by `, `.
 */
export function computeInlineValues(
  lines: string[],
  pausedLine: number,
  variables: { name: string; value: string }[],
  opts: InlineValueOptions = {},
): InlineValue[] {
  const lookBack = opts.lookBack ?? INLINE_VALUES_LOOK_BACK;
  const maxPerLine = opts.maxPerLine ?? MAX_PER_LINE;
  const maxValueChars = opts.maxValueChars ?? MAX_VALUE_CHARS;
  const values = new Map<string, string>();
  for (const variable of variables) {
    if (IDENTIFIER.test(variable.name) && !values.has(variable.name)) values.set(variable.name, variable.value);
  }
  const result: InlineValue[] = [];
  if (!values.size || maxPerLine < 1) return result;
  const last = Math.min(pausedLine, lines.length);
  for (let line = Math.max(1, pausedLine - lookBack); line <= last; line += 1) {
    const shown: string[] = [];
    for (const name of namesIn(codeOfLine(lines[line - 1] ?? ""))) {
      if (!values.has(name) || shown.includes(name)) continue;
      shown.push(name);
      if (shown.length >= maxPerLine) break;
    }
    if (shown.length) result.push({ line, text: shown.map((name) => `${name} = ${clip(values.get(name)!, maxValueChars)}`).join(", ") });
  }
  return result;
}

/** The word starting at or containing index, as [start, end), or null. */
function wordAt(text: string, index: number): [number, number] | null {
  if (index < 0 || index >= text.length || !WORD_CHAR.test(text[index]!)) return null;
  let start = index;
  let end = index + 1;
  while (start > 0 && WORD_CHAR.test(text[start - 1]!)) start -= 1;
  while (end < text.length && WORD_CHAR.test(text[end]!)) end += 1;
  return [start, end];
}

/**
 * The expression under 1-based `column`: the word there with the member
 * chain before it (`a.b` over `b`, `a` over `a` in `a.b`). Null outside a
 * word, inside strings and comments, for numbers, and for chains that start
 * with a call or index (`f().b` cannot be evaluated as `b`).
 */
export function hoverExpressionAt(line: string, column: number): HoverExpression | null {
  const code = codeOfLine(line);
  const word = wordAt(code, column - 1);
  if (!word) return null;
  let [start] = word;
  const end = word[1];
  if (DIGIT.test(code[start]!)) return null;
  while (code[start - 1] === ".") {
    const previous = wordAt(code, start - 2);
    if (!previous || DIGIT.test(code[previous[0]]!)) return null;
    start = previous[0];
  }
  return { expression: line.slice(start, end), startColumn: start + 1, endColumn: end + 1 };
}
