/**
 * Streaming test runs (testProviders debugRun, host 0.3.0). A run is a debug
 * session of the provider plugin's debugger, started through the one launch
 * path (services/debug-launch.ts via the debug sessions store: guard, Java
 * language service, confirmation, trust, settings, breakpoints, start) with
 * only the prepare step replaced by `plugin_host_test_run_prepare`, which
 * calls the provider's resolveRunAction for exactly these tests and returns
 * the prepared launch plus a runId. The host binds the session to the run;
 * results arrive as `plugin:test-run-event` and are merged by the test runs
 * store. Test runs are never saved as run configurations, and every run
 * resolves its launch again.
 *
 * One run per provider and project: while one is launching a new start
 * answers "busy"; once it runs, a new start stops it on the host (it ends
 * with reason "superseded").
 */
import type { PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";
import { TEST_PROVIDER_LIMITS, type TestItem } from "@/domain/plugins/test-providers";
import { parseTestRunEventPayload, type TestRunEventPayload } from "@/domain/testing/test-runs";
import { parsePrepared } from "@/services/debug-service";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import { testRunProviderKey, useTestRuns } from "@/stores/test-runs";

export const TEST_RUN_EVENT = "plugin:test-run-event";

/** A test run as the host prepares it. */
export interface TestRunPrepareRequest {
  pluginId: string;
  providerId: string;
  projectRoot: string;
  /** Test ids to run ([] = all tests), at most runTestsMax. */
  testIds: string[];
  /** Debug (true) or run without debugging. */
  debug: boolean;
}

export type PreparedTestRun = PreparedDebugLaunch & { runId: string };

export interface TestRunRequest extends TestRunPrepareRequest {
  /** The provider's debugRun.debuggerType (the debugger whose session runs the tests). */
  debuggerType: string;
  /** The session's name in the debug panel (for example the test class or method). */
  name: string;
  /** The discovered (sub)tree being run: its nodes start as pending. */
  discovered?: readonly TestItem[];
  /** Restart: the stopped session whose debug panel tab the new session takes over. */
  replaceSessionId?: string;
}

export type TestRunStartResult =
  | { status: "started"; runId: string; sessionId: string }
  /** The user declined a confirmation or the launch failed (already reported); runId when the host had prepared one. */
  | { status: "notStarted"; runId?: string }
  /** A run of this provider and project is still launching. */
  | { status: "busy" };

const RUN_ID_PATTERN = /^run-[0-9a-f]{1,60}$/;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: test runs need the MinCode desktop app");
}

/** Prepares a test run on the host (resolveRunAction; nothing is started). */
export async function prepareTestRun(request: TestRunPrepareRequest): Promise<PreparedTestRun> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_test_run_prepare", {
    payload: { ...request, testIds: request.testIds.slice(0, TEST_PROVIDER_LIMITS.runTestsMax) },
  });
  const prepared = parsePrepared(raw, "plugin_host_test_run_prepare");
  const runId = (raw as { runId?: unknown }).runId;
  if (typeof runId !== "string" || !RUN_ID_PATTERN.test(runId)) throw new Error("plugin_host_test_run_prepare returned an invalid run id");
  return { ...prepared, runId };
}

/**
 * Stops a run: its session is stopped (the run then ends with the session,
 * after the host's grace period for late results), a run still launching is
 * closed at once. Resolves false when the run had already ended.
 */
export async function stopTestRun(runId: string): Promise<boolean> {
  requireDesktop();
  const runs = useTestRuns();
  runs.markStopping(runId);
  let raw: unknown;
  try {
    raw = await invokeBackend<unknown>("plugin_host_test_run_stop", { payload: { runId } });
  } catch (error) {
    runs.clearStopping(runId);
    throw error;
  }
  return typeof raw === "object" && raw !== null && (raw as { stopped?: unknown }).stopped === true;
}

/** Subscribes to validated test run payloads. */
export function onTestRunEvent(handler: (payload: TestRunEventPayload) => void): () => void {
  return listenAppEvent<unknown>(TEST_RUN_EVENT, (raw) => {
    const payload = parseTestRunEventPayload(raw);
    if (payload) handler(payload);
  });
}

let unlistenRuns: (() => void) | null = null;

/** Feeds every test run payload into the test runs store (idempotent). */
export function ensureTestRunEvents() {
  if (unlistenRuns) return;
  const store = useTestRuns();
  unlistenRuns = onTestRunEvent((payload) => store.applyPayload(payload));
}

/** Stops feeding the store (tests). */
export function resetTestRunEvents() {
  unlistenRuns?.();
  unlistenRuns = null;
}

/** The transient run configuration of a provider's test runs (never saved). */
export function testRunConfiguration(request: Pick<TestRunRequest, "pluginId" | "providerId" | "debuggerType" | "name">): RunConfiguration {
  return {
    id: `test-run:${request.pluginId}/${request.providerId}`,
    name: request.name,
    pluginId: request.pluginId,
    type: request.debuggerType,
    request: "launch",
    fields: {},
  };
}

/** Starts a test run as a debug session (see the module comment). */
export async function startTestRun(request: TestRunRequest): Promise<TestRunStartResult> {
  ensureTestRunEvents();
  const runs = useTestRuns();
  const key = testRunProviderKey(request.pluginId, request.providerId, request.projectRoot);
  if (runs.isLaunching(key)) return { status: "busy" };
  runs.setLaunching(key, true);
  let runId: string | undefined;
  try {
    const prepareRequest: TestRunPrepareRequest = {
      pluginId: request.pluginId,
      providerId: request.providerId,
      projectRoot: request.projectRoot,
      testIds: [...request.testIds],
      debug: request.debug,
    };
    const sessionId = await useDebugSessionsStore().start(request.projectRoot, testRunConfiguration(request), {
      noDebug: !request.debug,
      prepare: async () => {
        const prepared = await prepareTestRun(prepareRequest);
        runId = prepared.runId;
        runs.createRun({ ...prepareRequest, runId: prepared.runId, startedAt: Date.now() }, request.discovered);
        return prepared;
      },
    }, request.replaceSessionId);
    if (!sessionId || !runId) {
      if (runId) {
        runs.endRun(runId, "cancelled");
        void stopTestRun(runId).catch(() => undefined);
      }
      return runId ? { status: "notStarted", runId } : { status: "notStarted" };
    }
    runs.bindSession(runId, sessionId);
    return { status: "started", runId, sessionId };
  } finally {
    runs.setLaunching(key, false);
  }
}
