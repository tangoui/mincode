/**
 * Starting streaming test runs from the UI (Tests panel, editor gutter,
 * debug panel Restart): one place that turns startTestRun's outcome into
 * what the user is told.
 * - One run per provider and project: starting a new one while one runs
 *   stops the old one on the host (it ends "superseded"); the panel shows
 *   only the newest run and the user is told the previous run was stopped.
 *   While a run of the same provider and project is still launching, the
 *   new start is refused ("busy") with a hint.
 * - Restart of a test-run session (configuration id `test-run:…`, never
 *   saved) runs the same tests again, with the same debug flag, in the same
 *   debug panel tab.
 */
import { ElMessage } from "element-plus";
import type { TestItem } from "@/domain/plugins/test-providers";
import { pickTestItems } from "@/domain/testing/test-discovery";
import { useVueI18n } from "@/i18n/messages";
import { startTestRun, type TestRunStartResult } from "@/services/plugin-test-runs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import { useTestDiscovery } from "@/stores/test-discovery";
import { useTestRuns } from "@/stores/test-runs";

export const TEST_RUN_CONFIG_PREFIX = "test-run:";

export interface TestRunLaunch {
  pluginId: string;
  providerId: string;
  /** The provider's debugRun.debuggerType. */
  debuggerType: string;
  projectRoot: string;
  /** [] runs every test. */
  testIds: string[];
  debug: boolean;
  /** The session's name in the debug panel. */
  name: string;
  /** The discovered subtrees being run (seed the results tree). */
  discovered?: readonly TestItem[];
}

const { t } = useVueI18n();
const registered = new WeakSet<object>();

/** Registers Restart of test-run sessions with the debug sessions store (once per store). */
export function ensureTestRunRestart() {
  const sessions = useDebugSessionsStore();
  if (registered.has(sessions)) return;
  registered.add(sessions);
  sessions.registerRestartHandler(TEST_RUN_CONFIG_PREFIX, async (session) => {
    const run = useTestRuns().runForSession(session.id);
    if (!run) {
      ElMessage.warning({ message: t("pluginContrib.testProviders.restartUnavailable", { name: session.configName }), showClose: true });
      return null;
    }
    const tests = useTestDiscovery().entry({ pluginId: run.pluginId, id: run.providerId }, run.projectRoot)?.tests;
    const result = await launchTestRun(
      {
        pluginId: run.pluginId,
        providerId: run.providerId,
        debuggerType: session.type,
        projectRoot: run.projectRoot,
        testIds: [...run.testIds],
        debug: run.debug,
        name: session.configName,
        ...(tests ? { discovered: pickTestItems(tests, run.testIds) } : {}),
      },
      { replaceSessionId: session.id },
    );
    return result?.status === "started" ? result.sessionId : null;
  });
}

/** Starts a test run and reports busy / superseded / failure; undefined when it threw. */
export async function launchTestRun(
  launch: TestRunLaunch,
  options: { replaceSessionId?: string } = {},
): Promise<TestRunStartResult | undefined> {
  ensureTestRunRestart();
  const previous = useTestRuns().currentRun(launch.pluginId, launch.providerId, launch.projectRoot);
  const superseding = !options.replaceSessionId && previous !== undefined && previous.phase !== "ended" && !previous.stopping;
  let result: TestRunStartResult;
  try {
    result = await startTestRun({
      ...launch,
      testIds: [...launch.testIds],
      ...(options.replaceSessionId ? { replaceSessionId: options.replaceSessionId } : {}),
    });
  } catch (error) {
    ElMessage.warning({
      message: t("pluginContrib.testProviders.runFailed", { error: error instanceof Error ? error.message : String(error) }),
      showClose: true,
      grouping: true,
    });
    return undefined;
  }
  if (result.status === "busy") {
    ElMessage.info({ message: t("pluginContrib.testProviders.busy"), grouping: true });
  } else if (result.status === "started" && superseding) {
    ElMessage.info({ message: t("pluginContrib.testProviders.superseded"), grouping: true });
  }
  return result;
}
