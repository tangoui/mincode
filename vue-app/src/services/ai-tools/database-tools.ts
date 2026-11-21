import { importInlineRows } from "@/services/database-row-import-service";
import Papa from "papaparse";
import type { AiCapability } from "@/domain/ai/capability-registry";
import type { AiResource } from "@/domain/ai/resource-registry";
import {
  isSensitiveDataName,
  requiredString,
  resourceCapability,
  sanitizeAiResult,
  stringArgument,
  tool,
  type AiToolDependencies,
} from "./shared";
import { createAiSqlSafety, type AiSqlSafety } from "./sql-safety";

export interface DatabaseToolDependencies extends AiToolDependencies {
  connectDatabase?(connectionId: string): Promise<unknown>;
  disconnectDatabase?(connectionId: string): Promise<unknown>;
  databaseSessionId?(connectionId: string): string | undefined;
  databaseAction?(input: Record<string, unknown>): Promise<unknown>;
  /**
   * 连接绑定的插件驱动标签（如 "Acme DB (com.acme.db)"）；内置驱动返回 undefined。
   * 仅用于审批预览标注（E-11），不改变任何工具的风险等级。
   */
  databaseDriverLabel?(connectionId: string): string | undefined;
  /** SQL 安全网（必需）：写工具经它检查与确认，见 ./sql-safety。 */
  sqlSafety: AiSqlSafety;
}

const approvalConnectionKeys = [
  "connectionId",
  "sourceConnectionId",
  "targetConnectionId",
] as const;

function approvalConnectionIds(
  input: Record<string, unknown>,
  resource: AiResource | undefined,
) {
  const ids = new Set<string>();
  for (const key of approvalConnectionKeys) {
    const value = input[key];
    if (typeof value === "string" && value) ids.add(value);
  }
  const fromResource = resource?.metadata?.connectionId;
  if (typeof fromResource === "string" && fromResource) ids.add(fromResource);
  return [...ids];
}

/**
 * 给非只读数据库工具的审批预览追加插件驱动标注。风险等级、执行逻辑保持不变；
 * AI 工具没有任何绑定驱动或授予凭据释放的入口。
 */
export function labelPluginDriverApprovals(
  capabilities: AiCapability[],
  driverLabel: DatabaseToolDependencies["databaseDriverLabel"],
): AiCapability[] {
  if (!driverLabel) return capabilities;
  return capabilities.map((capability) => {
    if (capability.risk === "read") return capability;
    const original = capability.approvalPreview?.bind(capability);
    return {
      ...capability,
      approvalPreview: async (context) => {
        const base = original ? await original(context) : capability.tool.name;
        const labels = [
          ...new Set(
            approvalConnectionIds(context.input, context.resource)
              .map((id) => driverLabel(id))
              .filter((label): label is string => Boolean(label)),
          ),
        ];
        return labels.length
          ? `${base} (via plugin driver ${labels.join(", ")})`
          : base;
      },
    };
  });
}

export const databaseSavedCapabilityNames = [
  "database.connect",
  "database.describe_schema",
  "database.open_connection",
  "database.open_connection_dialog",
  "database.edit_connection_dialog",
  "database.duplicate_connection",
  "database.toggle_connection_favorite",
  "database.rename_connection_group",
  "database.move_connection_to_group",
  "database.reorder_connection_groups",
  "database.delete_saved_connection",
];
export const databaseSessionCapabilityNames = [
  "database.disconnect",
  "database.describe_schema",
  "database.list_databases",
  "database.list_tables",
  "database.list_views",
  "database.list_columns",
  "database.list_indexes",
  "database.list_foreign_keys",
  "database.list_primary_keys",
  "database.get_table_ddl",
  "database.list_triggers",
  "database.get_table_rows",
  "database.insert_row",
  "database.update_row",
  "database.delete_row",
  "database.execute_query",
  "database.export_result",
  "database.import_rows",
  "database.cancel_query",
  "database.create_vector_collection",
];

