/**
 * Import of connections from other database tools: the IPC boundary for the
 * `db_import_*` commands (backend/dbimport). Previews never carry
 * passwords; decrypted secrets stay in the Go import session until commit.
 */
import {
  parseDatabaseImportPreview,
  parseDetectedImportLocations,
  type DatabaseImportCommitRow,
  type DatabaseImportFileSource,
  type DatabaseImportPreview,
  type DetectedImportLocation,
} from "@/domain/database/connection-import";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface DatabaseImportCommitResult {
  imported: number;
  connectionIds: string[];
}

/** Default locations of other tools' connection stores (existence only). */
export async function detectDatabaseImportLocations(): Promise<DetectedImportLocation[]> {
  if (!isDesktopRuntime()) return [];
  return parseDetectedImportLocations(await invokeBackend<unknown>("db_import_detect", {}));
}

export async function previewDatabaseImport(source: DatabaseImportFileSource, path: string): Promise<DatabaseImportPreview> {
  const preview = parseDatabaseImportPreview(await invokeBackend<unknown>("db_import_preview", { source, path }));
  if (!preview) throw new Error("db_import: malformed");
  return preview;
}

export async function commitDatabaseImport(args: {
  importId: string;
  group: string;
  keepFolders: boolean;
  rows: DatabaseImportCommitRow[];
}): Promise<DatabaseImportCommitResult> {
  const raw = await invokeBackend<unknown>("db_import_commit", args);
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const connectionIds = Array.isArray(record.connectionIds)
    ? record.connectionIds.filter((id): id is string => typeof id === "string" && id.length > 0)
    : [];
  return { imported: connectionIds.length, connectionIds };
}

/** Drops an import session (and its decrypted secrets) early; failures are ignored. */
export async function discardDatabaseImport(importId: string): Promise<void> {
  if (!importId || !isDesktopRuntime()) return;
  await invokeBackend<void>("db_import_discard", { importId }).catch(() => undefined);
}
