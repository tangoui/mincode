/**
 * The variable tree paths the user expanded, remembered per debug session and
 * per method of the selected frame (its `name`), so a step or a return to a
 * method shows the same nodes open. A path missing from the new tree (a local
 * that left scope) stays remembered; a collapse forgets it. The pane's scroll
 * position is remembered the same way. The entries live in the sessions store
 * (variableExpansionFor), which drops a session's when it ends or is closed,
 * whether or not a pane is mounted; nothing is persisted.
 */
import { shallowRef, triggerRef, watch, type Ref } from "vue";
import { useDebugSessionsStore, type DebugFrameExpansion } from "@/stores/debug-sessions";

function isUnder(path: string, root: string): boolean {
  return path === root || path.startsWith(`${root}/`);
}

export interface DebugVariableExpansion {
  /** The expanded paths of the current session and method (empty without a selected frame). */
  expanded: Ref<Set<string>>;
  /**
   * Records an expand or collapse. `frame` is the method the tree was built
   * for (the selected frame may have changed while the new tree loads);
   * null records nothing. Without it, the current method.
   */
  markExpanded(path: string, frame?: string | null): void;
  markCollapsed(path: string, frame?: string | null): void;
  /**
   * The remembered paths under the given root paths, to expand again. The
   * first time a method is shown its paths start as `initial` (e.g. its first
   * cheap scope).
   */
  pathsToRestore(rootPaths: string[], initial?: string[]): string[];
  /** Remembers the pane's scroll position for the current session and method. */
  rememberScroll(top: number): void;
  /** The remembered scroll position of the current session and method (0 the first time). */
  scrollTop(): number;
}

export function useDebugVariableExpansion(sessionId: () => string, frameKey: () => string | null): DebugVariableExpansion {
  const sessions = useDebugSessionsStore();
  const expanded = shallowRef(new Set<string>());

  /** The session's entry for a method (created on first use). */
  function entryFor(frame: string): DebugFrameExpansion {
    const frames = sessions.variableExpansionFor(sessionId());
    let entry = frames.get(frame);
    if (!entry) {
      entry = { paths: new Set(), seeded: false, scrollTop: 0 };
      frames.set(frame, entry);
    }
    return entry;
  }

  /** The current session and method's entry (created on first use); none without a frame. */
  function current(): DebugFrameExpansion | undefined {
    const frame = frameKey();
    if (frame === null) {
      if (expanded.value.size) expanded.value = new Set();
      return undefined;
    }
    const entry = entryFor(frame);
    if (expanded.value !== entry.paths) expanded.value = entry.paths;
    return entry;
  }

  watch(() => [sessionId(), frameKey()] as const, () => void current(), { immediate: true });

  /** The entry an expand or collapse belongs to (see markExpanded). */
  function target(frame: string | null | undefined): DebugFrameExpansion | undefined {
    if (frame === undefined || frame === frameKey()) return current();
    return frame === null ? undefined : entryFor(frame);
  }

  function markExpanded(path: string, frame?: string | null) {
    const entry = target(frame);
    if (!entry) return;
    entry.seeded = true;
    if (entry.paths.has(path)) return;
    entry.paths.add(path);
    if (entry.paths === expanded.value) triggerRef(expanded);
  }

  function markCollapsed(path: string, frame?: string | null) {
    const entry = target(frame);
    if (!entry) return;
    entry.seeded = true;
    if (entry.paths.delete(path) && entry.paths === expanded.value) triggerRef(expanded);
  }

  function pathsToRestore(rootPaths: string[], initial: string[] = []): string[] {
    const entry = current();
    if (!entry) return [];
    // Only a stop that listed the scopes seeds the default: one whose scopes
    // failed to load must not leave the method without it for good.
    if (!entry.seeded && initial.length) {
      entry.seeded = true;
      for (const path of initial) entry.paths.add(path);
      triggerRef(expanded);
    }
    return [...entry.paths].filter((path) => rootPaths.some((root) => isUnder(path, root)));
  }

  function rememberScroll(top: number) {
    const entry = current();
    if (entry) entry.scrollTop = top;
  }

  function scrollTop(): number {
    return current()?.scrollTop ?? 0;
  }

  return { expanded, markExpanded, markCollapsed, pathsToRestore, rememberScroll, scrollTop };
}
