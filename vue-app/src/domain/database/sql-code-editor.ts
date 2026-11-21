// Pure helpers for the CodeMirror SQL editor (components/database/query/SqlCodeEditor.vue):
// line/column ⇄ offset conversion for the view state the query tabs persist, and the SQL
// dialect of a connection type. No CodeMirror import here, so the logic is testable as is.
import type { DbType } from "@/domain/database/types";
import type { ProjectEditorViewState } from "@/domain/projects/types";

/** CodeMirror dialects this editor highlights (names of @codemirror/lang-sql exports). */
export type SqlEditorDialect = "StandardSQL" | "MySQL" | "MariaSQL" | "PostgreSQL" | "SQLite" | "MSSQL" | "PLSQL" | "Cassandra";

const MYSQL_FAMILY = new Set<DbType>(["mysql", "tidb", "oceanbase", "polardb", "tdsql", "dolt", "greatsql", "goldendb", "starrocks", "doris", "gbase"]);
const POSTGRES_FAMILY = new Set<DbType>(["postgres", "kingbase", "gaussdb", "opengauss", "highgo", "vastbase", "opentenbase", "cockroachdb", "yugabyte", "greenplum", "questdb", "materialize", "risingwave", "pgvector", "gbase8c"]);

/** The SQL dialect used for highlighting and keyword completion of a connection type. */
export function sqlEditorDialect(dbType: DbType | undefined): SqlEditorDialect {
  if (!dbType) return "StandardSQL";
  if (dbType === "mariadb") return "MariaSQL";
  if (MYSQL_FAMILY.has(dbType)) return "MySQL";
  if (POSTGRES_FAMILY.has(dbType)) return "PostgreSQL";
  if (dbType === "sqlite" || dbType === "duckdb") return "SQLite";
  if (dbType === "sqlserver") return "MSSQL";
  if (dbType === "oracle" || dbType === "dameng" || dbType === "yashandb") return "PLSQL";
  if (dbType === "cassandra") return "Cassandra";
  return "StandardSQL";
}

/** Minimal document view: line starts are computed on demand from the text. */
export interface TextLines {
  /** 1-based line count. */
  lines: number;
  /** Offset of the first character of 1-based `line`. */
  lineStart(line: number): number;
  /** Length of 1-based `line`, without its line break. */
  lineLength(line: number): number;
  /** 1-based line containing `offset`. */
  lineAt(offset: number): number;
}

/** Offset of a 1-based line/column, clamped into the document. */
export function offsetOf(doc: TextLines, line: number, column: number): number {
  const l = Math.min(Math.max(1, Math.floor(line) || 1), doc.lines);
  const c = Math.min(Math.max(1, Math.floor(column) || 1), doc.lineLength(l) + 1);
  return doc.lineStart(l) + c - 1;
}

/** 1-based line/column of `offset`. */
export function positionOf(doc: TextLines, offset: number): { line: number; column: number } {
  const line = doc.lineAt(offset);
  return { line, column: offset - doc.lineStart(line) + 1 };
}

/**
 * The persisted view state (same shape as the Monaco editors': 1-based lines and columns,
 * anchor = selectionStart*, head = position*) for a selection and scroll position.
 */
export function viewStateOf(doc: TextLines, anchor: number, head: number, scrollTop: number, scrollLeft: number): ProjectEditorViewState {
  const start = positionOf(doc, anchor);
  const end = positionOf(doc, head);
  return {
    lineNumber: end.line,
    column: end.column,
    selectionStartLineNumber: start.line,
    selectionStartColumn: start.column,
    positionLineNumber: end.line,
    positionColumn: end.column,
    scrollTop,
    scrollLeft,
  };
}

/** Selection offsets of a persisted view state, clamped into the current document. */
export function selectionOf(doc: TextLines, state: ProjectEditorViewState): { anchor: number; head: number } {
  return {
    anchor: offsetOf(doc, state.selectionStartLineNumber, state.selectionStartColumn),
    head: offsetOf(doc, state.positionLineNumber, state.positionColumn),
  };
}

/** Converts a Monaco snippet ("${1:table}", "$0") into CodeMirror's "${table}" / "${}" form. */
export function codeMirrorSnippet(template: string): string {
  return template.replace(/\$\{(\d+):([^}]*)\}/g, (_, _index: string, placeholder: string) => `\${${placeholder}}`).replace(/\$(\d+)/g, "${}");
}