function metadataString(
  resource: AiResource,
  key: "connectionId" | "sessionId",
) {
  const value = resource.metadata?.[key];
  if (typeof value !== "string" || !value)
    throw new Error(`Database resource has no bound ${key}`);
  return value;
}

function databaseName(input: Record<string, unknown>, capability: string) {
  return requiredString(input, "database", capability);
}

function tableName(input: Record<string, unknown>, capability: string) {
  return requiredString(input, "table", capability);
}

function requiredRecord(
  input: Record<string, unknown>,
  key: string,
  capability: string,
) {
  const value = input[key];
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).length === 0
  ) {
    throw new Error(`${capability} requires non-empty ${key}`);
  }
  return value as Record<string, unknown>;
}

function safeMutationRecord(
  input: Record<string, unknown>,
  key: string,
  capability: string,
) {
  const value = requiredRecord(input, key, capability);
  if (Object.keys(value).some(isSensitiveDataName)) {
    throw new Error(`${capability} cannot write sensitive credential fields`);
  }
  return value;
}

function boundedInteger(
  input: Record<string, unknown>,
  key: string,
  fallback: number,
  minimum: number,
  maximum: number,
) {
  const value = input[key];
  return typeof value === "number" && Number.isInteger(value)
    ? Math.max(minimum, Math.min(maximum, value))
    : fallback;
}

function safeJsonExportResult(value: unknown) {
  if (typeof value !== "string") return sanitizeAiResult(value);
  try {
    return sanitizeAiResult(JSON.parse(value));
  } catch {
    throw new Error(
      "database.export_result blocked invalid JSON export text from the model",
    );
  }
}

function safeCsvExportResult(value: unknown) {
  if (typeof value !== "string") {
    throw new Error(
      "database.export_result blocked invalid CSV export data from the model",
    );
  }
  const parsed = Papa.parse<string[]>(value, {
    delimiter: ",",
    skipEmptyLines: false,
  });
  if (parsed.errors.length > 0 || parsed.data.length === 0) {
    throw new Error(
      "database.export_result blocked invalid CSV export text from the model",
    );
  }
  const records = parsed.data.slice();
  const finalRecord = records[records.length - 1];
  if (
    value.endsWith("\n") &&
    finalRecord?.length === 1 &&
    finalRecord[0] === ""
  )
    records.pop();
  const header = records[0];
  if (
    !header ||
    header.length === 0 ||
    records.some((record) => record.length !== header.length)
  ) {
    throw new Error(
      "database.export_result blocked invalid CSV export text from the model",
    );
  }
  const sensitiveColumns = header.map(isSensitiveDataName);
  const sanitized = records.map((record, rowIndex) =>
    record.map((cell, columnIndex) => {
      if (rowIndex > 0 && sensitiveColumns[columnIndex]) return "[REDACTED]";
      return String(sanitizeAiResult(cell));
    }),
  );
  return `${Papa.unparse(sanitized, { delimiter: ",", newline: "\n", quotes: false, quoteChar: '"', escapeChar: '"' })}\n`;
}

function safeExportResult(value: unknown, format: string) {
  switch (format.toLowerCase()) {
    case "json":
      return safeJsonExportResult(value);
    case "csv":
      return safeCsvExportResult(value);
    default:
      throw new Error(
        "database.export_result blocked unsupported export format from the model",
      );
  }
}

function schemaDatabases(resource: AiResource) {
  const schema = resource.metadata?.schema;
  if (!schema || typeof schema !== "object") return [];
  const databases = (schema as { databases?: unknown }).databases;
  return Array.isArray(databases) ? databases : [];
}

function schemaTables(resource: AiResource, database: string) {
  const entry = schemaDatabases(resource).find(
    (item) =>
      item &&
      typeof item === "object" &&
      (item as { name?: unknown }).name === database,
  );
  if (!entry || typeof entry !== "object") return [];
  const tables = (entry as { tables?: unknown }).tables;
  return Array.isArray(tables) ? tables : [];
}

