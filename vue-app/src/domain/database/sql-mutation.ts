/**
 * Pure SQL mutation detection for the database workspace.
 *
 * A keyword heuristic for editor hints only. Execution confirmation is the
 * SQL safety net's job (services/sql-safety-service.ts): the backend
 * classifies the statement and issues the check token writes require.
 */

/**
 * Statement prefixes that modify data or schema.
 * Ordered roughly by destructiveness for potential future prioritisation.
 */
const MUTATION_PREFIXES = [
  "INSERT",
  "UPDATE",
  "DELETE",
  "REPLACE",
  "MERGE",
  "TRUNCATE",
  "DROP",
  "ALTER",
  "CREATE",
  "RENAME",
  "GRANT",
  "REVOKE",
  "CALL",
  "EXEC",
  "EXECUTE",
  "LOCK",
  "UNLOCK",
  "LOAD",
  "COPY",
] as const;

const MUTATION_PATTERN = new RegExp(
  `^(?:${MUTATION_PREFIXES.join("|")})\\b`,
  "i"
);

/**
 * Strip leading SQL comments (block and line) and whitespace so the first
 * meaningful keyword can be inspected.
 */
function stripLeadingComments(sql: string): string {
  let rest = sql.trimStart();
  for (;;) {
    if (rest.startsWith("/*")) {
      const end = rest.indexOf("*/");
      if (end === -1) return "";
      rest = rest.slice(end + 2).trimStart();
    } else if (rest.startsWith("--")) {
      const end = rest.indexOf("\n");
      if (end === -1) return "";
      rest = rest.slice(end + 1).trimStart();
    } else if (rest.startsWith("#")) {
      // MySQL-style line comment
      const end = rest.indexOf("\n");
      if (end === -1) return "";
      rest = rest.slice(end + 1).trimStart();
    } else {
      return rest;
    }
  }
}

/**
 * Split a script into individual statements on semicolons that sit outside
 * of quoted strings. This is intentionally conservative — it may treat a
 * script as a single statement in exotic edge cases, which only means the
 * confirmation is driven by the first statement's keyword.
 */
function splitStatements(sql: string): string[] {
  const statements: string[] = [];
  let current = "";
  let inSingle = false;
  let inDouble = false;
  let inBacktick = false;

  for (let i = 0; i < sql.length; i++) {
    const char = sql[i];
    if (inSingle) {
      current += char;
      if (char === "'" && sql[i + 1] === "'") {
        current += "'";
        i++;
      } else if (char === "'") {
        inSingle = false;
      }
    } else if (inDouble) {
      current += char;
      if (char === '"' && sql[i + 1] === '"') {
        current += '"';
        i++;
      } else if (char === '"') {
        inDouble = false;
      }
    } else if (inBacktick) {
      current += char;
      if (char === "`") inBacktick = false;
    } else if (char === "'") {
      inSingle = true;
      current += char;
    } else if (char === '"') {
      inDouble = true;
      current += char;
    } else if (char === "`") {
      inBacktick = true;
      current += char;
    } else if (char === ";") {
      statements.push(current);
      current = "";
    } else {
      current += char;
    }
  }
  statements.push(current);
  return statements;
}

/**
 * Returns `true` when the SQL script contains at least one statement that
 * modifies data or schema (INSERT, UPDATE, DELETE, DROP, ALTER, etc.).
 *
 * The check inspects the first meaningful keyword of every statement in the
 * script, skipping leading comments.
 */
export function isMutationSql(sql: string): boolean {
  return splitStatements(sql).some((statement) => {
    const body = stripLeadingComments(statement);
    return body.length > 0 && MUTATION_PATTERN.test(body);
  });
}

/**
 * Return the mutation keyword that triggered detection, for editor hints.
 * Returns `undefined` for read-only scripts.
 */
export function detectMutationKeyword(sql: string): string | undefined {
  for (const statement of splitStatements(sql)) {
    const body = stripLeadingComments(statement);
    if (!body) continue;
    const match = MUTATION_PATTERN.exec(body);
    if (match) return match[0].toUpperCase();
  }
  return undefined;
}
