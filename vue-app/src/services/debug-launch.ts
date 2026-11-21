/**
 * Starting a run configuration: the guidance guard (debugger installed, its
 * tools ready) → for a debugger running inside jdtls (adapter kind "jdtls"),
 * its plugin's bundle loaded into the built-in Java language service
 * (services/jdtls-bundles.ts, loaded on demand) → prepare → confirm (first
 * run, or after the resolved launch changed) → trust → the debugger's
 * settings (a debugger with a settingsAction gets the project's exception
 * type list; a failure is a warning, the launch goes on) → start with the
 * project's enabled breakpoints (files, and debugger addresses of
 * debugger-provided sources), the debugger's exception filters, its
 * enabled method breakpoints and its saved field breakpoints. Every user-initiated launch (toolbar,
 * entry-point menu, keybindings, Restart, test runs) goes through here; a
 * test run only replaces the prepare step (`DebugLaunchOptions.prepare`).
 *
 * The steps are reported through `onProgress` (domain/debug/launch-progress.ts)
 * for the debug panel. They run one after another: the settings sync is not
 * overlapped with prepare because both are actions of the same plugin
 * process, and a plugin that runs one action at a time (the SDK default)
 * would answer the second one busy; it also must follow the confirmation.
 * Only reading the saved configurations overlaps prepare.
 */
