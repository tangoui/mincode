/**
 * Loading a plugin's jdtls bundles (contributes.jdtlsBundles, e.g. java-debug)
 * into the built-in Java language service on demand, before a debugger whose
 * adapter runs inside jdtls (adapter kind "jdtls") is prepared.
 *
 * The host loads only jars the user confirmed by fingerprint
 * (backend/plugin/jdtls_bundles_trust.go) and only when jdtls
 * initializes; it never restarts jdtls itself but emits
 * `project-lsp:restart-required`. This module asks for the confirmation,
 * restarts (or starts) jdtls through the project's language servers, and
 * waits, cancellably, until the running jdtls is ready with the jars loaded.
 * The wait re-reads the host status when jdtls reports ServiceReady (its
 * `language/status`, forwarded as a project LSP event) and otherwise polls;
 * the status texts are passed on for the debug panel's launch progress.
 * Loaded on demand (debug-launch, LSP runtime): not part of the entry bundle.
 */
import { h, watch } from "vue";
import { ElButton, ElMessage, ElMessageBox, ElNotification } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { languageStatusParams } from "@/domain/debug/launch-progress";
import { beginBuiltinServerLaunch, builtinServerLaunchInFlight, isLspServerHeld, lspRootKey } from "@/services/lsp-server-holds";
import {
  onLspEvent,
  projectLspHostFor,
  type BuiltinServerAvailability,
  type LspRestartRequiredEvent,
  type ProjectLspHost,
} from "@/services/lsp-service";
import { invokeBackend } from "@/services/desktop-bridge";

export const JDTLS_SERVER_ID = "jdtls";
/** How long a launch waits for jdtls to be ready with the bundle loaded. */
export const JDTLS_READY_TIMEOUT_MS = 180_000;
/** Fallback poll while waiting; jdtls's ServiceReady status wakes the wait at once. */
export const JDTLS_POLL_INTERVAL_MS = 500;
/** The jdtls `language/status` type after which it serves the imported project. */
const SERVICE_READY = "ServiceReady";
const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;

/** JSON shape of the Go JdtlsBundleJar. */
export interface JdtlsBundleJar {
  name: string;
  sha256: string;
  bytes: number;
  commands?: string[];
  /** Package-relative path of the entry's jar when this is one of its dependency jars (no commands). */
  dependencyOf?: string;
}

/** JSON shape of the Go JdtlsBundleStatus. */
export interface JdtlsBundleStatus {
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  jars: JdtlsBundleJar[];
  fingerprint: string;
  trusted: boolean;
  previouslyTrusted: boolean;
  loaded: boolean;
  error?: string;
}

