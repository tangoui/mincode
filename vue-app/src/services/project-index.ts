import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface ProjectIndexFileEntry {
  root: string;
  path: string;
  name: string;
  size: number;
  modifiedAt: number;
}

export interface ProjectIndexRootResult {
  root: string;
  files: number;
  changed: number;
  truncated: boolean;
}

export interface ProjectIndexRefreshResult {
  available: boolean;
  roots: ProjectIndexRootResult[];
}

export async function refreshProjectFileIndex(
  roots: readonly string[]
): Promise<ProjectIndexRefreshResult> {
  if (!isDesktopRuntime()) return { available: false, roots: [] };
  const result = await invokeBackend<{ roots?: ProjectIndexRootResult[] }>("project_index_refresh", {
    roots: [...roots],
  });
  return { available: true, roots: Array.isArray(result?.roots) ? result.roots : [] };
}

export async function searchProjectFileIndex(
  roots: readonly string[],
  query: string,
  limit = 80
): Promise<ProjectIndexFileEntry[]> {
  if (!isDesktopRuntime()) return [];
  const result = await invokeBackend<unknown>("project_index_search", {
    roots: [...roots],
    query,
    limit,
  });
  if (!Array.isArray(result)) return [];
  return result.flatMap((raw) => {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return [];
    const entry = raw as Partial<ProjectIndexFileEntry>;
    if (
      typeof entry.root !== "string" ||
      typeof entry.path !== "string" ||
      typeof entry.name !== "string" ||
      typeof entry.size !== "number" ||
      typeof entry.modifiedAt !== "number"
    ) {
      return [];
    }
    return [entry as ProjectIndexFileEntry];
  });
}
