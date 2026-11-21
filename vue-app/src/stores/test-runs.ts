/**
 * Streaming test runs (testProviders debugRun): the state of each run by
 * runId, merged from the host's `plugin:test-run-event` payloads
 * (domain/testing/test-runs.ts). Transient: never persisted, the newest
 * TEST_RUNS_KEPT runs are kept (ended ones are evicted first). Events of a
 * run that is not created yet (they can arrive before its prepare call
 * returns the runId) are buffered briefly. Starting, stopping and the event
 * subscription live in services/plugin-test-runs.ts.
 *
 * Run states are raw objects (markRaw, in a shallowRef): a parameterized run
 * can add thousands of nodes, and deep reactivity made each merge cost a
 * proxy per node. Every mutation bumps `revision` (and the run's
 * `revisions[runId]`). runFor/currentRun depend on those counters and return
 * a read-only snapshot: a shallow copy of the run made once per revision, so
 * a computed over them yields a new object after every merged payload and its
 * readers (nodes, phase, nodeCount, stopping, ...) re-render. The snapshot
 * shares the raw node objects; never mutate it. `runs` itself is a
 * shallowRef of raw states: reading `runs[id].…` directly is not reactive.
 *
 * Derive everything from the snapshot in the same computed (or read it
 * again where it is used). Do not keep `snapshot.nodes` in a separate
 * computed chained on it: `nodes` is the same raw object in every
 * snapshot, so a computed returning it never changes and its readers miss
 * every later merge.
 */
