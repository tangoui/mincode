/**
 * The result grid as a plugin-service target (surface dbResult): what the
 * grid shows, rows read only when the menu asks for them. Only the lazily
 * loaded service menu of the result pane imports this (the database
 * workspace chunk is near its budget); the workspace view contributes just
 * a DbResultMenuSource for the open cell menu.
 */
import type { DbResultServiceTarget } from "@/domain/plugins/service-surfaces";

/**
 * The workspace view's part: where the open cell menu's rows came from (a
 * table tab's connection and database, or those a query ran on). Empty when
 * unknown: the target then names no database at all.
 */
export interface DbResultMenuSource {
  connectionId?: string;
  database?: string;
}

/** Where the grid's rows come from: loading state and the connection's names. */
export interface DbResultServiceSource {
  /** A query (or, for a table, its page) is still loading. */
  running: boolean;
  /** The open connection: only its id and name are read, with `kind` (the SQL editor's database.kind). */
  connection?: { id: string; name: string };
  kind?: string;
  database?: string;
  /** Kind of the table shown (table mode), when the schema lists it. */
  tableKind?: string;
}

/** The result pane's view of the grid (a subset of DatabaseResultPane's props). */
export interface DbResultGrid {
  object: "query" | "table";
  selectedTable?: string;
  activeResult?: { nextPageToken?: string };
  resultRows: readonly unknown[][];
  /** Rows as displayed (filter and sort applied). */
  displayRows: readonly { row: readonly unknown[] }[];
  /** Visible columns in display order: `index` into each row, `dataType` when the grid knows it. */
  columns: readonly { name: string; index: number; dataType?: string }[];
  gridState: { filterText: string; filters: { conditions: readonly unknown[] }; sort?: unknown; hiddenColumns: readonly string[] };
  formatCell: (value: unknown) => string;
  canNextPage: boolean;
  /** Staged edits of the table (table mode). */
  pendingRows?: ReadonlyMap<number, unknown>;
}

export function dbResultServiceTarget(grid: DbResultGrid, source: DbResultServiceSource): DbResultServiceTarget {
  const { columns, gridState: state } = grid;
  const connection = source.connection;
  const table = grid.object === "table" ? grid.selectedTable : undefined;
  return {
    surface: "dbResult",
    columns: columns.map((column) => ({ name: column.name, type: column.dataType || null })),
    rows: () => grid.displayRows.map(({ row }) => columns.map((column) => row[column.index])),
    formatCell: grid.formatCell,
    hasMore: grid.canNextPage || Boolean(grid.activeResult?.nextPageToken),
    running: source.running,
    pendingChanges: Boolean(grid.pendingRows?.size),
    view: {
      filtered:
        grid.displayRows.length < grid.resultRows.length || Boolean(state.filterText.trim() || state.filters.conditions.length),
      sorted: Boolean(state.sort),
      hiddenColumns: state.hiddenColumns.length,
    },
    ...(connection && source.kind
      ? {
          database: {
            connection: { id: connection.id, name: connection.name, kind: source.kind },
            connected: true,
            objectType: table ? "table" : source.database ? "database" : "connection",
            ...(source.database ? { database: source.database } : {}),
            ...(table ? { table: { name: table, kind: source.tableKind || "table" } } : {}),
          },
        }
      : {}),
  } satisfies DbResultServiceTarget;
}
