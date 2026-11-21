/**
 * Persistent per-project debugger state: run configurations, the selected
 * configuration, breakpoints (by project file, and by debugger address for
 * debugger-provided sources), exception filters and exception type lists,
 * method breakpoints, field breakpoints (persistable ones), watch expressions and the debug panel height, all under `app_project_debug_v1` keyed by project root.
 * Intentionally global (not workspace-scoped): like the project itself, a
 * project's run configurations and breakpoints are the same in every
 * workspace that opens it. Nothing is written into the user's project.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { ElMessage } from "element-plus";
import {
  PROJECT_DEBUG_STORAGE_KEY,
  emptyProjectDebugState,
  normalizeProjectDebugStates,
  type ProjectDebugState,
  type ProjectDebugStates,
} from "@/domain/debug/configurations";
import { DEBUGGER_ADDRESS_LIMITS, breakpointAtKey, isValidDebuggerAddress, type BreakpointAt } from "@/domain/debug/address-breakpoints";
import type { StoredDataBreakpoint } from "@/domain/debug/data-breakpoints";
import type { StoredFunctionBreakpoint } from "@/domain/debug/function-breakpoints";
import { existingKey, projectRootKey, toRelativeKey } from "@/domain/debug/paths";
import type { RunConfiguration, SourceBreakpoint } from "@/domain/debug/types";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import { useVueI18n } from "@/i18n/messages";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { registerUiSessionFlusher } from "@/services/ui-session";

const SAVE_DEBOUNCE_MS = 300;

/** The live store's flush; App's quit / close flush (flushPersistentUiState) writes the pending save through it. */
let flushLiveStore: (() => Promise<void>) | undefined;
registerUiSessionFlusher(() => flushLiveStore?.());

/** macOS and Windows file systems compare paths case-insensitively by default. */
export function pathsAreCaseInsensitive(): boolean {
  return currentKeyboardPlatform() !== "linux";
}

/** Both roots name the same project (trailing separators, `\` vs `/` and, where paths fold case, case ignored). */
export function sameProjectRoot(a: string, b: string): boolean {
  const left = projectRootKey(a).replace(/\\/g, "/");
  const right = projectRootKey(b).replace(/\\/g, "/");
  return pathsAreCaseInsensitive() ? left.toLowerCase() === right.toLowerCase() : left === right;
}

type StateChange = (state: ProjectDebugState) => void;

function sortedByLine(list: SourceBreakpoint[]): SourceBreakpoint[] {
  return [...list].sort((a, b) => a.line - b.line);
}

