/**
 * Shared inline-import boundary; injected transport preserves AI approval/session binding.
 * `safety` carries the SQL safety-net token (db_safety_check operation {kind: "import", table, format, data}).
 */
export async function importInlineRows(
  sessionId: string, database: string, table: string, format: string, data: string,
  transport: (command: "db_import_data", args: Record<string, unknown>) => Promise<unknown>,
  safety: { checkId?: string; confirmText?: string } = {},
): Promise<number> {
  const value = await transport("db_import_data", { sessionId, database, table, format, data, ...safety });
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw new Error("Malformed import result");
  return value;
}