import { h } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import DebugLaunchConfirmContent from "@/components/debug/DebugLaunchConfirmContent.vue";
import { DEBUGGER_ADDRESS_LIMITS, isValidDebuggerAddress } from "@/domain/debug/address-breakpoints";
import { configurationPayload } from "@/domain/debug/configurations";
import { sessionDataBreakpointList, type StoredDataBreakpoint } from "@/domain/debug/data-breakpoints";
import { enabledFunctionBreakpointNames } from "@/domain/debug/function-breakpoints";
import { toSourcePath } from "@/domain/debug/paths";
import { LAUNCH_STATUS_INTERVAL_MS, throttleLatest, type DebugLaunchProgress } from "@/domain/debug/launch-progress";
import { debuggeeNotices } from "@/domain/debug/session-model";
import type { DebugStartResult, DebuggerHotCodeReplace, DebuggerInfo, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { guardDebugLaunch } from "@/services/debug-guidance";
import {
  applyDebuggerSettings,
  prepareDebugger,
  startDebugger,
  trustDebugger,
  type OutgoingSourceBreakpoint,
} from "@/services/debug-service";
import { holdLspServer } from "@/services/lsp-server-holds";
import { useDebugConfigsStore } from "@/stores/debug-configs";

/**
 * The started session, and the breakpoint revision its start request carried.
 * `release` (jdtls debuggers) releases the hold that keeps the Java language
 * service from being idle-stopped; the caller calls it when the session ends.
 */
export type DebugLaunchResult = DebugStartResult & {
  breakpointRevision: number;
  /** The method breakpoint names the start request carried, in order (the host sends them only with supportsFunctionBreakpoints). */
  functionBreakpointNames: string[];
  /** The saved field breakpoints the start request carried, in order (sent only with supportsDataBreakpoints). */
  dataBreakpoints: StoredDataBreakpoint[];
  /** The exception filters the start request carried. */
  exceptionFilters: string[];
  /** The debugger's hot code replace declaration (from the debugger listing the launch was checked against). */
  hotCodeReplace?: DebuggerHotCodeReplace;
  /** Whether that listing was available; false when it failed (the declaration is unknown, not absent). */
  debuggerListed: boolean;
  release?: () => void;
};

export interface DebugLaunchOptions {
  noDebug: boolean;
  /** Absolute path of the active editor file, for `${file}`. */
  activeFile?: string;
  /**
   * Cancels the launch: the wait for the Java language service (jdtls
   * debuggers) at once, otherwise before the next step; never once the start
   * request was sent.
   */
  signal?: AbortSignal;
  /**
   * The launch's steps and the language service's status texts (at most
   * every LAUNCH_STATUS_INTERVAL_MS, the last one always), for the debug panel.
   */
  onProgress?: (progress: DebugLaunchProgress) => void;
  /**
   * The debug panel shows this launch with its Cancel: the wait for the Java
   * language service then leaves cancelling to it (its waiting message has
   * no Cancel of its own).
   */
  cancelInPanel?: () => boolean;
  /**
   * Replaces the prepare step (`plugin_host_debugger_prepare` with the run
   * configuration): a test run (services/plugin-test-runs.ts) prepares through
   * `plugin_host_test_run_prepare` instead and then goes on through the same
   * confirmation, trust and start.
   */
  prepare?: () => Promise<PreparedDebugLaunch>;
}

/**
 * Starts cfg in root. Resolves null when the guard stopped it (debugger
 * plugin or tools missing; the user was guided), the Java language service
 * could not load the debugger (the user was told, or cancelled) or the user
 * declined a confirmation; rejects when a step fails (Error("debug.config.noActiveFile")
 * when the configuration needs an active file and there is none).
 */
export async function launchDebugSession(root: string, cfg: RunConfiguration, opts: DebugLaunchOptions): Promise<DebugLaunchResult | null> {
  const configuration = configurationPayload(cfg, { projectRoot: root, file: opts.activeFile });
  opts.onProgress?.({ step: "checking" });
  const guard = await guardDebugLaunch(root, cfg, opts.activeFile, (found) => opts.onProgress?.({ languageService: found.adapterKind === "jdtls" }));
  if (!guard.proceed || opts.signal?.aborted) return null;
  if (guard.debugger?.adapterKind !== "jdtls") return prepareAndStart(root, cfg, configuration, opts, guard.debugger);
  // From the bundle flow until the session ends, jdtls hosts the debugger:
  // never idle- or background-stopped. The launch hold becomes a session hold
  // once the session runs (restarting jdtls then needs the user's consent).
  const releaseLaunch = holdLspServer(root, "jdtls", "launch");
  try {
    opts.onProgress?.({ step: "languageService" });
    const { ensureJdtlsBundlesReady } = await import("@/services/jdtls-bundles");
    const onProgress = opts.onProgress;
    const statuses = onProgress ? throttleLatest((status: string) => onProgress({ status }), LAUNCH_STATUS_INTERVAL_MS) : undefined;
    let ready: boolean;
    try {
      ready = await ensureJdtlsBundlesReady(root, cfg.pluginId, opts.signal ?? new AbortController().signal, {
        ...(onProgress && statuses ? { onStatus: statuses.push, onConfirm: (open: boolean) => onProgress({ confirming: open }) } : {}),
        ...(opts.cancelInPanel ? { cancelInPanel: opts.cancelInPanel } : {}),
      });
    } finally {
      // The latest text is shown, not dropped, when the wait ends within the interval.
      statuses?.flush();
    }
    const result = ready && !opts.signal?.aborted ? await prepareAndStart(root, cfg, configuration, opts, guard.debugger) : null;
    return result && { ...result, release: holdLspServer(root, "jdtls", "debug-session") };
  } finally {
    releaseLaunch();
  }
}

/** Settings syncs run one after another, so an older list never lands after a newer one. */
let settingsQueue: Promise<unknown> = Promise.resolve();

/**
 * Sends the project's saved settings of the debugger to its settingsAction
 * (always an explicit list: [] clears the previous one), read when the sync
 * runs. A failure is shown as a warning and resolves false; the list stays
 * saved and the next launch or list change syncs again.
 */
export function syncDebuggerSettings(root: string, debugger_: Pick<DebuggerInfo, "pluginId" | "type">): Promise<boolean> {
  const run = settingsQueue.then(async () => {
    const configs = useDebugConfigsStore();
    try {
      await configs.load();
      const exceptionTypes = [...(configs.stateFor(root).exceptionTypes[debugger_.type] ?? [])];
      await applyDebuggerSettings({ pluginId: debugger_.pluginId, type: debugger_.type, projectRoot: root, exceptionTypes });
      return true;
    } catch (error) {
      const { t } = useVueI18n();
      const reason = error instanceof Error ? error.message : String(error);
      ElMessage.warning({ message: t("debug.settings.syncFailed", { error: reason }), showClose: true, grouping: true });
      return false;
    }
  });
  settingsQueue = run;
  return run;
}

async function prepareAndStart(
  root: string,
  cfg: RunConfiguration,
  configuration: Record<string, unknown>,
  opts: DebugLaunchOptions,
  debugger_: DebuggerInfo | undefined,
): Promise<DebugLaunchResult | null> {
  const configs = useDebugConfigsStore();
  opts.onProgress?.({ step: "resolving" });
  // Independent of prepare (local persisted state); awaited after the confirmation.
  const configsLoaded = configs.load();
  configsLoaded.catch(() => undefined);
  let prepared = opts.prepare
    ? await opts.prepare()
    : await prepareDebugger({ pluginId: cfg.pluginId, type: cfg.type, projectRoot: root, configuration, noDebug: opts.noDebug });
  if (opts.signal?.aborted) return null;
  if (!prepared.launch.trusted) {
    const { t } = useVueI18n();
    try {
      await ElMessageBox.confirm(
        h(DebugLaunchConfirmContent, {
          launch: prepared.launch,
          changed: prepared.launch.previouslyTrusted,
          notices: debuggeeNotices(prepared, t("debug.launch.fullArguments")),
        }),
        t("pluginContrib.processLaunch.confirmTitle"),
        {
          type: "warning",
          confirmButtonText: t("pluginContrib.processLaunch.confirmRun"),
          cancelButtonText: t("common.cancel"),
          distinguishCancelAndClose: true,
          autofocus: false,
        },
      );
    } catch {
      return null;
    }
    if (opts.signal?.aborted) return null;
    prepared = await trustDebugger(prepared.prepareId, prepared.launch.fingerprint);
  }

  // Read after the confirmation: the user may have changed breakpoints meanwhile.
  await configsLoaded;
  const state = configs.stateFor(root);
  // Changes after this snapshot are re-sent once the session is listed (see the sessions store).
  const breakpointRevision = configs.currentBreakpointRevision();
  const breakpoints: Record<string, OutgoingSourceBreakpoint[]> = {};
  const addressBreakpoints: Record<string, OutgoingSourceBreakpoint[]> = {};
  if (!opts.noDebug) {
    for (const [key, list] of Object.entries(state.breakpoints)) {
      const enabled = list.filter((bp) => bp.enabled).map((bp) => ({ ...bp }));
      if (enabled.length) breakpoints[toSourcePath(root, key)] = enabled;
    }
    // Only this debugger's addresses (another debugger cannot read them). The
    // host refuses the whole start for one bad address: only valid ones, at
    // most its limit (the store keeps no others; checked again here).
    for (const [address, list] of Object.entries(state.addressBreakpoints[cfg.type] ?? {})) {
      const enabled = list.filter((bp) => bp.enabled).map((bp) => ({ ...bp }));
      if (!enabled.length || !isValidDebuggerAddress(address)) continue;
      if (Object.keys(addressBreakpoints).length >= DEBUGGER_ADDRESS_LIMITS.maxAddresses) break;
      addressBreakpoints[address] = enabled;
    }
  }
  const functionBreakpointNames = opts.noDebug ? [] : enabledFunctionBreakpointNames(state.functionBreakpoints[cfg.type]);
  const exceptionFilters = opts.noDebug ? [] : [...(state.exceptionFilters[cfg.type] ?? [])];
  // Only the saved (persistable) ones: a session's own field breakpoints end with it.
  const dataBreakpoints = opts.noDebug ? [] : sessionDataBreakpointList(state.dataBreakpoints[cfg.type], []).map((bp) => ({ ...bp }));
  // Right before start (after the confirmation, so a list edited meanwhile
  // counts), so the session's first setExceptionBreakpoints uses them. Debug
  // runs only: a run without debugging never pauses.
  if (!opts.noDebug && debugger_?.hasSettings) await syncDebuggerSettings(root, debugger_);
  if (opts.signal?.aborted) return null;
  opts.onProgress?.({ step: "launching" });
  const result = await startDebugger({
    prepareId: prepared.prepareId,
    fingerprint: prepared.launch.fingerprint,
    breakpoints,
    ...(Object.keys(addressBreakpoints).length ? { addressBreakpoints } : {}),
    exceptionFilters: [...exceptionFilters],
    ...(functionBreakpointNames.length ? { functionBreakpoints: functionBreakpointNames.map((name) => ({ name })) } : {}),
    ...(dataBreakpoints.length ? { dataBreakpoints: dataBreakpoints.map(({ dataId, accessType }) => ({ dataId, accessType })) } : {}),
  });
  const hotCodeReplace = debugger_?.hotCodeReplace;
  return {
    ...result,
    breakpointRevision,
    functionBreakpointNames,
    dataBreakpoints,
    exceptionFilters,
    debuggerListed: debugger_ !== undefined,
    ...(hotCodeReplace ? { hotCodeReplace: { ...hotCodeReplace } } : {}),
  };
}
