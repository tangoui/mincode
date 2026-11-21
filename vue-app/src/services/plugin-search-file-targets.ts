/**
 * Opens a plugin search file result (contributes.searchProviders target
 * `{type:"file"}`). Loaded on demand from the command palette, never from the
 * entry chunk.
 *
 * Click chain (design §1.4): local scope check → Go re-validation through
 * `plugin_host_validate_search_file_target` (same scope, provider, runtime
 * instance and generation, path policy, existing regular file) → local scope
 * check again → the existing open-file action with the result's own root and
 * the host's canonical path/line/column. The root always comes from the
 * result's fileScope, never from the store at click time. Re-validation does
 * not remove OS-level TOCTOU; the open itself still goes through the Go file
 * boundary.
 */
import {
  normalizeSearchFileTarget,
  searchFileScopeIsCurrent,
  type SearchFileActivation,
  type SearchFileTarget,
} from "@/domain/plugins/search-providers";
import { parseContributionError, type ContributionError } from "@/services/plugin-contribution-requests";
import { currentPluginWorkspaceScope } from "@/services/plugin-workspace-scope";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export type { SearchFileActivation, SearchFileTarget } from "@/domain/plugins/search-providers";

/** A failed re-validation, carrying the host's contribution error code. */
export class SearchFileTargetError extends Error implements ContributionError {
  readonly code: ContributionError["code"];
  readonly detail: string;

  constructor(error: ContributionError) {
    super(error.detail || error.code);
    this.name = "SearchFileTargetError";
    this.code = error.code;
    this.detail = error.detail;
  }
}

/** Where the palette opens a validated result. */
export interface SearchFileOpenRequest {
  root: string;
  path: string;
  line?: number;
  column?: number;
}

export type SearchFileOpenOutcome =
  | { status: "opened" }
  /** The scope or the provider runtime changed since the search: search again. */
  | { status: "stale" }
  /** Browser preview: file results cannot be validated or opened. */
  | { status: "desktop-required" }
  | { status: "cancelled" }
  | { status: "failed"; error: ContributionError };

/**
 * Asks Go to re-validate one file result and returns its canonical target.
 * Rejects with SearchFileTargetError (code `unavailable` outside the desktop
 * runtime); `signal` cancels the native call.
 */
export async function validatePluginSearchFileTarget(
  input: SearchFileActivation,
  signal?: AbortSignal,
): Promise<SearchFileTarget> {
  if (!isDesktopRuntime()) throw new SearchFileTargetError({ code: "unavailable", detail: "desktop runtime required" });
  if (signal?.aborted) throw new SearchFileTargetError({ code: "cancelled", detail: "" });
  const { target } = input;
  let response: unknown;
  try {
    response = await invokeBackend<unknown>(
      "plugin_host_validate_search_file_target",
      {
        pluginId: input.pluginId,
        providerId: input.providerId,
        fileScope: { workspaceId: input.fileScope.workspaceId, root: input.fileScope.root, revision: input.fileScope.revision },
        providerRuntime: { instance: input.providerRuntime.instance, generation: input.providerRuntime.generation },
        path: target.path,
        ...(target.line !== undefined ? { line: target.line } : {}),
        ...(target.column !== undefined ? { column: target.column } : {}),
      },
      { signal },
    );
  } catch (error) {
    if (signal?.aborted) throw new SearchFileTargetError({ code: "cancelled", detail: "" });
    throw new SearchFileTargetError(parseContributionError(error));
  }
  const canonical = normalizeSearchFileTarget(response);
  if (!canonical) throw new SearchFileTargetError({ code: "invalid_result", detail: "file target validation returned an invalid target" });
  return canonical;
}

const isCurrent = (input: SearchFileActivation) => searchFileScopeIsCurrent(input.fileScope, currentPluginWorkspaceScope());

/**
 * Runs the full click chain and calls `open` only for a result that is still
 * current before and after the native re-validation. Never throws for
 * validation failures; `open` errors propagate.
 */
export async function openPluginSearchFileTarget(
  input: SearchFileActivation,
  open: (request: SearchFileOpenRequest) => Promise<void>,
  signal?: AbortSignal,
): Promise<SearchFileOpenOutcome> {
  if (!isDesktopRuntime()) return { status: "desktop-required" };
  if (!isCurrent(input)) return { status: "stale" };
  let target: SearchFileTarget;
  try {
    target = await validatePluginSearchFileTarget(input, signal);
  } catch (error) {
    const failure: ContributionError = error instanceof SearchFileTargetError ? error : parseContributionError(error);
    if (failure.code === "cancelled" || signal?.aborted) return { status: "cancelled" };
    // The host reports scope and runtime changes as `unavailable`.
    if (failure.code === "unavailable" || !isCurrent(input)) return { status: "stale" };
    return { status: "failed", error: { code: failure.code, detail: failure.detail } };
  }
  if (signal?.aborted) return { status: "cancelled" };
  if (!isCurrent(input)) return { status: "stale" };
  await open({
    root: input.fileScope.root,
    path: target.path,
    ...(target.line !== undefined ? { line: target.line } : {}),
    ...(target.column !== undefined ? { column: target.column } : {}),
  });
  return { status: "opened" };
}
