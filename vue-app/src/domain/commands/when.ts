/**
 * when-expression evaluation for plugin command visibility. The dialect is
 * deliberately tiny and mirrors backend/plugin/command_context.go:
 * clause (&& clause)* where clause = [!]key [("=="|"!=") "literal"].
 * Unknown keys are falsy; equality requires a string context value.
 */

export const WHEN_EXPRESSION_MAX_LENGTH = 512;
export const WHEN_EXPRESSION_MAX_CLAUSES = 8;

export type PluginContextValue = string | number | boolean | null;
export type PluginContextValues = Record<string, PluginContextValue>;

export interface WhenClause {
  key: string;
  op: "" | "==" | "!=";
  value: string;
  negate: boolean;
}

// Host keys may be camelCase after the first dot (editor.languageId,
// resource.isProjectRoot); undotted camelCase keys (VS Code's sideBarVisible)
// stay invalid, and plugin context keys set through host.context.set stay
// lowercase. Mirrors whenKeyPattern in command_context.go.
const KEY_PATTERN = /^(?=.{1,64}$)[a-z0-9][a-z0-9_-]*(?:\.[A-Za-z0-9._-]*)?$/;

function tokenize(expression: string): string[] {
  const tokens: string[] = [];
  let index = 0;
  while (index < expression.length) {
    const char = expression[index]!;
    if (char === " " || char === "\t") {
      index++;
      continue;
    }
    if (char === "!") {
      if (expression[index + 1] === "=") {
        tokens.push("!=");
        index += 2;
      } else {
        tokens.push("!");
        index++;
      }
      continue;
    }
    if (char === "=") {
      if (expression[index + 1] !== "=") throw new Error("when expression uses == for equality");
      tokens.push("==");
      index += 2;
      continue;
    }
    if (char === "&") {
      if (expression[index + 1] !== "&") throw new Error("when expression uses && between clauses");
      tokens.push("&&");
      index += 2;
      continue;
    }
    if (char === '"') {
      let end = index + 1;
      while (end < expression.length && expression[end] !== '"') {
        const code = expression.charCodeAt(end);
        if (code < 0x20 || expression[end] === "\\") {
          throw new Error("when string literal contains an unsupported character");
        }
        end++;
      }
      if (end >= expression.length) throw new Error("when string literal is not terminated");
      tokens.push(expression.slice(index, end + 1));
      index = end + 1;
      continue;
    }
    let end = index;
    while (end < expression.length && /[A-Za-z0-9._-]/.test(expression[end]!)) end++;
    if (end === index) throw new Error(`when expression has an unexpected character ${JSON.stringify(char)}`);
    tokens.push(expression.slice(index, end));
    index = end;
  }
  return tokens;
}

export function parseWhenExpression(expression: string): WhenClause[] {
  const trimmed = expression.trim();
  if (!trimmed) throw new Error("when expression is empty");
  if (trimmed.length > WHEN_EXPRESSION_MAX_LENGTH) {
    throw new Error(`when expression exceeds ${WHEN_EXPRESSION_MAX_LENGTH} characters`);
  }
  const tokens = tokenize(trimmed);
  const clauses: WhenClause[] = [];
  let position = 0;
  let expectClause = true;
  while (position < tokens.length) {
    let token = tokens[position]!;
    if (expectClause) {
      const clause: WhenClause = { key: "", op: "", value: "", negate: false };
      if (token === "!") {
        clause.negate = true;
        position++;
        if (position >= tokens.length) throw new Error("when expression ends after !");
        token = tokens[position]!;
      }
      if (!KEY_PATTERN.test(token)) {
        throw new Error(`when expression expects a context key, got ${JSON.stringify(token)}`);
      }
      clause.key = token;
      position++;
      if (position < tokens.length && (tokens[position] === "==" || tokens[position] === "!=")) {
        clause.op = tokens[position] as "==" | "!=";
        position++;
        const literal = tokens[position];
        if (literal === undefined || !literal.startsWith('"')) {
          throw new Error("when comparison requires a quoted string literal");
        }
        clause.value = literal.slice(1, -1);
        position++;
      }
      clauses.push(clause);
      if (clauses.length > WHEN_EXPRESSION_MAX_CLAUSES) {
        throw new Error(`when expression exceeds ${WHEN_EXPRESSION_MAX_CLAUSES} clauses`);
      }
      expectClause = false;
      continue;
    }
    if (token !== "&&") throw new Error(`when expression expects &&, got ${JSON.stringify(token)}`);
    position++;
    expectClause = true;
  }
  if (expectClause) throw new Error("when expression ends after &&");
  return clauses;
}

function truthy(value: PluginContextValue | undefined): boolean {
  if (value === undefined || value === null) return false;
  if (typeof value === "boolean") return value;
  if (typeof value === "string") return value !== "";
  return value !== 0;
}

export function evaluateWhenClauses(clauses: readonly WhenClause[], context: PluginContextValues): boolean {
  for (const clause of clauses) {
    const value = context[clause.key];
    let result: boolean;
    if (clause.op === "") {
      result = truthy(value);
    } else {
      const equal = typeof value === "string" && value === clause.value;
      result = clause.op === "==" ? equal : !equal;
    }
    if (clause.negate) result = !result;
    if (!result) return false;
  }
  return true;
}

/** Returns false for malformed expressions: a bad when hides the command. */
export function evaluateWhen(expression: string, context: PluginContextValues): boolean {
  const trimmed = expression.trim();
  if (!trimmed) return true;
  try {
    return evaluateWhenClauses(parseWhenExpression(trimmed), context);
  } catch {
    return false;
  }
}
