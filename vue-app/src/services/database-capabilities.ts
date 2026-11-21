import type { DbType } from "@/domain/database/types";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

let supportedTypesPromise: Promise<Set<DbType> | null> | null = null;

export function loadSupportedDatabaseTypes(): Promise<Set<DbType> | null> {
  if (!isDesktopRuntime()) return Promise.resolve(null);
  supportedTypesPromise ??= invokeBackend<DbType[]>("db_supported_types")
    .then((types) => new Set(types))
    .catch(() => null);
  return supportedTypesPromise;
}
