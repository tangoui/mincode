/**
 * Seam for contribution points that register data with Monaco (snippets,
 * editor themes, future languages/grammars). Hooks run once Monaco has
 * loaded, when the first editor installs the MinCode keymap
 * (`ensureMonacoKeymap`), or immediately when registered after that. This
 * module only imports Monaco types, so it never loads Monaco eagerly.
 */
import type * as MonacoRuntime from "@/services/monaco-runtime.js";

export type MonacoApi = typeof MonacoRuntime;
export type MonacoContributionHook = (monaco: MonacoApi) => (() => void) | void;

interface HookState {
  hook: MonacoContributionHook;
  dispose?: () => void;
}

const hooks = new Set<HookState>();
let loaded: MonacoApi | undefined;
const MAX_ERRORS = 32;
const errors: string[] = [];

function recordError(operation: string, error: unknown) {
  errors.push(`${operation}: ${error instanceof Error ? error.message : String(error)}`);
  if (errors.length > MAX_ERRORS) errors.splice(0, errors.length - MAX_ERRORS);
}

/** Recent hook failures (diagnostics); a failing hook never breaks editor loading. */
export function monacoContributionErrors(): readonly string[] {
  return errors;
}

function run(state: HookState, monaco: MonacoApi) {
  try {
    state.dispose = state.hook(monaco) ?? undefined;
  } catch (error) {
    recordError("monaco contribution hook", error);
  }
}

/**
 * Registers a hook that receives the Monaco API. Returns a disposer that
 * removes the hook and disposes what it registered.
 */
export function registerMonacoContributionHook(hook: MonacoContributionHook): () => void {
  const state: HookState = { hook };
  hooks.add(state);
  if (loaded) run(state, loaded);
  return () => {
    if (!hooks.delete(state)) return;
    const dispose = state.dispose;
    state.dispose = undefined;
    try {
      dispose?.();
    } catch (error) {
      recordError("dispose monaco contribution hook", error);
    }
  };
}

/** Called once Monaco is loaded; runs every pending hook. Idempotent. */
export function ensureMonacoContributions(monaco: MonacoApi): void {
  if (loaded) return;
  loaded = monaco;
  for (const state of hooks) run(state, monaco);
}
