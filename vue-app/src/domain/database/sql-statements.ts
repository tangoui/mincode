export interface SqlStatementRange {
  start: number;
  end: number;
  text: string;
}

export interface SqlEditorSelection {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
}

function isWhitespace(value: string | undefined) {
  return value === " " || value === "\t" || value === "\r" || value === "\n";
}

function trimRange(sql: string, start: number, end: number): SqlStatementRange | undefined {
  while (start < end && isWhitespace(sql[start])) start += 1;
  while (end > start && isWhitespace(sql[end - 1])) end -= 1;
  return start < end ? { start, end, text: sql.slice(start, end) } : undefined;
}

/** Splits SQL without treating semicolons inside strings or comments as delimiters. */
export function splitSqlStatements(sql: string): SqlStatementRange[] {
  const statements: SqlStatementRange[] = [];
  let start = 0;
  let quote: "'" | '"' | "`" | undefined;
  let inLineComment = false;
  let inBlockComment = false;

  for (let index = 0; index < sql.length; index += 1) {
    const current = sql[index];
    const next = sql[index + 1];

    if (inLineComment) {
      if (current === "\n") inLineComment = false;
      continue;
    }
    if (inBlockComment) {
      if (current === "*" && next === "/") {
        inBlockComment = false;
        index += 1;
      }
      continue;
    }
    if (quote) {
      if (current === "\\") {
        index += 1;
      } else if (current === quote) {
        if (sql[index + 1] === quote && quote !== "`") index += 1;
        else quote = undefined;
      }
      continue;
    }

    if (current === "-" && next === "-") {
      inLineComment = true;
      index += 1;
    } else if (current === "#") {
      inLineComment = true;
    } else if (current === "/" && next === "*") {
      inBlockComment = true;
      index += 1;
    } else if (current === "'" || current === '"' || current === "`") {
      quote = current;
    } else if (current === ";") {
      const statement = trimRange(sql, start, index);
      if (statement) statements.push(statement);
      start = index + 1;
    }
  }

  const finalStatement = trimRange(sql, start, sql.length);
  if (finalStatement) statements.push(finalStatement);
  return statements;
}

export function offsetAtPosition(sql: string, lineNumber: number, column: number) {
  const line = Math.max(1, lineNumber);
  const targetColumn = Math.max(1, column);
  let currentLine = 1;
  let offset = 0;
  while (offset < sql.length && currentLine < line) {
    if (sql[offset] === "\n") currentLine += 1;
    offset += 1;
  }
  return Math.min(sql.length, offset + targetColumn - 1);
}

export function statementAtOffset(sql: string, offset: number) {
  const statements = splitSqlStatements(sql);
  const position = Math.max(0, Math.min(sql.length, offset));
  return statements.find((statement) => position >= statement.start && position < statement.end)?.text
    ?? statements.find((statement) => position < statement.start)?.text
    ?? statements[statements.length - 1]?.text
    ?? sql.trim();
}

export function resolveSqlForExecution(
  sql: string,
  selection: SqlEditorSelection | undefined,
  runAll = false,
) {
  if (runAll) return sql.trim();
  if (!selection) return statementAtOffset(sql, 0);
  const start = offsetAtPosition(sql, selection.startLineNumber, selection.startColumn);
  const end = offsetAtPosition(sql, selection.endLineNumber, selection.endColumn);
  const selectionStart = Math.min(start, end);
  const selectionEnd = Math.max(start, end);
  if (selectionEnd > selectionStart) return sql.slice(selectionStart, selectionEnd).trim();
  return statementAtOffset(sql, selectionStart);
}
