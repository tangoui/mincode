import type { BackendCommand } from "@/generated/backend-commands";

function createAbortError(message = "Operation was cancelled.") {
  return new DOMException(message, "AbortError");
}

function throwIfAborted(signal?: AbortSignal) {
  if (signal?.aborted) throw createAbortError();
}

export { createAbortError };

const CODE_SEARCH_MODES = ["auto", "path", "text", "symbol"] as const;
const MAX_QUERY_LENGTH = 256;
const MAX_RESULT_LIMIT = 100;
const MAX_DETAIL_LENGTH = 500;

export type ProjectCodeSearchMode = (typeof CODE_SEARCH_MODES)[number];

export interface ProjectCodeSearchScope {
  projectRoot: string;
  /** Optional project-relative file or directory scope. */
  path?: string;
}

export interface ProjectCodeSearchRequest extends ProjectCodeSearchScope {
  query: string;
  mode?: ProjectCodeSearchMode;
  limit?: number;
  signal?: AbortSignal;
}

export interface ProjectCodeSearchResult {
  path: string;
  kind: "path" | "text" | "symbol";
  line?: number;
  column?: number;
  /** Bounded source text suitable for a command-palette result preview. */
  detail?: string;
  score: number;
  reason: string;
}

export interface ProjectCodeSearchResponse {
  query: string;
  mode: ProjectCodeSearchMode;
  engine: string;
  filesConsidered: number;
  results: ProjectCodeSearchResult[];
  truncated: boolean;
  durationMs: number;
}

export interface ProjectCodeSearchRenderResult {
  kind: "file";
  title: string;
  subtitle?: string;
  search: {
    scope?: { path: string };
    detail?: string;
    line?: number;
    column?: number;
  };
  action: {
    type: "open-file";
    root: string;
    path: string;
    line?: number;
    column?: number;
  };
}

type Invoke = <T>(command: BackendCommand, args?: Record<string, unknown>) => Promise<T>;

function defaultInvoke<T>(command: BackendCommand, args: Record<string, unknown> = {}) {
  return import("@/services/desktop-bridge").then(({ invokeBackend }) =>
    invokeBackend<T>(command, args),
  );
}

let nextRequestId = 0;

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.floor(value)
    : undefined;
}

function boundedString(value: unknown, maxLength: number) {
  return typeof value === "string" ? value.slice(0, maxLength) : undefined;
}

export function createProjectCodeSearchRequestId() {
  nextRequestId = (nextRequestId + 1) % Number.MAX_SAFE_INTEGER;
  return `project-code-search-${Date.now().toString(36)}-${nextRequestId.toString(36)}`;
}

export function normalizeProjectCodeSearchRequest(
  request: Pick<ProjectCodeSearchRequest, "projectRoot" | "path" | "query" | "mode" | "limit">,
) {
  const projectRoot = request.projectRoot.trim();
  const query = request.query.trim();
  if (!projectRoot) throw new Error("projectRoot is required");
  if (!query) throw new Error("query is required");
  if (query.length > MAX_QUERY_LENGTH) {
    throw new Error(`query must be at most ${MAX_QUERY_LENGTH} characters`);
  }

  const path = request.path?.trim();
  if (path?.startsWith("/") || path?.split(/[\\/]/).includes("..")) {
    throw new Error("path must be project-relative");
  }
  const mode = request.mode ?? "auto";
  if (!CODE_SEARCH_MODES.includes(mode)) throw new Error(`Unsupported code search mode: ${mode}`);
  const limit = Math.max(1, Math.min(MAX_RESULT_LIMIT, Math.round(request.limit ?? 40)));

  return { projectRoot, scopePath: path || undefined, query, mode, limit };
}

export function parseProjectCodeSearchResponse(value: unknown): ProjectCodeSearchResponse {
  const record = asRecord(value) ?? {};
  const mode = CODE_SEARCH_MODES.includes(record.mode as ProjectCodeSearchMode)
    ? (record.mode as ProjectCodeSearchMode)
    : "auto";
  const rawResults = Array.isArray(record.results) ? record.results : [];
  const results = rawResults.flatMap((raw) => {
    const entry = asRecord(raw);
    const path = boundedString(entry?.relativePath, 4_096)?.trim();
    const kind = entry?.kind;
    if (!path || (kind !== "path" && kind !== "text" && kind !== "symbol")) return [];
    return [{
      path,
      kind,
      line: positiveInteger(entry?.line),
      column: positiveInteger(entry?.column),
      detail: boundedString(entry?.lineText, MAX_DETAIL_LENGTH),
      score: typeof entry?.score === "number" && Number.isFinite(entry.score) ? entry.score : 0,
      reason: boundedString(entry?.reason, 160) ?? "code search match",
    } satisfies ProjectCodeSearchResult];
  });

  return {
    query: boundedString(record.query, MAX_QUERY_LENGTH) ?? "",
    mode,
    engine: boundedString(record.engine, 80) ?? "",
    filesConsidered: positiveInteger(record.filesConsidered) ?? 0,
    results,
    truncated: record.truncated === true,
    durationMs: positiveInteger(record.durationMs) ?? 0,
  };
}

/** Maps a code-search match to the command palette's file render model. */
export function renderProjectCodeSearchResult(
  projectRoot: string,
  result: ProjectCodeSearchResult,
): ProjectCodeSearchRenderResult {
  const pathSegments = result.path.split("/");
  const title = pathSegments[pathSegments.length - 1] || result.path;
  return {
    kind: "file",
    title,
    subtitle: result.path,
    search: {
      scope: { path: result.path },
      detail: result.detail,
      line: result.line,
      column: result.column,
    },
    action: {
      type: "open-file",
      root: projectRoot,
      path: result.path,
      line: result.line,
      column: result.column,
    },
  };
}

async function cancelProjectCodeSearch(invoke: Invoke, requestId: string) {
  try {
    await invoke<void>("local_fs_cancel_code_search", { requestId });
  } catch {
    // Search completion or a detached desktop bridge may make cancellation unavailable.
  }
}

/**
 * Calls the native, project-bounded code search command. Aborting rejects with the
 * standard AbortError and asks the backend to stop the linked request best-effort.
 */
export async function searchProjectCode(
  request: ProjectCodeSearchRequest,
  invoke: Invoke = defaultInvoke,
): Promise<ProjectCodeSearchResponse> {
  const args = normalizeProjectCodeSearchRequest(request);
  throwIfAborted(request.signal);
  const requestId = createProjectCodeSearchRequestId();
  let aborted = false;
  const handleAbort = () => {
    aborted = true;
    void cancelProjectCodeSearch(invoke, requestId);
  };
  request.signal?.addEventListener("abort", handleAbort, { once: true });

  try {
    const response = await invoke<unknown>("local_fs_search_code", { ...args, requestId });
    if (aborted || request.signal?.aborted) throw createAbortError();
    return parseProjectCodeSearchResponse(response);
  } catch (error) {
    if (aborted || request.signal?.aborted) throw createAbortError();
    throw error;
  } finally {
    request.signal?.removeEventListener("abort", handleAbort);
  }
}