/** JSON shape of the Go JdtlsBundlesStatus. */
export interface JdtlsBundlesStatus {
  bundles: JdtlsBundleStatus[];
  restartRequired: boolean;
  jdtls: { running: boolean; ready: boolean };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function invalid(operation: string): never {
  throw new Error(`unavailable: ${operation} returned an invalid jdtls bundle status`);
}

function parseBundle(raw: unknown, operation: string): JdtlsBundleStatus {
  if (!isRecord(raw) || typeof raw.pluginId !== "string" || !raw.pluginId || typeof raw.fingerprint !== "string") invalid(operation);
  const jars = Array.isArray(raw.jars)
    ? raw.jars.filter(
        (jar): jar is JdtlsBundleJar =>
          isRecord(jar) && typeof jar.name === "string" && typeof jar.sha256 === "string" && typeof jar.bytes === "number",
      )
    : [];
  return {
    pluginId: raw.pluginId,
    pluginName: typeof raw.pluginName === "string" ? raw.pluginName : "",
    pluginVersion: typeof raw.pluginVersion === "string" ? raw.pluginVersion : "",
    jars: jars.map(({ name, sha256, bytes, commands, dependencyOf }) => ({
      name,
      sha256,
      bytes,
      commands: Array.isArray(commands) ? commands.filter((command): command is string => typeof command === "string") : [],
      ...(typeof dependencyOf === "string" && dependencyOf ? { dependencyOf } : {}),
    })),
    fingerprint: raw.fingerprint,
    trusted: raw.trusted === true,
    previouslyTrusted: raw.previouslyTrusted === true,
    loaded: raw.loaded === true,
    ...(typeof raw.error === "string" && raw.error ? { error: raw.error } : {}),
  };
}

/** The plugins' jdtls bundles and the project's jdtls state. */
export async function jdtlsBundlesStatus(projectRoot: string): Promise<JdtlsBundlesStatus> {
  const operation = "plugin_host_jdtls_bundles_status";
  const raw = await invokeBackend<unknown>(operation, { payload: { projectRoot } });
  if (!isRecord(raw) || !Array.isArray(raw.bundles) || !isRecord(raw.jdtls)) invalid(operation);
  return {
    bundles: raw.bundles.map((bundle) => parseBundle(bundle, operation)),
    restartRequired: raw.restartRequired === true,
    jdtls: { running: raw.jdtls.running === true, ready: raw.jdtls.ready === true },
  };
}

/** Stores the user's confirmation of exactly fingerprint (`changed:` when the jars changed meanwhile). */
export async function trustJdtlsBundles(pluginId: string, fingerprint: string): Promise<JdtlsBundleStatus> {
  const operation = "plugin_host_jdtls_bundles_trust";
  return parseBundle(await invokeBackend<unknown>(operation, { payload: { pluginId, fingerprint } }), operation);
}

/** Asks to load bundle into the Java language service; true only when the user chose Load. */
async function confirmJdtlsBundle(bundle: JdtlsBundleStatus): Promise<boolean> {
  const { t } = useVueI18n();
  const { default: JdtlsBundleConfirmContent } = await import("@/components/debug/JdtlsBundleConfirmContent.vue");
  try {
    await ElMessageBox.confirm(h(JdtlsBundleConfirmContent, { bundle }), t("debug.jdtls.confirmTitle"), {
      type: "warning",
      confirmButtonText: t("debug.jdtls.confirmLoad"),
      cancelButtonText: t("common.cancel"),
      distinguishCancelAndClose: true,
      autofocus: false,
      customClass: "app-plugin-launch-confirm",
    });
    return true;
  } catch {
    return false;
  }
}

/** Options of ensureJdtlsBundlesReady. */
export interface JdtlsReadyOptions {
  /** jdtls's status texts while the launch waits for it (shown verbatim, already made safe). */
  onStatus?: (text: string) => void;
  /** A confirmation dialog opens (true) or closes (false). */
  onConfirm?: (open: boolean) => void;
  /**
   * The debug panel shows the launch with its own Cancel (read reactively):
   * meanwhile the waiting message has no Cancel and closing it only hides it;
   * a hidden message shows again, with its Cancel, once the panel stops
   * showing the launch.
   */
  cancelInPanel?: () => boolean;
}

/** Runs a confirmation dialog, reporting it open meanwhile. */
async function confirming<T>(options: JdtlsReadyOptions, dialog: () => Promise<T>): Promise<T> {
  options.onConfirm?.(true);
  try {
    return await dialog();
  } finally {
    options.onConfirm?.(false);
  }
}

/** Resolves after ms, on abort, or when wake() is called (whichever comes first). */
function sleep(ms: number, signal: AbortSignal, wake: { resolve?: () => void }): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    wake.resolve = done;
    function done() {
      clearTimeout(timer);
      wake.resolve = undefined;
      signal.removeEventListener("abort", done);
      resolve();
    }
    signal.addEventListener("abort", done, { once: true });
  });
}

/**
 * Runs before (the restart or start of jdtls), then polls until the
 * project's jdtls is ready with pluginId's bundle loaded, showing one
 * cancellable waiting message for the whole time (its Cancel left to the
 * debug panel while the panel shows the launch). False when cancelled (silently),
 * when jdtls stopped, or after JDTLS_READY_TIMEOUT_MS (both with a message).
 */
async function confirmRestartEndsSession(): Promise<boolean> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(t("debug.jdtls.restartEndsSession"), t("debug.jdtls.restartEndsSessionTitle"), {
      type: "warning",
      confirmButtonText: t("debug.jdtls.restartAndContinue"),
      cancelButtonText: t("common.cancel"),
      distinguishCancelAndClose: true,
      autofocus: false,
    });
    return true;
  } catch {
    return false;
  }
}