const schemaSyncProperties = {
  sourceConnectionId: { type: "string" },
  sourceDatabase: { type: "string" },
  targetConnectionId: { type: "string" },
  targetDatabase: { type: "string" },
};
const schemaSyncRequired = Object.keys(schemaSyncProperties);

function schemaSyncSelection(
  input: Record<string, unknown>,
  capability: string,
) {
  return {
    sourceConnectionId: requiredString(input, "sourceConnectionId", capability),
    sourceDatabase: requiredString(input, "sourceDatabase", capability),
    targetConnectionId: requiredString(input, "targetConnectionId", capability),
    targetDatabase: requiredString(input, "targetDatabase", capability),
  };
}

export function databaseCapabilities(
  dependencies: DatabaseToolDependencies,
): AiCapability[] {
  return labelPluginDriverApprovals(
    baseDatabaseCapabilities(dependencies),
    dependencies.databaseDriverLabel,
  );
}

function baseDatabaseCapabilities(
  dependencies: DatabaseToolDependencies,
): AiCapability[] {
  const safety = createAiSqlSafety(dependencies.invoke, dependencies.sqlSafety);
  const workspaceActions: AiCapability[] = dependencies.databaseAction
    ? [
        {
          module: "database",
          target: "none",
          discovery: true,
          tool: tool(
            "database.list_workspace_connections",
            "List non-sensitive database workspace connections, visible SQL tabs and table tabs.",
          ),
          mode: "read",
          risk: "read",
          execute: () =>
            dependencies.databaseAction!({ action: "list-workspace" }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.open_connection",
            "Connect and focus a saved database connection in the visible Database workspace.",
            { connectionId: { type: "string" } },
            ["connectionId"],
          ),
          mode: "write",
          risk: "network",
          approvalPreview: ({ input }) =>
            `Connect database ${requiredString(input, "connectionId", "database.open_connection")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "open-connection",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.open_connection",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.open_table_tab",
            "Open and focus a table tab in the visible Database workspace.",
            {
              connectionId: { type: "string" },
              database: { type: "string" },
              table: { type: "string" },
            },
            ["connectionId", "database", "table"],
          ),
          mode: "write",
          risk: "network",
          approvalPreview: ({ input }) =>
            `Open database table ${requiredString(input, "database", "database.open_table_tab")}.${requiredString(input, "table", "database.open_table_tab")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "open-table",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.open_table_tab",
              ),
              database: requiredString(
                input,
                "database",
                "database.open_table_tab",
              ),
              table: requiredString(input, "table", "database.open_table_tab"),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.open_table_details",
            "Open the table structure drawer in the visible Database workspace, showing columns, indexes, foreign keys and DDL for one table.",
            {
              connectionId: { type: "string" },
              database: { type: "string" },
              table: { type: "string" },
            },
            ["connectionId", "database", "table"],
          ),
          mode: "write",
          risk: "network",
          approvalPreview: ({ input }) =>
            `Open the structure drawer for ${requiredString(input, "database", "database.open_table_details")}.${requiredString(input, "table", "database.open_table_details")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "open-table-details",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.open_table_details",
              ),
              database: requiredString(
                input,
                "database",
                "database.open_table_details",
              ),
              table: requiredString(
                input,
                "table",
                "database.open_table_details",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.new_sql_tab",
            "Create and focus a visible SQL editor tab. SQL and name are optional.",
            { sql: { type: "string" }, name: { type: "string" } },
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            stringArgument(input, "name") || "新建 SQL 编辑器标签",
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "new-query",
              sql: stringArgument(input, "sql") || undefined,
              name: stringArgument(input, "name") || undefined,
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.close_sql_tab",
            "Close a visible SQL editor tab by ID, or the active tab when tabId is omitted.",
            { tabId: { type: "string" } },
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `Close database SQL tab ${stringArgument(input, "tabId") || "active"}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "close-query",
              tabId: stringArgument(input, "tabId") || undefined,
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.close_table_tab",
            "Close a visible database table tab by ID, or the active table tab when tabId is omitted.",
            { tabId: { type: "string" } },
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `Close database table tab ${stringArgument(input, "tabId") || "active"}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "close-table",
              tabId: stringArgument(input, "tabId") || undefined,
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.open_connection_dialog",
            "Open the visible new database connection dialog. The user enters credentials locally.",
          ),
          mode: "write",
          risk: "write",
          approvalPreview: () => "打开新建数据库连接窗口",
          execute: () =>
            dependencies.databaseAction!({ action: "new-connection-dialog" }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.edit_connection_dialog",
            "Open the visible editor for one saved database connection. Credentials remain local.",
            { connectionId: { type: "string" } },
            ["connectionId"],
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `编辑数据库连接 ${requiredString(input, "connectionId", "database.edit_connection_dialog")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "edit-connection-dialog",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.edit_connection_dialog",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.duplicate_connection",
            "Duplicate a saved database connection without exposing credentials.",
            { connectionId: { type: "string" } },
            ["connectionId"],
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `Duplicate database connection ${requiredString(input, "connectionId", "database.duplicate_connection")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "duplicate-connection",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.duplicate_connection",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.toggle_connection_favorite",
            "Toggle the favorite state of a saved database connection.",
            { connectionId: { type: "string" } },
            ["connectionId"],
          ),
          mode: "write",
          risk: "write",
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "toggle-favorite",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.toggle_connection_favorite",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.rename_connection_group",
            "Rename a saved database connection group.",
            { previousName: { type: "string" }, nextName: { type: "string" } },
            ["previousName", "nextName"],
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `Rename database group ${requiredString(input, "previousName", "database.rename_connection_group")} to ${requiredString(input, "nextName", "database.rename_connection_group")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "rename-group",
              previousName: requiredString(
                input,
                "previousName",
                "database.rename_connection_group",
              ),
              nextName: requiredString(
                input,
                "nextName",
                "database.rename_connection_group",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.move_connection_to_group",
            "Move a saved database connection to a group. Use an empty group name to ungroup it.",
            { connectionId: { type: "string" }, group: { type: "string" } },
            ["connectionId", "group"],
          ),
          mode: "write",
          risk: "write",
          approvalPreview: ({ input }) =>
            `Move database connection ${requiredString(input, "connectionId", "database.move_connection_to_group")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "move-group",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.move_connection_to_group",
              ),
              group: stringArgument(input, "group") || "",
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.reorder_connection_groups",
            "Reorder saved database connection groups by their complete ordered names.",
            {
              groups: { type: "array", items: { type: "string" }, minItems: 1 },
            },
            ["groups"],
          ),
          mode: "write",
          risk: "write",
          approvalPreview: () => "Reorder database connection groups",
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "reorder-groups",
              groups: input.groups,
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.delete_saved_connection",
            "Remove one saved database connection from MinCode. This never deletes the remote database.",
            { connectionId: { type: "string" } },
            ["connectionId"],
          ),
          mode: "write",
          risk: "dangerous",
          approvalPreview: ({ input }) =>
            `Remove saved database connection ${requiredString(input, "connectionId", "database.delete_saved_connection")}`,
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "delete-connection",
              connectionId: requiredString(
                input,
                "connectionId",
                "database.delete_saved_connection",
              ),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.compare_schema",
            "Compare two database schemas and list every structural difference with its id, kind and risk.",
            schemaSyncProperties,
            schemaSyncRequired,
          ),
          mode: "read",
          risk: "network",
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "compare-schema",
              ...schemaSyncSelection(input, "database.compare_schema"),
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.generate_schema_sync",
            "Generate the migration SQL that would bring the target schema in line with the source. This only previews statements; nothing runs.",
            {
              ...schemaSyncProperties,
              changeIds: {
                type: "array",
                items: { type: "string" },
                minItems: 1,
              },
            },
            schemaSyncRequired,
          ),
          mode: "read",
          risk: "network",
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "generate-schema-sync",
              ...schemaSyncSelection(input, "database.generate_schema_sync"),
              changeIds: input.changeIds,
            }),
        },
        {
          module: "database",
          target: "none",
          tool: tool(
            "database.execute_schema_sync",
            "Run the generated migration SQL against the target database. This alters the target schema and can drop tables, columns and indexes.",
            {
              ...schemaSyncProperties,
              changeIds: {
                type: "array",
                items: { type: "string" },
                minItems: 1,
              },
            },
            schemaSyncRequired,
          ),
          mode: "write",
          risk: "dangerous",
          approvalPreview: ({ input }) => {
            const selection = schemaSyncSelection(
              input,
              "database.execute_schema_sync",
            );
            const scope = Array.isArray(input.changeIds)
              ? `${input.changeIds.length} change(s)`
              : "all default changes";
            return `Alter ${selection.targetDatabase} to match ${selection.sourceDatabase} (${scope})`;
          },
          execute: ({ input }) =>
            dependencies.databaseAction!({
              action: "execute-schema-sync",
              ...schemaSyncSelection(input, "database.execute_schema_sync"),
              changeIds: input.changeIds,
            }),
        },
      ]
    : [];
  const database = resourceCapability;
  const tableProperties = {
    database: { type: "string" },
    table: { type: "string" },
  };
  const tableRead = (name: string, command: string, description: string) =>
    database({
      module: "database",
      tool: tool(name, description, tableProperties, ["database", "table"]),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke(command, {
            sessionId: metadataString(resource, "sessionId"),
            database: databaseName(input, name),
            table: tableName(input, name),
          }),
        ),
    });

  return [
    ...workspaceActions,
    database({
      module: "database",
      tool: tool(
        "database.connect",
        "Connect the bound saved database connection without exposing credentials.",
      ),
      mode: "write",
      risk: "network",
      execute: ({ resource }) =>
        dependencies.connectDatabase?.(
          metadataString(resource, "connectionId"),
        ),
    }),
    database({
      module: "database",
      tool: tool(
        "database.disconnect",
        "Disconnect the bound active database session.",
      ),
      mode: "write",
      risk: "write",
      execute: ({ resource }) =>
        dependencies.disconnectDatabase?.(
          metadataString(resource, "connectionId"),
        ),
    }),
    database({
      module: "database",
      tool: tool(
        "database.describe_schema",
        "Connect when necessary, then read the schema from the bound database connection.",
      ),
      mode: "read",
      risk: "network",
      execute: async ({ resource }) => {
        const connectionId = metadataString(resource, "connectionId");
        let sessionId =
          typeof resource.metadata?.sessionId === "string"
            ? resource.metadata.sessionId
            : dependencies.databaseSessionId?.(connectionId);
        if (!sessionId) {
          if (!dependencies.connectDatabase) {
            throw new Error(
              "database.describe_schema cannot connect the selected database in this workspace",
            );
          }
          await dependencies.connectDatabase(connectionId);
          sessionId = dependencies.databaseSessionId?.(connectionId);
        }
        if (!sessionId)
          throw new Error(
            "database.describe_schema could not establish a database session",
          );
        return sanitizeAiResult(
          await dependencies.invoke("db_get_schema", { sessionId }),
        );
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.list_databases",
        "List databases from the connected schema snapshot.",
      ),
      mode: "read",
      risk: "read",
      execute: ({ resource }) =>
        schemaDatabases(resource).map((item) => {
          const record =
            item && typeof item === "object"
              ? (item as Record<string, unknown>)
              : {};
          return { name: record.name };
        }),
    }),
    ...(["tables", "views"] as const).map((kind) =>
      database({
        module: "database",
        tool: tool(
          `database.list_${kind}`,
          `List ${kind} in a database from the connected schema snapshot.`,
          {
            database: { type: "string" },
          },
          ["database"],
        ),
        mode: "read",
        risk: "read",
        execute: ({ input, resource }) =>
          schemaTables(resource, databaseName(input, `database.list_${kind}`))
            .filter((item) => {
              const value =
                item && typeof item === "object"
                  ? String(
                      (item as Record<string, unknown>).kind ?? "",
                    ).toLowerCase()
                  : "";
              return kind === "views"
                ? value.includes("view")
                : value.includes("table");
            })
            .map((item) => ({ name: (item as Record<string, unknown>).name })),
      }),
    ),
    tableRead(
      "database.list_columns",
      "db_get_table_columns",
      "List columns for a table.",
    ),
    tableRead(
      "database.list_indexes",
      "db_list_indexes",
      "List indexes for a table.",
    ),
    tableRead(
      "database.list_foreign_keys",
      "db_list_foreign_keys",
      "List foreign keys for a table.",
    ),
    tableRead(
      "database.list_primary_keys",
      "db_get_primary_keys",
      "List primary keys for a table.",
    ),
    tableRead(
      "database.get_table_ddl",
      "db_get_table_ddl",
      "Read DDL for a table.",
    ),
    tableRead(
      "database.list_triggers",
      "db_list_triggers",
      "List triggers for a table.",
    ),
    database({
      module: "database",
      tool: tool(
        "database.get_table_rows",
        "Read a bounded page of sanitized rows from a table.",
        {
          ...tableProperties,
          limit: { type: "integer", minimum: 1, maximum: 500 },
          offset: { type: "integer", minimum: 0 },
        },
        ["database", "table"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("db_get_table_rows", {
            sessionId: metadataString(resource, "sessionId"),
            database: databaseName(input, "database.get_table_rows"),
            table: tableName(input, "database.get_table_rows"),
            limit: boundedInteger(input, "limit", 100, 1, 500),
            offset: boundedInteger(
              input,
              "offset",
              0,
              0,
              Number.MAX_SAFE_INTEGER,
            ),
          }),
        ),
    }),
    database({
      module: "database",
      tool: tool(
        "database.insert_row",
        "Insert one row into a bound table.",
        {
          ...tableProperties,
          fields: {
            type: "object",
            minProperties: 1,
            additionalProperties: true,
          },
        },
        ["database", "table", "fields"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        `${databaseName(input, "database.insert_row")}.${tableName(input, "database.insert_row")} (field values redacted)`,
      execute: ({ input, resource, conversationId }) => {
        const sessionId = metadataString(resource, "sessionId");
        const database = databaseName(input, "database.insert_row");
        const table = tableName(input, "database.insert_row");
        const fields = safeMutationRecord(input, "fields", "database.insert_row");
        return safety.runRowWrite({ sessionId, database, table, write: "insert", conversationId }, () =>
          safety.run({
            sessionId,
            database,
            executeFirst: true,
            execute: (token) =>
              dependencies.invoke("db_insert_row", { sessionId, database, table, fields, source: "ai", ...safety.tokenArgs(token) }),
          }));
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.update_row",
        "Update selected columns in one row identified by its primary key.",
        {
          ...tableProperties,
          primaryKey: {
            type: "object",
            minProperties: 1,
            additionalProperties: true,
          },
          fields: {
            type: "object",
            minProperties: 1,
            additionalProperties: true,
          },
        },
        ["database", "table", "primaryKey", "fields"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        `${databaseName(input, "database.update_row")}.${tableName(input, "database.update_row")} (${Object.keys(requiredRecord(input, "fields", "database.update_row")).join(", ")}; values redacted)`,
      execute: ({ input, resource, conversationId }) => {
        const sessionId = metadataString(resource, "sessionId");
        const database = databaseName(input, "database.update_row");
        const table = tableName(input, "database.update_row");
        const primaryKey = requiredRecord(
          input,
          "primaryKey",
          "database.update_row",
        );
        const fields = safeMutationRecord(
          input,
          "fields",
          "database.update_row",
        );
        return safety.runRowWrite({ sessionId, database, table, write: "update", conversationId }, async () => {
          const affected: Record<string, unknown> = {};
          for (const [column, value] of Object.entries(fields)) {
            affected[column] = await safety.run({
              sessionId,
              database,
              executeFirst: true,
              execute: (token) =>
                dependencies.invoke("db_update_cell", {
                  sessionId, database, table, primaryKey, column, value, source: "ai", ...safety.tokenArgs(token),
                }),
            });
          }
          return { affected };
        });
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.delete_row",
        "Delete one row identified by its primary key.",
        {
          ...tableProperties,
          primaryKey: {
            type: "object",
            minProperties: 1,
            additionalProperties: true,
          },
        },
        ["database", "table", "primaryKey"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        `${databaseName(input, "database.delete_row")}.${tableName(input, "database.delete_row")} (primary-key values redacted)`,
      execute: ({ input, resource, conversationId }) => {
        const sessionId = metadataString(resource, "sessionId");
        const database = databaseName(input, "database.delete_row");
        const table = tableName(input, "database.delete_row");
        const primaryKey = requiredRecord(input, "primaryKey", "database.delete_row");
        return safety.runRowWrite({ sessionId, database, table, write: "delete", conversationId }, () =>
          safety.run({
            sessionId,
            database,
            executeFirst: true,
            execute: (token) =>
              dependencies.invoke("db_delete_row", { sessionId, database, table, primaryKey, source: "ai", ...safety.tokenArgs(token) }),
          }));
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.execute_query",
        "Execute a query on the bound active database session.",
        {
          query: { type: "string" },
          database: { type: "string" },
        },
        ["query"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        requiredString(input, "query", "database.execute_query"),
      execute: async ({ input, resource }) => {
        const sessionId = metadataString(resource, "sessionId");
        const query = requiredString(input, "query", "database.execute_query");
        const database = stringArgument(input, "database") || null;
        // 先不带令牌执行：读语句没有额外往返；写语句由安全网检查，高风险必须由用户确认。
        return sanitizeAiResult(
          await safety.run({
            sessionId,
            query,
            database,
            executeFirst: true,
            execute: (token) =>
              dependencies.invoke("db_execute_query", {
                sessionId, query, database, queryId: crypto.randomUUID(), source: "ai", ...safety.tokenArgs(token),
              }),
          }),
        );
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.export_result",
        "Export a bound table result as CSV or JSON text without writing a local path.",
        {
          ...tableProperties,
          format: { type: "string", enum: ["csv", "json"] },
          limit: { type: "integer", minimum: 1, maximum: 10000 },
        },
        ["database", "table", "format"],
      ),
      mode: "read",
      risk: "dangerous",
      approvalPreview: ({ input }) => {
        const target = `${databaseName(input, "database.export_result")}.${tableName(input, "database.export_result")}`;
        const limit =
          typeof input.limit === "number"
            ? Math.max(1, Math.min(10000, Math.round(input.limit)))
            : 10000;
        return `${target} (limit: ${limit})`;
      },
      execute: async ({ input, resource }) => {
        const format = requiredString(
          input,
          "format",
          "database.export_result",
        );
        return safeExportResult(
          await dependencies.invoke("db_export_table", {
            sessionId: metadataString(resource, "sessionId"),
            database: databaseName(input, "database.export_result"),
            table: tableName(input, "database.export_result"),
            format,
            limit:
              typeof input.limit === "number"
                ? Math.max(1, Math.min(10000, Math.round(input.limit)))
                : 10000,
          }),
          format,
        );
      },
    }),
    database({
      module: "database",
      /**
       * 后端按 format 走 CSV/JSON 解析后直接 INSERT，行数不限。
       * 表名和条数都进审批文案，data 本身不放——里面可能是整表数据。
       */
      tool: tool(
        "database.import_rows",
        "Insert rows into a bound table from CSV or JSON text supplied here. Existing rows are not touched.",
        {
          ...tableProperties,
          format: { type: "string", enum: ["csv", "json"] },
          data: {
            type: "string",
            description:
              "CSV text with a header row, or a JSON array of objects.",
          },
        },
        ["database", "table", "format", "data"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) => {
        const target = `${databaseName(input, "database.import_rows")}.${tableName(input, "database.import_rows")}`;
        const data = typeof input.data === "string" ? input.data : "";
        const lines = data
          ? data.split("\n").filter((line) => line.trim()).length
          : 0;
        return `Insert into ${target} from ${requiredString(input, "format", "database.import_rows")} (${lines} line(s); data redacted)`;
      },
      execute: ({ input, resource }) => {
        const sessionId = metadataString(resource, "sessionId");
        const database = databaseName(input, "database.import_rows");
        const table = tableName(input, "database.import_rows");
        const format = requiredString(input, "format", "database.import_rows");
        const data = typeof input.data === "string" ? input.data : "";
        return safety.run({
          sessionId,
          database,
          operation: { kind: "import", table, format, data },
          execute: (token) => importInlineRows(sessionId, database, table, format, data, dependencies.invoke, safety.tokenArgs(token)),
        });
      },
    }),
    database({
      module: "database",
      tool: tool(
        "database.cancel_query",
        "Cancel a running query on the bound connection.",
        {
          queryId: {
            type: "string",
            description: "Query id reported when the query started.",
          },
        },
        ["queryId"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        `Cancel query ${requiredString(input, "queryId", "database.cancel_query")}`,
      execute: ({ input }) =>
        dependencies.invoke("db_cancel_query", {
          queryId: requiredString(input, "queryId", "database.cancel_query"),
        }),
    }),
    database({
      module: "database",
      tool: tool(
        "database.create_vector_collection",
        "Create a collection on a bound vector database connection.",
        {
          name: { type: "string" },
          dimension: { type: "integer", minimum: 1, maximum: 65536 },
          metric: {
            type: "string",
            description:
              "Distance metric such as cosine or l2; leave empty for the provider default.",
          },
          vectorName: {
            type: "string",
            description:
              "Vector field name; leave empty for the provider default.",
          },
          cloud: {
            type: "string",
            description:
              "Managed provider cloud, when the provider requires one.",
          },
          region: {
            type: "string",
            description:
              "Managed provider region, when the provider requires one.",
          },
        },
        ["name", "dimension"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        `Create vector collection ${requiredString(input, "name", "database.create_vector_collection")} with ${typeof input.dimension === "number" ? input.dimension : 0} dimension(s)`,
      execute: ({ input, resource }) =>
        dependencies.invoke("db_vector_create_collection", {
          sessionId: metadataString(resource, "sessionId"),
          name: requiredString(
            input,
            "name",
            "database.create_vector_collection",
          ),
          dimension:
            typeof input.dimension === "number"
              ? Math.max(1, Math.min(65536, Math.round(input.dimension)))
              : 0,
          metric: stringArgument(input, "metric"),
          cloud: stringArgument(input, "cloud"),
          region: stringArgument(input, "region"),
          vectorName: stringArgument(input, "vectorName"),
          options: {},
        }),
    }),
    {
      module: "database",
      target: "none",
      tool: tool(
        "database.check_drivers",
        "Check which ODBC drivers and driver install prerequisites are present on this machine.",
      ),
      mode: "read",
      risk: "read",
      execute: async () => ({
        odbc: sanitizeAiResult(
          await dependencies.invoke("db_check_odbc_drivers", {}),
        ),
        installEnvironment: sanitizeAiResult(
          await dependencies.invoke("db_detect_install_env", {}),
        ),
      }),
    },
  ];
}
