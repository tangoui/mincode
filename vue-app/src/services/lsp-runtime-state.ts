import { shallowReactive } from "vue";
import {
  applyWorkDoneProgress,
  parseWorkDoneProgress,
  pickLspProgress,
  type LspProgressSummary,
  type LspProgressToken,
  type LspServerProgressState,
} from "@/domain/projects/lsp-progress";

export type LspRuntimePhase = "starting" | "initializing" | "ready" | "failed" | "stopped";
export type LspRuntimeFailure = "start" | "initialize" | "sync" | "exit";
export interface LspRuntimeState {
  phase: LspRuntimePhase;
  failure?: LspRuntimeFailure;
}

// Transient state belongs to the project root, never to a global server id.
// Keep only failure categories: raw protocol errors can contain source content.
const states = shallowReactive(new Map<string, LspRuntimeState>());
// Sessions whose file-watch registration was acknowledged unwatched because the
// project exceeds the watch limits; kept apart from phase updates and cleared
// when the server starts again.
const watchLimited = shallowReactive(new Set<string>());
// Status-bar progress: display names, running baseline file scans and the
// server's work-done progress tokens. Cleared when a server stops or fails.
const names = shallowReactive(new Map<string, string>());
const scanning = shallowReactive(new Set<string>());
const progress = shallowReactive(new Map<string, ReadonlyMap<string, LspProgressToken>>());
const key = (root: string, serverId: string) => `${root}\0${serverId}`;

export function lspRuntimeState(root: string, serverId: string): LspRuntimeState | undefined {
  return states.get(key(root, serverId));
}

export function setLspRuntimeState(root: string, serverId: string, state: LspRuntimeState): void {
  const id = key(root, serverId);
  if (state.phase === "starting") watchLimited.delete(id);
  if (state.phase === "starting" || state.phase === "stopped" || state.phase === "failed") {
    scanning.delete(id);
    progress.delete(id);
  }
  states.set(id, state);
}

export function isLspWatchLimited(root: string, serverId: string): boolean {
  return watchLimited.has(key(root, serverId));
}

export function markLspWatchLimited(root: string, serverId: string): void {
  watchLimited.add(key(root, serverId));
}

/** The server's display name (from its start result), shown in the status bar. */
export function setLspServerName(root: string, serverId: string, name: string): void {
  if (name) names.set(key(root, serverId), name);
}

/** The backend's baseline file scan for a watch registration began or ended. */
export function setLspWatchScanning(root: string, serverId: string, active: boolean): void {
  if (active) scanning.add(key(root, serverId));
  else scanning.delete(key(root, serverId));
}

/** Applies a server's `$/progress` notification; malformed params are ignored. */
export function applyLspProgress(root: string, serverId: string, params: unknown): void {
  const parsed = parseWorkDoneProgress(params);
  if (!parsed) return;
  const id = key(root, serverId);
  const next = applyWorkDoneProgress(progress.get(id) ?? new Map(), parsed);
  if (next.size) progress.set(id, next);
  else progress.delete(id);
}

/** What the status bar shows while a language server gets ready; undefined when idle. */
export function lspStatusBarProgress(): LspProgressSummary | undefined {
  return pickLspProgress(lspProgressStates());
}

/**
 * Every known server session, for the status bar summary. Sessions belong to
 * the active project (others are stopped in the background), so no root filter.
 */
export function lspProgressStates(): LspServerProgressState[] {
  const ids = new Set<string>([...states.keys(), ...scanning, ...progress.keys()]);
  return [...ids].map((id) => ({
    name: names.get(id) ?? id.slice(id.indexOf("\0") + 1),
    phase: states.get(id)?.phase,
    scanning: scanning.has(id),
    tokens: progress.get(id) ?? new Map(),
  }));
}
