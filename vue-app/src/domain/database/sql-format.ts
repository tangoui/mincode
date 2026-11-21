const CLAUSE_PATTERN = /\s+(LEFT\s+JOIN|RIGHT\s+JOIN|INNER\s+JOIN|FULL\s+JOIN|CROSS\s+JOIN|GROUP\s+BY|ORDER\s+BY|FROM|WHERE|HAVING|LIMIT|OFFSET|VALUES|SET|UNION(?:\s+ALL)?|JOIN)\s+/gi;
const KEYWORD_PATTERN = /\b(select|from|where|and|or|as|join|left|right|inner|full|cross|on|group|by|order|having|limit|offset|insert|into|values|update|set|delete|create|alter|drop|table|distinct|union|all|null|is|not|like|in|exists|case|when|then|else|end|asc|desc)\b/gi;

function formatPlainSql(value: string) {
  return value
    .replace(/[ \t]+/g, " ")
    .replace(CLAUSE_PATTERN, (_match, clause: string) => `\n${clause.toUpperCase()} `)
    .replace(KEYWORD_PATTERN, (keyword) => keyword.toUpperCase());
}

/** Basic editor formatter that never rewrites quoted values or SQL comments. */
export function formatSqlForEditor(sql: string) {
  const output: string[] = [];
  let plainStart = 0;
  let index = 0;

  function pushProtected(end: number) {
    if (plainStart < index) output.push(formatPlainSql(sql.slice(plainStart, index)));
    output.push(sql.slice(index, end));
    index = end;
    plainStart = end;
  }

  while (index < sql.length) {
    const current = sql[index];
    const next = sql[index + 1];
    if (current === "'" || current === '"' || current === "`") {
      const quote = current;
      let end = index + 1;
      while (end < sql.length) {
        if (sql[end] === "\\") end += 2;
        else if (sql[end] === quote) {
          if (sql[end + 1] === quote && quote !== "`") end += 2;
          else {
            end += 1;
            break;
          }
        } else end += 1;
      }
      pushProtected(end);
      continue;
    }
    if ((current === "-" && next === "-") || current === "#") {
      const lineEnd = sql.indexOf("\n", index);
      pushProtected(lineEnd < 0 ? sql.length : lineEnd + 1);
      continue;
    }
    if (current === "/" && next === "*") {
      const blockEnd = sql.indexOf("*/", index + 2);
      pushProtected(blockEnd < 0 ? sql.length : blockEnd + 2);
      continue;
    }
    index += 1;
  }
  if (plainStart < sql.length) output.push(formatPlainSql(sql.slice(plainStart)));
  return output.join("").trim();
}
