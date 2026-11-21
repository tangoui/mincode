import type { DatabaseColumnInfo, DatabaseSchemaInfo } from "@/domain/database/types";

export type SqlCompletionItemKind = "keyword" | "snippet" | "table" | "column";

export interface SqlCompletionItem {
  label: string;
  insertText: string;
  kind: SqlCompletionItemKind;
  detail?: string;
  documentation?: string;
  sortText?: string;
}

export interface SqlCompletionContext {
  schema?: DatabaseSchemaInfo;
  selectedDatabase?: string;
  selectedTable?: string;
  selectedTableColumns?: DatabaseColumnInfo[];
  resultColumns?: string[];
}

const SQL_KEYWORDS = [
  "SELECT",
  "FROM",
  "WHERE",
  "AND",
  "OR",
  "NOT",
  "AS",
  "ON",
  "JOIN",
  "INNER JOIN",
  "LEFT JOIN",
  "RIGHT JOIN",
  "FULL OUTER JOIN",
  "CROSS JOIN",
  "GROUP BY",
  "HAVING",
  "ORDER BY",
  "ASC",
  "DESC",
  "LIMIT",
  "OFFSET",
  "DISTINCT",
  "UNION",
  "UNION ALL",
  "EXISTS",
  "IN",
  "BETWEEN",
  "LIKE",
  "IS NULL",
  "IS NOT NULL",
  "CASE",
  "WHEN",
  "THEN",
  "ELSE",
  "END",
  "INSERT INTO",
  "VALUES",
  "UPDATE",
  "SET",
  "DELETE FROM",
  "CREATE TABLE",
  "ALTER TABLE",
  "DROP TABLE",
  "CREATE INDEX",
  "WITH",
  "RETURNING",
] as const;

const SQL_SNIPPETS: SqlCompletionItem[] = [
  {
    label: "SELECT … FROM",
    insertText: "SELECT ${1:*}\nFROM ${2:table}\nWHERE ${3:condition};",
    kind: "snippet",
    sortText: "0-select",
  },
  {
    label: "INSERT INTO … VALUES",
    insertText: "INSERT INTO ${1:table} (${2:columns})\nVALUES (${3:values});",
    kind: "snippet",
    sortText: "0-insert",
  },
  {
    label: "UPDATE … SET",
    insertText: "UPDATE ${1:table}\nSET ${2:column} = ${3:value}\nWHERE ${4:condition};",
    kind: "snippet",
    sortText: "0-update",
  },
  {
    label: "DELETE FROM …",
    insertText: "DELETE FROM ${1:table}\nWHERE ${2:condition};",
    kind: "snippet",
    sortText: "0-delete",
  },
];

function uniqueByLabel(items: SqlCompletionItem[]) {
  const labels = new Set<string>();
  return items.filter((item) => {
    const key = item.label.toLocaleLowerCase();
    if (labels.has(key)) return false;
    labels.add(key);
    return true;
  });
}

/**
 * Builds the SQL editor's local completion catalog. Database schema data stays
 * in the caller, making this fast and usable without extra requests while typing.
 */
export function buildSqlCompletionItems(context: SqlCompletionContext): SqlCompletionItem[] {
  const keywordItems: SqlCompletionItem[] = SQL_KEYWORDS.map((keyword, index) => ({
    label: keyword,
    insertText: keyword,
    kind: "keyword",
    sortText: `1-${String(index).padStart(2, "0")}`,
  }));

  const tableItems: SqlCompletionItem[] = (context.schema?.databases ?? []).flatMap((database) =>
    database.tables.map((table) => {
      const inSelectedDatabase = database.name === context.selectedDatabase;
      const qualifiedName = inSelectedDatabase ? table.name : `${database.name}.${table.name}`;
      return {
        label: qualifiedName,
        insertText: qualifiedName,
        kind: "table" as const,
        detail: table.kind,
        sortText: inSelectedDatabase ? `2-${table.name}` : `3-${qualifiedName}`,
      };
    })
  );

  const selectedColumnItems: SqlCompletionItem[] = (context.selectedTableColumns ?? []).map(
    (column) => ({
      label: column.name,
      insertText: column.name,
      kind: "column" as const,
      detail: `${context.selectedTable ? `${context.selectedTable}.` : ""}${column.dataType}`,
      documentation: column.comment || undefined,
      sortText: `2-${column.name}`,
    })
  );
  const resultColumnItems: SqlCompletionItem[] = (context.resultColumns ?? []).map((column) => ({
    label: column,
    insertText: column,
    kind: "column" as const,
    sortText: `4-${column}`,
  }));

  return uniqueByLabel([
    ...SQL_SNIPPETS,
    ...keywordItems,
    ...tableItems,
    ...selectedColumnItems,
    ...resultColumnItems,
  ]);
}
