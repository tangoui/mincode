/**
 * Pure tab-navigation logic behind the workbench keybinding commands
 * (`workbench:next-tab`, `workbench:tab-N`, `workbench:close-tab`,
 * `workbench:reopen-closed`). The Vue side lives in
 * composables/use-workbench-tab-commands.ts.
 */
import type { WorkspaceTab, WorkspaceView } from "@/types/workspace";

/** A closed tab that `workbench:reopen-closed` can bring back. */
export type ClosedTabEntry =
  | { kind: "view"; view: WorkspaceView }
  | { kind: "terminal"; title?: string }
  | { kind: "document"; path: string; projectRoot?: string };

/** Upper bound of the per-workspace closed-tab stack. */
export const CLOSED_TAB_LIMIT = 20;

function closedEntryKey(entry: ClosedTabEntry): string {
  switch (entry.kind) {
    case "view":
      return `view:${entry.view}`;
    case "terminal":
      // Every closed terminal is a separate entry (a reopen starts a fresh shell).
      return "";
    case "document":
      return `document:${entry.path}`;
  }
}

/**
 * Pushes `entry` on top of `stack` (last element = most recent) and returns a
 * new array. An older entry for the same tab is dropped, and the stack keeps
 * at most `limit` entries.
 */
export function pushClosedEntry(
  stack: readonly ClosedTabEntry[],
  entry: ClosedTabEntry,
  limit = CLOSED_TAB_LIMIT,
): ClosedTabEntry[] {
  const key = closedEntryKey(entry);
  const next = key ? stack.filter((item) => closedEntryKey(item) !== key) : [...stack];
  next.push(entry);
  const max = Math.max(1, Math.floor(limit));
  return next.length > max ? next.slice(next.length - max) : next;
}

/** Pops the most recent entry; `entry` is undefined for an empty stack. */
export function popClosedEntry(stack: readonly ClosedTabEntry[]): {
  entry: ClosedTabEntry | undefined;
  rest: ClosedTabEntry[];
} {
  if (!stack.length) return { entry: undefined, rest: [] };
  return { entry: stack[stack.length - 1], rest: stack.slice(0, -1) };
}

/**
 * The closed-tab entry for a workbench tab, or null when the tab cannot be
 * reopened meaningfully (dashboard, per-connection tabs such as SFTP).
 * Singleton views use the id `${workspaceId}:${view}` (workbench.openView).
 */
export function closedEntryForTab(
  tab: Pick<WorkspaceTab, "id" | "workspaceId" | "view" | "title">,
): ClosedTabEntry | null {
  if (tab.view === "dashboard") return null;
  if (tab.id === `${tab.workspaceId}:${tab.view}`) return { kind: "view", view: tab.view };
  if (tab.view === "terminal") return { kind: "terminal", title: tab.title };
  return null;
}

/**
 * The tab `delta` steps away from `activeId`, wrapping around. With no active
 * tab (the dashboard) next goes to the first tab and previous to the last.
 */
export function adjacentTabId(ids: readonly string[], activeId: string | null | undefined, delta: number): string | undefined {
  if (!ids.length) return undefined;
  const index = activeId ? ids.indexOf(activeId) : -1;
  if (index < 0) return delta >= 0 ? ids[0] : ids[ids.length - 1];
  const length = ids.length;
  return ids[(((index + Math.trunc(delta)) % length) + length) % length];
}

/**
 * The tab for `workbench:tab-N` (1-based). As in VS Code and browsers,
 * `tab-9` always selects the last tab; other indexes past the end do nothing.
 */
export function tabIdAtIndex(ids: readonly string[], n: number): string | undefined {
  if (!ids.length || !Number.isInteger(n) || n < 1) return undefined;
  if (n >= 9) return ids[ids.length - 1];
  return ids[n - 1];
}

/** Where keyboard focus is relative to the project editor area. */
export type EditorAreaFocus = "editor-area" | "elsewhere" | "none";

/**
 * What `workbench:close-tab` / `workbench:close-all` act on: the document tabs
 * of the project editor while the focus is inside it (or nowhere, e.g. right
 * after the previous document closed) and it has tabs open; otherwise the
 * workbench tabs.
 */
export function closeTargetKind(input: {
  hasDocumentHost: boolean;
  focus: EditorAreaFocus;
  documentTabCount: number;
}): "document" | "workbench" {
  if (!input.hasDocumentHost || input.documentTabCount <= 0) return "workbench";
  return input.focus === "elsewhere" ? "workbench" : "document";
}
