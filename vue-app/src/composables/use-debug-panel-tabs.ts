/**
 * The project view's debug panel tabs: the launches in flight (one tab each
 * until its session exists, showing DebugLaunchProgress) followed by the debug
 * sessions, of the projects the view owns. A new tab (a launch, then the
 * session it started or restarted) opens the panel on it. A launch gets its
 * tab only once it reaches its second step or has run LAUNCH_TAB_DELAY_MS, so
 * one refused at once (a guard, a missing tool) does not flash the panel.
 * Launch tabs keep their own selection; session tabs use the sessions store's.
 */
import { computed, onScopeDispose, ref, watch } from "vue";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

export interface DebugPanelTab {
  id: string;
  title: string;
  closable: boolean;
  /** A session's state, or "starting" for a launch. */
  state: string;
}

const LAUNCH_TAB_PREFIX = "launch:";

/** How long a launch still in its first step goes without a tab. */
export const LAUNCH_TAB_DELAY_MS = 300;

/** The launch seq of a launch tab id; null for a session tab. */
export function launchTabSeq(id: string): number | null {
  if (!id.startsWith(LAUNCH_TAB_PREFIX)) return null;
  const seq = Number(id.slice(LAUNCH_TAB_PREFIX.length));
  return Number.isInteger(seq) ? seq : null;
}

export function useDebugPanelTabs(owns: (root: string) => boolean, reveal: () => void) {
  const sessions = useDebugSessionsStore();

  /** Launches (seq) shown although still in their first step: they ran LAUNCH_TAB_DELAY_MS. */
  const delayedShown = ref<ReadonlySet<number>>(new Set());
  const delayTimers = new Map<number, ReturnType<typeof setTimeout>>();
  watch(
    () => sessions.launches.map((launch) => launch.seq),
    (seqs) => {
      for (const seq of seqs) {
        if (delayTimers.has(seq) || delayedShown.value.has(seq)) continue;
        delayTimers.set(
          seq,
          setTimeout(() => {
            delayTimers.delete(seq);
            delayedShown.value = new Set([...delayedShown.value, seq]);
          }, LAUNCH_TAB_DELAY_MS),
        );
      }
      for (const [seq, timer] of delayTimers) {
        if (seqs.includes(seq)) continue;
        clearTimeout(timer);
        delayTimers.delete(seq);
      }
      if ([...delayedShown.value].some((seq) => !seqs.includes(seq))) delayedShown.value = new Set(seqs.filter((seq) => delayedShown.value.has(seq)));
    },
    { immediate: true },
  );
  onScopeDispose(() => {
    for (const timer of delayTimers.values()) clearTimeout(timer);
    delayTimers.clear();
  });

  // Every session event (each output batch too) replaces a session view; the
  // tabs keep their identity unless an id, title or state changed, so program
  // output does not re-render the view.
  const tabs = computed<DebugPanelTab[]>((previous) => {
    const next = [
      ...sessions.launches
        .filter((launch) => owns(launch.root) && (launch.step !== "checking" || delayedShown.value.has(launch.seq)))
        .map((launch) => ({ id: `${LAUNCH_TAB_PREFIX}${launch.seq}`, title: launch.configName, closable: false, state: "starting" })),
      ...sessions.sessions
        .filter((session) => owns(session.projectRoot))
        .map((session) => ({ id: session.id, title: session.configName, closable: true, state: session.state })),
    ];
    const same =
      previous?.length === next.length &&
      next.every((tab, index) => tab.id === previous[index]!.id && tab.title === previous[index]!.title && tab.state === previous[index]!.state);
    return same ? previous : next;
  });

  /** The launch tab selected (by the user or because it was new). */
  const launchTab = ref<string | null>(null);

  function select(id: string) {
    if (launchTabSeq(id) !== null) {
      launchTab.value = id;
      return;
    }
    launchTab.value = null;
    sessions.selectSession(id);
  }

  const activeId = computed(() => {
    const launch = launchTab.value;
    if (launch && tabs.value.some((tab) => tab.id === launch)) return launch;
    const selected = sessions.selectedSessionId;
    return tabs.value.some((tab) => tab.id === selected) ? selected : (tabs.value[0]?.id ?? null);
  });

  /** The launch a tab shows; undefined for a session tab or an ended launch. */
  function launchFor(id: string) {
    const seq = launchTabSeq(id);
    return seq === null ? undefined : sessions.launches.find((launch) => launch.seq === seq);
  }

  watch(
    () => tabs.value.map((tab) => tab.id),
    (ids, previous) => {
      const added = ids.find((id) => !(previous ?? []).includes(id));
      if (!added || !previous) return;
      reveal();
      select(added);
    },
  );

  return { tabs, activeId, select, launchFor };
}
