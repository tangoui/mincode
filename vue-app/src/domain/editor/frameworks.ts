export interface FrameworkCondition {
  file: string;
  field?: string[];
  equals?: string;
}

export interface FrameworkCompletion {
  label: string;
  insertText: string;
  context: string;
  detail: string;
  documentation: string;
}

export interface EditorFramework {
  id: string;
  languages: string[];
  match: {
    all: FrameworkCondition[];
    any: FrameworkCondition[];
    exclude: FrameworkCondition[];
  };
  completions: FrameworkCompletion[];
}

export interface FrameworkRoot {
  root: string;
  pluginId: string;
  framework: EditorFramework;
}

/** 只解析有限长度的 JSONC，不执行插件代码。 */
export function frameworkConfig(source: string): unknown {
  if (source.length > 262144) return undefined;
  let text = "";
  let quoted = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (quoted) {
      text += char;
      if (char === "\\") text += source[++index] ?? "";
      else if (char === '"') quoted = false;
    } else if (char === '"') {
      quoted = true;
      text += char;
    } else if (char === "/" && source[index + 1] === "/") {
      while (index < source.length && source[index] !== "\n") index += 1;
      text += "\n";
    } else if (char === "/" && source[index + 1] === "*") {
      const end = source.indexOf("*/", index + 2);
      if (end < 0) return undefined;
      index = end + 1;
      text += " ";
    } else text += char;
  }
  let cleaned = "";
  quoted = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (quoted) {
      cleaned += char;
      if (char === "\\") cleaned += text[++index] ?? "";
      else if (char === '"') quoted = false;
      continue;
    }
    if (char === '"') quoted = true;
    if (char === ",") {
      let next = index + 1;
      while (next < text.length && /\s/.test(text[next]!)) next += 1;
      if (text[next] === "}" || text[next] === "]") continue;
    }
    cleaned += char;
  }
  try {
    return JSON.parse(cleaned) as unknown;
  } catch {
    return undefined;
  }
}

function object(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

/** 宿主快照也按不可信数据重新校验。 */
export function parseEditorFramework(value: unknown): EditorFramework | undefined {
  const entry = object(value);
  const pattern = /^[a-z][a-z0-9+#._-]{0,31}$/;
  if (!entry || Object.keys(entry).some((key) => !["id", "languages", "match", "completions"].includes(key))) return undefined;
  if (typeof entry.id !== "string" || !pattern.test(entry.id)) return undefined;
  if (!Array.isArray(entry.languages) || !entry.languages.length || entry.languages.length > 16 || !entry.languages.every((language) => typeof language === "string" && pattern.test(language))) return undefined;
  const match = object(entry.match);
  if (!match || Object.keys(match).some((key) => !["all", "any", "exclude"].includes(key))) return undefined;
  for (const key of ["all", "any", "exclude"]) {
    const conditions = match[key];
    if (!Array.isArray(conditions) || conditions.length > 16 || (key === "all" && !conditions.length)) return undefined;
    for (const value of conditions) {
      const condition = object(value);
      if (!condition || Object.keys(condition).some((key) => !["file", "field", "equals"].includes(key))) return undefined;
      if (typeof condition.file !== "string" || !condition.file || condition.file.length > 128 || (/[\\\\:*?]/.test(condition.file) || condition.file.includes(String.fromCharCode(0))) || condition.file.split("/").some((segment) => !segment || segment.startsWith("."))) return undefined;
      if (condition.field !== undefined && (!Array.isArray(condition.field) || condition.field.length > 8 || !condition.field.every((field) => typeof field === "string" && field.length > 0 && field.length <= 128 && !["__proto__", "constructor", "prototype"].includes(field)))) return undefined;
      if (condition.equals !== undefined && (typeof condition.equals !== "string" || condition.equals.length > 128 || !Array.isArray(condition.field) || !condition.field.length)) return undefined;
    }
  }
  if (!Array.isArray(entry.completions) || entry.completions.length > 1000) return undefined;
  for (const value of entry.completions) {
    const item = object(value);
    if (!item || Object.keys(item).some((key) => !["label", "insertText", "context", "detail", "documentation"].includes(key))) return undefined;
    if (typeof item.label !== "string" || !item.label || item.label.length > 128 || typeof item.context !== "string" || !item.context || item.context.length > 128 || !item.label.startsWith(item.context)) return undefined;
    if (typeof item.insertText !== "string" || !item.insertText || item.insertText.length > 1024 || typeof item.detail !== "string" || item.detail.length > 1024 || typeof item.documentation !== "string" || item.documentation.length > 4096) return undefined;
  }
  return entry as unknown as EditorFramework;
}

function conditionMatches(condition: FrameworkCondition, files: ReadonlyMap<string, string>): boolean {
  const source = files.get(condition.file);
  if (source === undefined) return false;
  if (!condition.field?.length) return !condition.file.endsWith(".json") || object(frameworkConfig(source)) !== undefined;
  let value = frameworkConfig(source);
  for (const field of condition.field) {
    if (typeof value !== "object" || value === null || !Object.prototype.hasOwnProperty.call(value, field)) return false;
    value = (value as Record<string, unknown>)[field];
  }
  return condition.equals === undefined ? value !== undefined : String(value) === condition.equals;
}

export function matchFramework(framework: EditorFramework, files: ReadonlyMap<string, string>): boolean {
  return framework.match.all.every((condition) => conditionMatches(condition, files))
    && (!framework.match.any.length || framework.match.any.some((condition) => conditionMatches(condition, files)))
    && !framework.match.exclude.some((condition) => conditionMatches(condition, files));
}

function normalized(path: string): string {
  const result = path.replace(/\\/g, "/").replace(/\/+$/, "");
  return /^[A-Za-z]:\//.test(result) ? result.toLowerCase() : result;
}

export function nearestFrameworkRoot(path: string, roots: readonly FrameworkRoot[], boundaries: readonly string[]): FrameworkRoot | undefined {
  const file = normalized(path);
  const inside = (root: string) => file === normalized(root) || file.startsWith(`${normalized(root)}/`);
  const nearest = roots.filter((item) => inside(item.root)).sort((left, right) => right.root.length - left.root.length)[0];
  if (!nearest || boundaries.some((boundary) => boundary.length > nearest.root.length && inside(boundary))) return undefined;
  return nearest;
}

const IDENTIFIER_PART = /[\p{ID_Continue}$\u200c\u200d]/u;

/** 以标识符开头的上下文（如 `uni.`）不能紧跟在标识符字符或 `.` 之后，避免命中 `myuni.`、`obj.uni.`。 */
function contextStartsToken(before: string, start: number, context: string): boolean {
  if (!start || !IDENTIFIER_PART.test(context[0]!)) return true;
  const previous = before[start - 1]!;
  return previous !== "." && !IDENTIFIER_PART.test(previous);
}

export function frameworkSuggestions(framework: EditorFramework, before: string): FrameworkCompletion[] {
  return framework.completions.filter((item) => {
    const start = before.lastIndexOf(item.context);
    if (start < 0 || !contextStartsToken(before, start, item.context)) return false;
    const typed = before.slice(start);
    return item.label.startsWith(typed) && !/\s/.test(typed);
  }).slice(0, 100);
}