export const useDebugConfigsStore = defineStore("debug-configs", () => {
  const { t } = useVueI18n();
  const projects = ref<ProjectDebugStates>({});
  /** True once the stored value was read; nothing is saved before that. */
  const loaded = ref(false);
  let loading: Promise<void> | undefined;
  /** Changes made before a load succeeded, replayed onto the stored state. */
  let pendingChanges: Array<{ root: string; change: StateChange }> = [];
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  /** A failed save was reported; the next failure stays quiet until a save succeeds. */
  let saveFailureShown = false;
  /** Bumped by every breakpoint change (in memory only). */
  let breakpointRevision = 0;
  /** root + "\0" + breakpointAtKey(at) → the revision of its last breakpoint change; one entry per file or address. */
  const breakpointChanges = new Map<string, { root: string; at: BreakpointAt; revision: number }>();

  /**
   * Reads the stored value once. A failed read leaves the store unloaded (so
   * no save can overwrite other projects with a partial map) and the next
   * call retries; changes made meanwhile are replayed after a good read.
   */
  function load(): Promise<void> {
    if (loaded.value) return Promise.resolve();
    loading ??= loadPersistedValue<unknown>(PROJECT_DEBUG_STORAGE_KEY).then(
      (value) => {
        const stored = normalizeProjectDebugStates(value);
        const replay = pendingChanges;
        pendingChanges = [];
        for (const { root, change } of replay) applyChange(stored, root, change);
        projects.value = stored;
        loaded.value = true;
        if (replay.length) scheduleSave();
      },
      () => {
        loading = undefined;
      }
    );
    return loading;
  }

  async function save(): Promise<void> {
    saveTimer = undefined;
    await load();
    // Never overwrite other projects' stored state with a partial value.
    if (!loaded.value) return;
    const snapshot = JSON.parse(JSON.stringify(projects.value)) as ProjectDebugStates;
    try {
      await savePersistedValue(PROJECT_DEBUG_STORAGE_KEY, snapshot);
      saveFailureShown = false;
    } catch (error) {
      if (saveFailureShown) return;
      saveFailureShown = true;
      const reason = error instanceof Error ? error.message : String(error);
      ElMessage.error({ message: t("debug.config.saveFailed", { error: reason }), showClose: true, grouping: true });
    }
  }

  /** The save being written, so a flush also waits for one the timer already started. */
  let saving: Promise<void> | undefined;

  function runSave(): Promise<void> {
    const current = save().finally(() => {
      if (saving === current) saving = undefined;
    });
    saving = current;
    return current;
  }

  function scheduleSave() {
    if (saveTimer !== undefined) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => void runSave(), SAVE_DEBOUNCE_MS);
  }

  /** Writes a pending debounced save now (e.g. before the window closes). */
  function flush(): Promise<void> {
    if (saveTimer === undefined) return saving ?? Promise.resolve();
    clearTimeout(saveTimer);
    return runSave();
  }

  /** The map key of a project root: normalized, and the stored spelling when one matches ignoring case. */
  function rootKeyIn(states: ProjectDebugStates, root: string): string {
    return existingKey(Object.keys(states), projectRootKey(root), pathsAreCaseInsensitive());
  }

  /** The project's state; a project without stored state reads as empty (not inserted). */
  function stateFor(root: string): ProjectDebugState {
    return projects.value[rootKeyIn(projects.value, root)] ?? emptyProjectDebugState();
  }

  /** Runs change on the root's state in states; a new project is kept only if the change left it non-empty. */
  function applyChange(states: ProjectDebugStates, root: string, change: StateChange) {
    const key = rootKeyIn(states, root);
    const existing = states[key];
    const state = existing ?? emptyProjectDebugState();
    change(state);
    if (!existing && JSON.stringify(state) !== JSON.stringify(emptyProjectDebugState())) states[key] = state;
  }

  /**
   * Applies a change now and schedules a save. Before a load succeeded the
   * change is also queued, so it is replayed onto the stored state instead
   * of replacing it; changes therefore read the state they are given.
   */
  function mutate(root: string, change: StateChange) {
    if (!loaded.value) pendingChanges.push({ root, change });
    applyChange(projects.value, root, change);
    scheduleSave();
  }

  /** Stores a copy, so later changes to the caller's object stay out of the store. */
  function upsertConfig(root: string, cfg: RunConfiguration) {
    const json = JSON.stringify(cfg);
    mutate(root, (state) => {
      const copy = JSON.parse(json) as RunConfiguration;
      const index = state.configs.findIndex((item) => item.id === copy.id);
      if (index >= 0) state.configs.splice(index, 1, copy);
      else state.configs.push(copy);
    });
  }

  /** Removing the selected configuration selects the first remaining one. */
  function removeConfig(root: string, id: string) {
    mutate(root, (state) => {
      state.configs = state.configs.filter((item) => item.id !== id);
      if (state.selectedConfigId === id) state.selectedConfigId = state.configs[0]?.id ?? null;
    });
  }

  /** Selects a configuration of the project; unknown ids are ignored, null clears. */
  function selectConfig(root: string, id: string | null) {
    mutate(root, (state) => {
      if (id === null || state.configs.some((item) => item.id === id)) state.selectedConfigId = id;
    });
  }

  /**
   * Where at's breakpoint list lives in state: the project-relative key of a
   * file (null outside the project), or an address verbatim under its
   * debugger type (null when the host would refuse it or the type is empty).
   * `map` is undefined for a type without address breakpoints yet; `create`
   * makes it (only while mutating).
   */
  function breakpointSlot(
    state: ProjectDebugState,
    root: string,
    at: BreakpointAt,
    create = false,
  ): { map: Record<string, SourceBreakpoint[]> | undefined; key: string; address: boolean } | null {
    if (typeof at !== "string") {
      if (!at.type || !isValidDebuggerAddress(at.address)) return null;
      const map = create ? (state.addressBreakpoints[at.type] ??= {}) : state.addressBreakpoints[at.type];
      return { map, key: at.address, address: true };
    }
    const caseInsensitive = pathsAreCaseInsensitive();
    const key = toRelativeKey(root, at, caseInsensitive);
    return key === null ? null : { map: state.breakpoints, key: existingKey(Object.keys(state.breakpoints), key, caseInsensitive), address: false };
  }

  /**
   * Replaces the breakpoint list of a file or debugger address (sorted; an
   * empty list removes the key). A file outside the project, an address the
   * host would refuse, or a new address past DEBUGGER_ADDRESS_LIMITS.maxAddresses
   * (per debugger type) changes nothing and returns false.
   */
  function updateBreakpoints(root: string, at: BreakpointAt, change: (list: SourceBreakpoint[]) => SourceBreakpoint[]): boolean {
    if (breakpointSlot(emptyProjectDebugState(), root, at) === null) return false;
    let applied = true;
    mutate(root, (state) => {
      const slot = breakpointSlot(state, root, at, true);
      if (slot === null || slot.map === undefined) return;
      const map = slot.map;
      const existing = map[slot.key];
      const next = sortedByLine(change(existing ?? []));
      if (!existing && next.length && slot.address && Object.keys(map).length >= DEBUGGER_ADDRESS_LIMITS.maxAddresses) {
        applied = false;
      } else if (next.length) map[slot.key] = next;
      else delete map[slot.key];
      // A type left without address breakpoints is not kept.
      if (slot.address && typeof at !== "string" && !Object.keys(map).length) delete state.addressBreakpoints[at.type];
    });
    if (!applied) return false;
    breakpointRevision += 1;
    breakpointChanges.set(`${root}\0${breakpointAtKey(at)}`, { root, at, revision: breakpointRevision });
    return true;
  }

  /** The current breakpoint revision, for `breakpointsChangedSince`. */
  function currentBreakpointRevision(): number {
    return breakpointRevision;
  }

  /**
   * The project's files (absolute paths) and debugger addresses whose
   * breakpoints changed after `revision` (e.g. while a launch was starting
   * with an older snapshot).
   */
  function breakpointsChangedSince(root: string, revision: number): BreakpointAt[] {
    const changed = new Map<string, BreakpointAt>();
    for (const change of breakpointChanges.values()) {
      if (change.revision > revision && sameProjectRoot(change.root, root)) changed.set(breakpointAtKey(change.at), change.at);
    }
    return [...changed.values()];
  }

  /** The breakpoints of a project file (absolute path) or a debugger address. */
  function breakpointsFor(root: string, at: BreakpointAt): SourceBreakpoint[] {
    const state = stateFor(root);
    const slot = breakpointSlot(state, root, at);
    return slot?.map?.[slot.key] ?? [];
  }

  /**
   * Adds an enabled breakpoint on the line, or removes the one there. False
   * when nothing could change (see updateBreakpoints).
   */
  function toggleBreakpoint(root: string, at: BreakpointAt, line: number): boolean {
    return updateBreakpoints(root, at, (list) =>
      list.some((bp) => bp.line === line) ? list.filter((bp) => bp.line !== line) : [...list, { line, enabled: true }]
    );
  }

  /** Adds or replaces the breakpoint on bp.line. */
  function setBreakpoint(root: string, at: BreakpointAt, bp: SourceBreakpoint): boolean {
    return updateBreakpoints(root, at, (list) => [...list.filter((item) => item.line !== bp.line), { ...bp }]);
  }

  function removeBreakpoint(root: string, at: BreakpointAt, line: number): boolean {
    return updateBreakpoints(root, at, (list) => list.filter((bp) => bp.line !== line));
  }

  /**
   * Applies editor line moves at once. Breakpoints not named keep their line;
   * one moved below line 1 (its line was deleted) is dropped. When several
   * land on the same line, the line's own breakpoint (one that did not move)
   * keeps it, with its condition; otherwise the one that was highest in the file.
   */
  function moveBreakpoints(root: string, at: BreakpointAt, lines: { from: number; to: number }[]) {
    const moves = new Map(lines.map(({ from, to }) => [from, to]));
    updateBreakpoints(root, at, (list) => {
      const byLine = new Map<number, SourceBreakpoint>();
      const target = (bp: SourceBreakpoint) => moves.get(bp.line) ?? bp.line;
      const sorted = sortedByLine(list);
      const staying = sorted.filter((bp) => target(bp) === bp.line);
      const moving = sorted.filter((bp) => target(bp) !== bp.line);
      for (const bp of [...staying, ...moving]) {
        const line = target(bp);
        if (line < 1 || byLine.has(line)) continue;
        byLine.set(line, { ...bp, line });
      }
      return [...byLine.values()];
    });
  }

  // The same operations on a debugger type's address (debugger-provided sources).
  const addressBreakpointsFor = (root: string, type: string, address: string) => breakpointsFor(root, { address, type });
  const toggleAddressBreakpoint = (root: string, type: string, address: string, line: number) => toggleBreakpoint(root, { address, type }, line);
  const setAddressBreakpoint = (root: string, type: string, address: string, bp: SourceBreakpoint) => setBreakpoint(root, { address, type }, bp);
  const removeAddressBreakpoint = (root: string, type: string, address: string, line: number) => removeBreakpoint(root, { address, type }, line);

  function setExceptionFilters(root: string, type: string, filters: string[]) {
    const copy = [...filters];
    mutate(root, (state) => {
      state.exceptionFilters[type] = [...copy];
    });
  }

  /** Saves a debugger type's exception type list (already normalized; [] means no restriction). */
  function setExceptionTypes(root: string, type: string, types: readonly string[]) {
    const copy = [...types];
    mutate(root, (state) => {
      state.exceptionTypes[type] = [...copy];
    });
  }

  /**
   * Saves a debugger type's method breakpoints (names already normalized, in
   * display order); an empty list removes the type's entry.
   */
  function setFunctionBreakpoints(root: string, type: string, list: readonly StoredFunctionBreakpoint[]) {
    const json = JSON.stringify(list.map(({ name, enabled }) => ({ name, enabled })));
    mutate(root, (state) => {
      const copy = JSON.parse(json) as StoredFunctionBreakpoint[];
      if (copy.length) state.functionBreakpoints[type] = copy;
      else delete state.functionBreakpoints[type];
    });
  }

  /**
   * Saves a debugger type's persistable field breakpoints (in the order they
   * were set); an empty list removes the type's entry.
   */
  function setDataBreakpoints(root: string, type: string, list: readonly StoredDataBreakpoint[]) {
    const json = JSON.stringify(list.map(({ dataId, description, accessType }) => ({ dataId, description, accessType })));
    mutate(root, (state) => {
      const copy = JSON.parse(json) as StoredDataBreakpoint[];
      if (copy.length) state.dataBreakpoints[type] = copy;
      else delete state.dataBreakpoints[type];
    });
  }

  function setWatches(root: string, expressions: string[]) {
    const copy = [...expressions];
    mutate(root, (state) => {
      state.watches = [...copy];
    });
  }

  /** Stores a rounded positive height; other values are ignored. */
  function setPanelHeight(root: string, px: number) {
    if (!Number.isFinite(px) || px <= 0) return;
    mutate(root, (state) => {
      state.panelHeight = Math.round(px);
    });
  }

  flushLiveStore = flush;

  return {
    projects,
    loaded,
    load,
    flush,
    stateFor,
    upsertConfig,
    removeConfig,
    selectConfig,
    toggleBreakpoint,
    setBreakpoint,
    removeBreakpoint,
    moveBreakpoints,
    breakpointsFor,
    addressBreakpointsFor,
    toggleAddressBreakpoint,
    setAddressBreakpoint,
    removeAddressBreakpoint,
    currentBreakpointRevision,
    breakpointsChangedSince,
    setExceptionFilters,
    setExceptionTypes,
    setFunctionBreakpoints,
    setDataBreakpoints,
    setWatches,
    setPanelHeight,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDebugConfigsStore, import.meta.hot));
}