import { markRaw, ref, shallowRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { TestItem } from "@/domain/plugins/test-providers";
import {
  aggregateTestRunStatuses,
  applyTestRunEvents,
  createTestRunState,
  endTestRun,
  failedTestIds as runFailedTestIds,
  seedTestRunNodes,
  summarizeTestRun,
  testNodeDisplayStatus,
  type TestNodeStatus,
  type TestRunEvent,
  type TestRunEventPayload,
  type TestRunInit,
  type TestRunState,
  type TestRunSummary,
} from "@/domain/testing/test-runs";

/** Runs kept in memory. */
export const TEST_RUNS_KEPT = 16;
/** Runs whose events wait for their run to be created, and events per run. */
const BUFFERED_RUNS_MAX = 8;
const BUFFERED_EVENTS_MAX = 5000;

/** The key of one provider in one project (one run at a time). */
export function testRunProviderKey(pluginId: string, providerId: string, projectRoot: string): string {
  return `${pluginId}/${providerId}@${projectRoot}`;
}

export const useTestRuns = defineStore("test-runs", () => {
  const runs = shallowRef<Record<string, TestRunState>>({});
  /** Bumped on every change of any run. */
  const revision = ref(0);
  /** Bumped on every change of one run. */
  const revisions = ref<Record<string, number>>({});

  function touch(runId: string) {
    revision.value += 1;
    revisions.value = { ...revisions.value, [runId]: (revisions.value[runId] ?? 0) + 1 };
  }
  /** runIds, oldest first. */
  const order = ref<string[]>([]);
  /** Provider keys with a launch in flight (before the session exists). */
  const launching = ref<Set<string>>(new Set());
  const buffered = new Map<string, { pluginId: string; events: TestRunEvent[] }>();
  /** The snapshot of each kept run at its current revision (removed with the run). */
  const snapshots = new Map<string, { revision: number; view: Readonly<TestRunState> }>();

  function evict() {
    while (order.value.length > TEST_RUNS_KEPT) {
      const victim = order.value.find((id) => runs.value[id]?.phase === "ended") ?? order.value[0]!;
      order.value = order.value.filter((id) => id !== victim);
      const next = { ...runs.value };
      delete next[victim];
      runs.value = next;
      const nextRevisions = { ...revisions.value };
      delete nextRevisions[victim];
      revisions.value = nextRevisions;
      snapshots.delete(victim);
    }
  }

  /** Creates a run (the prepare call returned its id) and replays its buffered events. */
  function createRun(init: TestRunInit, discovered?: readonly TestItem[]): TestRunState {
    const existing = runs.value[init.runId];
    if (existing) return existing;
    const run = markRaw(createTestRunState(init));
    if (discovered?.length) seedTestRunNodes(run, discovered);
    const pending = buffered.get(init.runId);
    buffered.delete(init.runId);
    if (pending && pending.pluginId === init.pluginId) applyTestRunEvents(run, pending.events);
    runs.value = { ...runs.value, [init.runId]: run };
    order.value = [...order.value, init.runId];
    touch(init.runId);
    evict();
    return run;
  }

  /** Merges a validated payload; events of an unknown run are buffered. */
  function applyPayload(payload: TestRunEventPayload) {
    const run = runs.value[payload.runId];
    if (!run) {
      const pending = buffered.get(payload.runId) ?? { pluginId: payload.pluginId, events: [] };
      if (!buffered.has(payload.runId)) {
        if (buffered.size >= BUFFERED_RUNS_MAX) buffered.delete(buffered.keys().next().value!);
        buffered.set(payload.runId, pending);
      }
      pending.events.push(...payload.events.slice(0, Math.max(0, BUFFERED_EVENTS_MAX - pending.events.length)));
      return;
    }
    // A payload naming another plugin than the run's is ignored (the host never sends one).
    if (run.pluginId !== payload.pluginId) return;
    run.dropped += payload.dropped;
    applyTestRunEvents(run, payload.events);
    touch(run.runId);
  }

  function bindSession(runId: string, sessionId: string) {
    const run = runs.value[runId];
    if (!run) return;
    run.sessionId = sessionId;
    if (run.phase === "preparing") run.phase = "running";
    touch(runId);
  }

  /** Ends a run on the frontend side (its launch was declined or failed); unfinished tests become terminated. */
  function endRun(runId: string, reason: string) {
    const run = runs.value[runId];
    if (!run) return;
    endTestRun(run, reason);
    touch(runId);
  }

  /** The user stopped the run: shown as stopping until the host's ended event arrives. */
  function markStopping(runId: string) {
    const run = runs.value[runId];
    if (!run || run.phase === "ended" || run.stopping) return;
    run.stopping = true;
    touch(runId);
  }

  /** A read-only snapshot of the run, a new object per revision of that run. */
  function snapshot(run: TestRunState): Readonly<TestRunState> {
    const revisionOfRun = revisions.value[run.runId] ?? 0;
    const cached = snapshots.get(run.runId);
    if (cached && cached.revision === revisionOfRun && snapshots.size <= TEST_RUNS_KEPT * 2) return cached.view;
    if (snapshots.size > TEST_RUNS_KEPT * 2) snapshots.clear();
    const view = Object.freeze({ ...run });
    snapshots.set(run.runId, { revision: revisionOfRun, view });
    return view;
  }

  /** The stop request failed: the run is shown as running again. */
  function clearStopping(runId: string) {
    const run = runs.value[runId];
    if (!run || !run.stopping) return;
    run.stopping = false;
    touch(runId);
  }

  /** The run as a read-only snapshot (reactive: a new object after each change of the run). */
  function runFor(runId: string): Readonly<TestRunState> | undefined {
    void revisions.value[runId];
    const run = runs.value[runId];
    return run ? snapshot(run) : undefined;
  }

  /**
   * The run to show for a provider in a project: the newest one started.
   * Late events of an older (superseded or stopped) run change only that
   * run, never what this returns.
   */
  function currentRun(pluginId: string, providerId: string, projectRoot: string): Readonly<TestRunState> | undefined {
    void revision.value;
    const run = latestRun(pluginId, providerId, projectRoot);
    return run ? runFor(run.runId) : undefined;
  }

  /** The newest run of a provider in a project (not reactive to run contents; see currentRun). */
  function latestRun(pluginId: string, providerId: string, projectRoot: string): TestRunState | undefined {
    for (let index = order.value.length - 1; index >= 0; index -= 1) {
      const run = runs.value[order.value[index]!];
      if (run && run.pluginId === pluginId && run.providerId === providerId && run.projectRoot === projectRoot) return run;
    }
    return undefined;
  }

  /** The run bound to a debug session, as a snapshot (Restart of a test-run session). */
  function runForSession(sessionId: string): Readonly<TestRunState> | undefined {
    void revision.value;
    for (let index = order.value.length - 1; index >= 0; index -= 1) {
      const run = runs.value[order.value[index]!];
      if (run?.sessionId === sessionId) return runFor(run.runId);
    }
    return undefined;
  }

  /**
   * The last displayed status of each id among a provider's kept runs in a
   * project (the gutter's last-result icons): from the newest run that has
   * the node, with a node still pending in an ended run skipped for an older
   * result. Ids without any result are absent. Cost: ids × kept runs (a
   * suite walks only its own subtree).
   */
  function lastStatuses(pluginId: string, providerId: string, projectRoot: string, ids: readonly string[]): Map<string, TestNodeStatus> {
    void revision.value;
    const out = new Map<string, TestNodeStatus>();
    const pending = new Set(ids);
    for (let index = order.value.length - 1; index >= 0 && pending.size; index -= 1) {
      const run = runs.value[order.value[index]!];
      if (!run || run.pluginId !== pluginId || run.providerId !== providerId || run.projectRoot !== projectRoot) continue;
      for (const id of [...pending]) {
        const status = testNodeDisplayStatus(run, id);
        if (status === undefined || (status === "pending" && run.phase === "ended")) continue;
        out.set(id, status);
        pending.delete(id);
      }
    }
    return out;
  }

  /** The ids "Rerun failed" runs for this run ([] when none failed). */
  function failedTestIds(runId: string): string[] {
    const run = runFor(runId);
    return run ? runFailedTestIds(run) : [];
  }

  function summary(runId: string): TestRunSummary | undefined {
    const run = runFor(runId);
    return run ? summarizeTestRun(run) : undefined;
  }

  function statuses(runId: string): Map<string, TestNodeStatus> {
    const run = runFor(runId);
    return run ? aggregateTestRunStatuses(run) : new Map();
  }

  function isLaunching(key: string) {
    return launching.value.has(key);
  }

  function setLaunching(key: string, active: boolean) {
    const next = new Set(launching.value);
    if (active) next.add(key);
    else next.delete(key);
    launching.value = next;
  }

  return {
    runs,
    order,
    revision,
    revisions,
    createRun,
    applyPayload,
    bindSession,
    endRun,
    markStopping,
    clearStopping,
    runFor,
    currentRun,
    latestRun,
    runForSession,
    lastStatuses,
    failedTestIds,
    summary,
    statuses,
    isLaunching,
    setLaunching,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTestRuns, import.meta.hot));
}
