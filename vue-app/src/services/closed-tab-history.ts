/**
 * In-memory stack of recently closed tabs for `workbench:reopen-closed`.
 *
 * Runtime state scoped by workbench workspace ID (tabs belong to a workbench
 * workspace, see stores/workbench.ts), bounded by CLOSED_TAB_LIMIT and never
 * persisted: a restart or reload starts with an empty stack.
 */
import {
  CLOSED_TAB_LIMIT,
  closedEntryForTab,
  popClosedEntry,
  pushClosedEntry,
  type ClosedTabEntry,
} from "@/domain/workbench/tab-navigation";
import type { WorkspaceTab } from "@/types/workspace";

const stacks = new Map<string, ClosedTabEntry[]>();

export function recordClosedEntry(workspaceId: string, entry: ClosedTabEntry): void {
  if (!workspaceId) return;
  stacks.set(workspaceId, pushClosedEntry(stacks.get(workspaceId) ?? [], entry, CLOSED_TAB_LIMIT));
}

/** Records closed workbench tabs (in closing order); tabs that cannot be reopened are skipped. */
export function recordClosedTabs(tabs: readonly WorkspaceTab[]): void {
  for (const tab of tabs) {
    const entry = closedEntryForTab(tab);
    if (entry) recordClosedEntry(tab.workspaceId, entry);
  }
}

export function takeClosedEntry(workspaceId: string): ClosedTabEntry | undefined {
  const { entry, rest } = popClosedEntry(stacks.get(workspaceId) ?? []);
  stacks.set(workspaceId, rest);
  return entry;
}