async function waitUntilLoaded(
  root: string,
  pluginId: string,
  signal: AbortSignal,
  /** Runs once the cancellable message shows (the restart or start); false stops the wait. */
  before: () => Promise<boolean>,
  options: JdtlsReadyOptions,
): Promise<boolean> {
  const { t } = useVueI18n();
  const cancel = new AbortController();
  const stop = () => cancel.abort();
  signal.addEventListener("abort", stop, { once: true });
  let finished = false;
  // The host marks the session ready before it forwards ServiceReady, so the
  // status read right after the event sees it.
  const wake: { resolve?: () => void } = {};
  let readySignalled = false;
  const rootKey = lspRootKey(root);
  const unlisten = onLspEvent((event) => {
    if (event.method !== "language/status" || event.serverId !== JDTLS_SERVER_ID || lspRootKey(event.root) !== rootKey) return;
    const status = languageStatusParams(event.params);
    if (!status) return;
    if (status.message) options.onStatus?.(status.message);
    if (status.type === SERVICE_READY) {
      readySignalled = true;
      wake.resolve?.();
    }
  });
  const inPanel = () => options.cancelInPanel?.() === true;
  /** Closed while the panel showed the launch: it comes back once the panel stops (the one Cancel must stay reachable). */
  let dismissed = false;
  const showWaiting = () =>
    ElMessage({
      type: "info",
      message: () =>
        h("span", { class: "app-jdtls-waiting" }, [
          t("debug.jdtls.waiting"),
          ...(inPanel() ? [] : [" ", h(ElButton, { link: true, type: "primary", size: "small", onClick: stop }, () => t("common.cancel"))]),
        ]),
      duration: 0,
      showClose: true,
      onClose: () => {
        if (finished) return;
        if (inPanel()) dismissed = true;
        else stop();
      },
    });
  let waiting = showWaiting();
  const stopWatchingPanel = watch(inPanel, (shown) => {
    if (shown || !dismissed || finished || cancel.signal.aborted) return;
    dismissed = false;
    waiting = showWaiting();
  });
  const deadline = Date.now() + JDTLS_READY_TIMEOUT_MS;
  try {
    if (!(await before())) return false;
    for (;;) {
      if (cancel.signal.aborted) return false;
      const status = await jdtlsBundlesStatus(root);
      if (cancel.signal.aborted) return false;
      const bundle = status.bundles.find((item) => item.pluginId === pluginId);
      if (status.jdtls.ready && bundle?.loaded) return true;
      if (!status.jdtls.running) {
        ElMessage.error({ message: t("debug.jdtls.stopped"), showClose: true, grouping: true });
        return false;
      }
      if (Date.now() >= deadline) {
        ElMessage.error({ message: t("debug.jdtls.timeout", { seconds: JDTLS_READY_TIMEOUT_MS / 1000 }), showClose: true, grouping: true });
        return false;
      }
      if (readySignalled) {
        // Read once more right away (the event may have raced the last read).
        readySignalled = false;
        continue;
      }
      await sleep(JDTLS_POLL_INTERVAL_MS, cancel.signal, wake);
    }
  } finally {
    finished = true;
    stopWatchingPanel();
    unlisten();
    signal.removeEventListener("abort", stop);
    waiting.close();
  }
}

/**
 * Makes pluginId's jdtls bundle loaded into root's running, ready jdtls:
 * offers to enable the built-in jdtls when the project has not enabled it
 * (declining → false), refuses when Java is assigned to a plugin language
 * server there; asks the user
 * to confirm an unconfirmed bundle (declining → false); restarts jdtls when
 * it runs without the bundle, or starts it (the confirmation, or the Debug
 * action itself, is the consent: no second question); then waits until it is
 * ready with the bundle loaded. Resolves false when the launch must not go
 * on (the user was told, or cancelled); rejects when a host call fails.
 */
export async function ensureJdtlsBundlesReady(
  root: string,
  pluginId: string,
  signal: AbortSignal,
  options: JdtlsReadyOptions = {},
): Promise<boolean> {
  const { t } = useVueI18n();
  // Java launches loading a bundle restart jdtls themselves: no restart prompt meanwhile.
  const endLaunch = beginBuiltinServerLaunch(JDTLS_SERVER_ID);
  try {
    const host = projectLspHostFor(root);
    if (!host) {
      warnUnavailable("unavailable");
      return false;
    }
    if (!(await ensureJdtlsServesJava(host, signal, options))) return false;
    let status = await jdtlsBundlesStatus(root);
    let bundle = status.bundles.find((item) => item.pluginId === pluginId);
    if (!bundle || bundle.error || !bundle.jars.length || !FINGERPRINT_PATTERN.test(bundle.fingerprint)) {
      const message = bundle?.error
        ? t("debug.jdtls.bundleError", { plugin: bundle.pluginName || pluginId, error: bundle.error })
        : t("debug.jdtls.noBundle", { plugin: bundle?.pluginName || pluginId });
      ElMessage.error({ message, showClose: true, grouping: true });
      return false;
    }
    if (!bundle.trusted) {
      const shown = bundle;
      if (!(await confirming(options, () => confirmJdtlsBundle(shown))) || signal.aborted) return false;
      await trustJdtlsBundles(pluginId, bundle.fingerprint);
      status = await jdtlsBundlesStatus(root);
      bundle = status.bundles.find((item) => item.pluginId === pluginId);
      if (!bundle?.trusted) throw new Error("unavailable: the jdtls bundle confirmation was not stored");
    }
    if (status.jdtls.ready && bundle.loaded) return true;
    const restart = status.restartRequired || !status.jdtls.running || !bundle.loaded;
    // Restarting a jdtls that hosts another debug session ends that session: ask first.
    if (restart && status.jdtls.running && isLspServerHeld(root, JDTLS_SERVER_ID, "debug-session")) {
      if (!(await confirming(options, confirmRestartEndsSession)) || signal.aborted) return false;
    }
    return await waitUntilLoaded(root, pluginId, signal, async () => {
      if (!restart || (await host.restartBuiltinServer(JDTLS_SERVER_ID))) return true;
      ElMessage.error({ message: t("debug.jdtls.startFailed"), showClose: true, grouping: true });
      return false;
    }, options);
  } finally {
    endLaunch();
  }
}

function warnUnavailable(availability: Exclude<BuiltinServerAvailability, "available">) {
  const { t } = useVueI18n();
  const key = availability === "plugin" ? "debug.jdtls.pluginServer" : availability === "disabled" ? "debug.jdtls.notEnabled" : "debug.jdtls.unavailable";
  ElMessage.warning({ message: t(key), showClose: true, grouping: true });
}

/**
 * Whether the built-in jdtls serves Java in host's project. When it is not
 * enabled there, offers to enable it (in the project's language server
 * settings, as the Language Servers dialog does) and continue; other cases
 * are refused with a message.
 */
async function ensureJdtlsServesJava(host: ProjectLspHost, signal: AbortSignal, options: JdtlsReadyOptions): Promise<boolean> {
  const { t } = useVueI18n();
  let availability = await host.builtinServerAvailability(JDTLS_SERVER_ID, "java");
  if (availability === "disabled") {
    try {
      await confirming(options, () =>
        ElMessageBox.confirm(t("debug.jdtls.disabled"), t("debug.jdtls.disabledTitle"), {
          type: "info",
          confirmButtonText: t("debug.jdtls.enableAndContinue"),
          cancelButtonText: t("common.cancel"),
          distinguishCancelAndClose: true,
        }),
      );
    } catch {
      return false;
    }
    if (signal.aborted) return false;
    if (!host.enableBuiltinServer(JDTLS_SERVER_ID)) {
      warnUnavailable("disabled");
      return false;
    }
    availability = await host.builtinServerAvailability(JDTLS_SERVER_ID, "java");
  }
  if (availability === "available") return true;
  warnUnavailable(availability);
  return false;
}

let restartPrompt: { close(): void } | null = null;

/**
 * project-lsp:restart-required for jdtls: one notice "restart to apply the
 * plugin changes" with a Restart button, unless a Java launch is restarting
 * jdtls itself or the notice is already shown. restart restarts the running
 * jdtls sessions; `deferred`: some host a Java debug session and restart only
 * after it ends (the notice says so).
 */
export function handleJdtlsRestartRequired(
  event: LspRestartRequiredEvent,
  restart: () => Promise<void>,
  { deferred = false }: { deferred?: boolean } = {},
): void {
  if (event.serverId !== JDTLS_SERVER_ID || builtinServerLaunchInFlight(JDTLS_SERVER_ID) || restartPrompt) return;
  const { t } = useVueI18n();
  const run = () => {
    restartPrompt?.close();
    restart().catch((error: unknown) => {
      const reason = error instanceof Error ? error.message : String(error);
      ElMessage.error({ message: t("lsp.restartRequired.failed", { error: reason }), showClose: true, grouping: true });
    });
  };
  restartPrompt = ElNotification({
    type: "info",
    title: t("lsp.restartRequired.title"),
    message: h("div", { class: "app-jdtls-restart" }, [
      h("p", t("lsp.restartRequired.message")),
      ...(deferred ? [h("p", { "data-restart-deferred": "" }, t("lsp.restartRequired.deferred"))] : []),
      h(ElButton, { type: "primary", size: "small", onClick: run }, () => t("lsp.restartRequired.restart")),
    ]),
    duration: 0,
    onClose: () => {
      restartPrompt = null;
    },
  });
}
