import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent, ref, type VNode } from "vue";
import JdtlsBundleConfirmContent from "@/components/debug/JdtlsBundleConfirmContent.vue";
import { createPinia, setActivePinia } from "pinia";
import type { DebuggerInfo, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";
import type { BuiltinServerAvailability, ProjectLspHost } from "@/services/lsp-service";

const mocks = vi.hoisted(() => ({
  invoke: vi.fn(),
  confirm: vi.fn(),
  message: Object.assign(vi.fn((_options: unknown) => ({ close: vi.fn() })), { info: vi.fn(), warning: vi.fn(), error: vi.fn(), success: vi.fn() }),
  notify: vi.fn(),
  guard: vi.fn(),
  prepare: vi.fn(),
  trust: vi.fn(),
  start: vi.fn(),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: mocks.invoke,
  listenAppEvent: vi.fn((name: string, handler: (payload: unknown) => void) => {
    const set = mocks.listeners.get(name) ?? new Set();
    set.add(handler);
    mocks.listeners.set(name, set);
    return () => set.delete(handler);
  }),
}));
vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ElMessage: mocks.message,
  ElMessageBox: { confirm: mocks.confirm },
  ElNotification: mocks.notify,
}));
vi.mock("@/services/debug-guidance", () => ({ guardDebugLaunch: mocks.guard }));
vi.mock("@/services/debug-service", () => ({
  prepareDebugger: mocks.prepare,
  trustDebugger: mocks.trust,
  startDebugger: mocks.start,
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import { JDTLS_POLL_INTERVAL_MS, JDTLS_READY_TIMEOUT_MS, ensureJdtlsBundlesReady, handleJdtlsRestartRequired, jdtlsBundlesStatus } from "@/services/jdtls-bundles";
import { launchDebugSession } from "@/services/debug-launch";
import { registerProjectLspHost } from "@/services/lsp-service";
import { holdLspServer } from "@/services/lsp-server-holds";

const ROOT = "/Users/A/Proj";
const PLUGIN = "java-debug";
const FINGERPRINT = "f".repeat(64);
const SHA = "a".repeat(64);

interface StatusShape {
  trusted?: boolean;
  loaded?: boolean;
  running?: boolean;
  ready?: boolean;
  restartRequired?: boolean;
}

function status({ trusted = false, loaded = false, running = true, ready = true, restartRequired = false }: StatusShape = {}) {
  return {
    bundles: [
      {
        pluginId: PLUGIN,
        pluginName: "Java Debug",
        pluginVersion: "1.2.3",
        jars: [{ name: "com.microsoft.java.debug.plugin.jar", sha256: SHA, bytes: 1024 }],
        fingerprint: FINGERPRINT,
        trusted,
        previouslyTrusted: false,
        loaded,
      },
    ],
    restartRequired,
    jdtls: { running, ready },
  };
}

/** Answers plugin_host_jdtls_bundles_status from statuses in order (the last one repeats). */
function serveStatuses(...statuses: ReturnType<typeof status>[]) {
  let index = 0;
  mocks.invoke.mockImplementation(async (command: string, args: { payload: Record<string, unknown> }) => {
    if (command === "plugin_host_jdtls_bundles_status") {
      expect(args.payload).toEqual({ projectRoot: ROOT });
      return statuses[Math.min(index++, statuses.length - 1)];
    }
    if (command === "plugin_host_jdtls_bundles_trust") {
      expect(args.payload).toEqual({ pluginId: PLUGIN, fingerprint: FINGERPRINT });
      return { ...status().bundles[0], trusted: true };
    }
    throw new Error(`unexpected ${command}`);
  });
}

/** A project LSP event from the host (project-lsp:event). */
function lspEvent(event: { root?: string; serverId?: string; method: string; params: unknown }) {
  for (const handler of mocks.listeners.get("project-lsp:event") ?? []) handler({ root: ROOT, serverId: "jdtls", generation: 1, ...event });
}

function languageStatus(type: string, message: string, change: { root?: string; serverId?: string } = {}) {
  lspEvent({ method: "language/status", params: { type, message }, ...change });
}

function statusCalls() {
  return mocks.invoke.mock.calls.filter(([command]) => command === "plugin_host_jdtls_bundles_status").length;
}

function fakeHost(initial: BuiltinServerAvailability = "available") {
  let availability = initial;
  const host = {
    root: () => ROOT,
    active: () => true,
    builtinServerAvailability: vi.fn(async (_serverId: string, _language: string) => availability),
    // Like the project view: enabling jdtls in the project's LSP settings makes it serve Java.
    enableBuiltinServer: vi.fn((_serverId: string) => {
      if (availability === "disabled") availability = "available";
      return true;
    }),
    restartBuiltinServer: vi.fn(async (_serverId: string) => true),
  } satisfies ProjectLspHost;
  disposers.push(registerProjectLspHost(host));
  return host;
}

let disposers: (() => void)[] = [];

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
  mocks.message.mockImplementation((_options: unknown) => ({ close: vi.fn() }));
});

afterEach(() => {
  for (const dispose of disposers) dispose();
  disposers = [];
  vi.useRealTimers();
});

describe("ensureJdtlsBundlesReady", () => {
  it("asks to trust an untrusted bundle and restarts jdtls after confirm", async () => {
    const host = fakeHost();
    serveStatuses(status(), status({ trusted: true, restartRequired: true }), status({ trusted: true, loaded: true }));
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.invoke).toHaveBeenCalledWith("plugin_host_jdtls_bundles_trust", { payload: { pluginId: PLUGIN, fingerprint: FINGERPRINT } });
    // Agreed in this operation: no second question before the restart.
    expect(host.restartBuiltinServer).toHaveBeenCalledWith("jdtls");
    expect(host.builtinServerAvailability).toHaveBeenCalledWith("jdtls", "java");
  });

  it("reports each confirmation dialog opening and closing", async () => {
    fakeHost();
    serveStatuses(status(), status({ trusted: true, restartRequired: true }), status({ trusted: true, loaded: true }));
    const confirming: boolean[] = [];
    mocks.confirm.mockImplementationOnce(async () => {
      expect(confirming).toEqual([true]);
      return "confirm";
    });
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal, { onConfirm: (open) => confirming.push(open) })).resolves.toBe(true);
    expect(confirming).toEqual([true, false]);

    // Declined: closed too.
    serveStatuses(status());
    confirming.length = 0;
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal, { onConfirm: (open) => confirming.push(open) })).resolves.toBe(false);
    expect(confirming).toEqual([true, false]);
  });

  it("starts jdtls when it is not running and skips the dialog for a trusted bundle", async () => {
    const host = fakeHost();
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(host.restartBuiltinServer).toHaveBeenCalledTimes(1);
  });

  it("does nothing more when the bundle is already loaded into a ready jdtls", async () => {
    const host = fakeHost();
    serveStatuses(status({ trusted: true, loaded: true }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(host.restartBuiltinServer).not.toHaveBeenCalled();
    expect(statusCalls()).toBe(1);
  });

  it("asks before restarting a jdtls that hosts another Java debug session", async () => {
    const host = fakeHost();
    const release = holdLspServer(ROOT, "jdtls", "debug-session");
    disposers.push(release);
    serveStatuses(status({ trusted: true, restartRequired: true }));
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(String(mocks.confirm.mock.calls[0][0])).toMatch(/调试会话/);
    expect(host.restartBuiltinServer).not.toHaveBeenCalled();

    serveStatuses(status({ trusted: true, restartRequired: true }), status({ trusted: true, loaded: true }));
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(host.restartBuiltinServer).toHaveBeenCalledTimes(1);

    // A jdtls held only by this launch (no debug session) restarts without asking.
    release();
    mocks.confirm.mockClear();
    const ownLaunch = holdLspServer(ROOT, "jdtls", "launch");
    disposers.push(ownLaunch);
    serveStatuses(status({ trusted: true, restartRequired: true }), status({ trusted: true, loaded: true }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("shows the cancellable waiting message before restarting jdtls", async () => {
    const host = fakeHost();
    let shownBeforeRestart = false;
    host.restartBuiltinServer.mockImplementationOnce(async () => {
      shownBeforeRestart = mocks.message.mock.calls.length === 1;
      return true;
    });
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(shownBeforeRestart).toBe(true);

    // Cancelled while jdtls restarts: no polling afterwards.
    const cancel = () => (mocks.message.mock.calls.at(-1)?.[0] as { onClose: () => void }).onClose();
    host.restartBuiltinServer.mockImplementationOnce(async () => {
      cancel();
      return true;
    });
    serveStatuses(status({ trusted: true, running: false, ready: false }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(statusCalls()).toBe(3);
  });

  it("declining the bundle confirm aborts the launch", async () => {
    const host = fakeHost();
    serveStatuses(status());
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(mocks.invoke).not.toHaveBeenCalledWith("plugin_host_jdtls_bundles_trust", expect.anything());
    expect(host.restartBuiltinServer).not.toHaveBeenCalled();
  });

  it("waits for jdtls ready and can be cancelled", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true, ready: false }), status({ trusted: true, loaded: true }));
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal);
    await vi.advanceTimersByTimeAsync(5_000);
    await expect(ready).resolves.toBe(true);
    // The waiting message was shown and closed.
    const shown = mocks.message.mock.results.at(-1)?.value as { close: ReturnType<typeof vi.fn> };
    expect(shown.close).toHaveBeenCalled();

    // Cancelled from the waiting message: the launch stops without an error.
    serveStatuses(status({ trusted: true, loaded: true, ready: false }));
    const cancelled = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal);
    await vi.advanceTimersByTimeAsync(2_000);
    const options = mocks.message.mock.calls.at(-1)?.[0] as { onClose: () => void };
    options.onClose();
    await vi.advanceTimersByTimeAsync(2_000);
    await expect(cancelled).resolves.toBe(false);
    expect(mocks.message.error).not.toHaveBeenCalled();

    // Cancelled by the caller's signal.
    const controller = new AbortController();
    const aborted = ensureJdtlsBundlesReady(ROOT, PLUGIN, controller.signal);
    await vi.advanceTimersByTimeAsync(1_000);
    controller.abort();
    await vi.advanceTimersByTimeAsync(1_000);
    await expect(aborted).resolves.toBe(false);
  });

  it("leaves Cancel to the debug panel while the panel shows the launch", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, loaded: true, ready: false }));
    let inPanel = true;
    const panelCancel = new AbortController();
    const waiting = ensureJdtlsBundlesReady(ROOT, PLUGIN, panelCancel.signal, { cancelInPanel: () => inPanel });
    await vi.advanceTimersByTimeAsync(10);
    const options = mocks.message.mock.calls.at(-1)?.[0] as { message: VNode | (() => VNode); onClose: () => void };
    const render = () => mount(defineComponent({ render: () => (typeof options.message === "function" ? options.message() : options.message) }));
    expect(render().text()).toContain("Java");
    expect(render().find("button").exists()).toBe(false);
    // Closing the message only hides it: the panel's Cancel is the one.
    options.onClose();
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS * 2);
    let settled = false;
    void waiting.then(() => (settled = true));
    await vi.advanceTimersByTimeAsync(0);
    expect(settled).toBe(false);
    // Without the panel the message keeps its own Cancel.
    inPanel = false;
    expect(render().find("button").exists()).toBe(true);
    panelCancel.abort();
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS);
    await expect(waiting).resolves.toBe(false);
  });

  it("shows the waiting message with its Cancel again when the panel stops showing the launch after the message was closed", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, loaded: true, ready: false }));
    const inPanel = ref(true);
    const waiting = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal, { cancelInPanel: () => inPanel.value });
    await vi.advanceTimersByTimeAsync(10);
    type Options = { message: VNode | (() => VNode); onClose: () => void };
    const render = (options: Options) => mount(defineComponent({ render: () => (typeof options.message === "function" ? options.message() : options.message) }));
    const shownBefore = mocks.message.mock.calls.length;
    const first = mocks.message.mock.calls.at(-1)?.[0] as Options;
    // Hidden while the panel shows the launch with its Cancel ...
    first.onClose();
    // ... the panel hides it (another tab, collapsed): the message comes back with its own Cancel.
    inPanel.value = false;
    await vi.advanceTimersByTimeAsync(10);
    expect(mocks.message.mock.calls.length).toBe(shownBefore + 1);
    const again = mocks.message.mock.calls.at(-1)?.[0] as Options;
    expect(render(again).find("button").exists()).toBe(true);
    // Showing and hiding the panel again does not stack messages while this one is up.
    inPanel.value = true;
    await vi.advanceTimersByTimeAsync(10);
    inPanel.value = false;
    await vi.advanceTimersByTimeAsync(10);
    expect(mocks.message.mock.calls.length).toBe(shownBefore + 1);
    await render(again).find("button").trigger("click");
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS);
    await expect(waiting).resolves.toBe(false);
    // Nothing comes back once the wait has ended.
    inPanel.value = true;
    await vi.advanceTimersByTimeAsync(10);
    again.onClose();
    inPanel.value = false;
    await vi.advanceTimersByTimeAsync(10);
    expect(mocks.message.mock.calls.length).toBe(shownBefore + 1);
  });

  it("re-reads the status as soon as jdtls reports ServiceReady instead of waiting for the next poll", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true, ready: false }), status({ trusted: true, loaded: true }));
    let done = false;
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal).then((value) => {
      done = true;
      return value;
    });
    await vi.advanceTimersByTimeAsync(10);
    expect(statusCalls()).toBe(2);
    expect(done).toBe(false);
    // Another project's or server's ServiceReady does not count.
    languageStatus("ServiceReady", "ServiceReady", { root: "/Users/A/Other" });
    languageStatus("ServiceReady", "ServiceReady", { serverId: "pyright" });
    await vi.advanceTimersByTimeAsync(10);
    expect(statusCalls()).toBe(2);
    languageStatus("ServiceReady", "ServiceReady");
    await vi.advanceTimersByTimeAsync(10);
    expect(done).toBe(true);
    await expect(ready).resolves.toBe(true);
    expect(statusCalls()).toBe(3);
    // The listener is gone once the wait ends.
    expect(mocks.listeners.get("project-lsp:event")?.size ?? 0).toBe(0);
  });

  it("still polls without the event, at most every half second, and keeps the overall timeout", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, loaded: true, ready: false }), status({ trusted: true, loaded: true, ready: false }), status({ trusted: true, loaded: true }));
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal);
    await vi.advanceTimersByTimeAsync(10);
    const before = statusCalls();
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS);
    expect(statusCalls()).toBe(before + 1);
    expect(JDTLS_POLL_INTERVAL_MS).toBeLessThanOrEqual(500);
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS);
    await expect(ready).resolves.toBe(true);
  });

  it("passes jdtls's status texts on while waiting, made safe to show", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true, ready: false }));
    const texts: string[] = [];
    const controller = new AbortController();
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, controller.signal, { onStatus: (text) => texts.push(text) });
    await vi.advanceTimersByTimeAsync(10);
    languageStatus("Starting", "50% Starting Java Language Server - Project 'demo'");
    languageStatus("Starting", "Downloading x.jar\u202e\n 73%");
    languageStatus("Starting", "   ");
    languageStatus("Starting", "elsewhere", { root: "/Users/A/Other" });
    lspEvent({ method: "language/status", params: "junk" });
    expect(texts).toEqual(["50% Starting Java Language Server - Project 'demo'", "Downloading x.jar 73%"]);
    controller.abort();
    await vi.advanceTimersByTimeAsync(JDTLS_POLL_INTERVAL_MS);
    await expect(ready).resolves.toBe(false);
  });

  it("times out with a message", async () => {
    vi.useFakeTimers();
    fakeHost();
    serveStatuses(status({ trusted: true, loaded: true, ready: false }));
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal);
    await vi.advanceTimersByTimeAsync(JDTLS_READY_TIMEOUT_MS + 2_000);
    await expect(ready).resolves.toBe(false);
    expect(mocks.message.error).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringMatching(/180|Java/) }));
  });

  it("refuses when Java is assigned to a plugin language server", async () => {
    const host = fakeHost("plugin");
    serveStatuses(status());
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(mocks.message.warning).toHaveBeenCalledTimes(1);
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(statusCalls()).toBe(0);
    expect(host.restartBuiltinServer).not.toHaveBeenCalled();
  });

  it("offers to enable the built-in Java language service and continues", async () => {
    const host = fakeHost("disabled");
    serveStatuses(status({ trusted: true, running: false, ready: false }), status({ trusted: true, loaded: true }));
    mocks.confirm.mockResolvedValueOnce("confirm");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(true);
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.confirm.mock.calls[0][2]).toMatchObject({ confirmButtonText: "启用 Java 语言服务并继续" });
    expect(host.enableBuiltinServer).toHaveBeenCalledWith("jdtls");
    expect(host.restartBuiltinServer).toHaveBeenCalledWith("jdtls");
    expect(mocks.message.warning).not.toHaveBeenCalled();
  });

  it("declining to enable the built-in Java language service aborts the launch", async () => {
    const host = fakeHost("disabled");
    serveStatuses(status());
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(host.enableBuiltinServer).not.toHaveBeenCalled();
    expect(host.restartBuiltinServer).not.toHaveBeenCalled();
    expect(statusCalls()).toBe(0);
  });

  it("refuses when the project view cannot enable the Java language service", async () => {
    const host = fakeHost("disabled");
    host.enableBuiltinServer.mockReturnValueOnce(false);
    mocks.confirm.mockResolvedValueOnce("confirm");
    serveStatuses(status());
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(mocks.message.warning).toHaveBeenCalledTimes(1);
    expect(statusCalls()).toBe(0);
  });

  it("reports a jdtls that does not start", async () => {
    const host = fakeHost();
    host.restartBuiltinServer.mockResolvedValueOnce(false);
    serveStatuses(status({ trusted: true, running: false, ready: false }));
    await expect(ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal)).resolves.toBe(false);
    expect(mocks.message.error).toHaveBeenCalledTimes(1);
  });
});

describe("restart-required prompt", () => {
  it("restart-required event shows a restart prompt once", async () => {
    const close = vi.fn();
    mocks.notify.mockReturnValue({ close });
    const restart = vi.fn().mockResolvedValue(undefined);
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, restart);
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, restart);
    expect(mocks.notify).toHaveBeenCalledTimes(1);
    const options = mocks.notify.mock.calls[0][0] as { onClose: () => void };
    // Another server's event is not about jdtls bundles.
    handleJdtlsRestartRequired({ serverId: "gopls", reason: "bundles" }, restart);
    expect(mocks.notify).toHaveBeenCalledTimes(1);
    options.onClose();
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, restart);
    expect(mocks.notify).toHaveBeenCalledTimes(2);
    (mocks.notify.mock.calls[1][0] as { onClose: () => void }).onClose();
  });

  it("says when projects with a running Java debug session restart later", async () => {
    mocks.notify.mockReturnValue({ close: vi.fn() });
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, vi.fn(), { deferred: true });
    const options = mocks.notify.mock.calls[0][0] as { message: { children: { props?: Record<string, unknown> | null }[] }; onClose: () => void };
    expect(options.message.children.some((child) => child.props && "data-restart-deferred" in child.props)).toBe(true);
    options.onClose();
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, vi.fn());
    const plain = mocks.notify.mock.calls[1][0] as typeof options;
    expect(plain.message.children.some((child) => child.props && "data-restart-deferred" in child.props)).toBe(false);
    plain.onClose();
  });

  it("is not shown while a Java launch is loading the bundle", async () => {
    fakeHost();
    serveStatuses(status());
    let answer!: (value: string) => void;
    mocks.confirm.mockReturnValueOnce(new Promise((resolve) => (answer = resolve)));
    const ready = ensureJdtlsBundlesReady(ROOT, PLUGIN, new AbortController().signal);
    await vi.waitFor(() => expect(mocks.confirm).toHaveBeenCalled());
    handleJdtlsRestartRequired({ serverId: "jdtls", reason: "bundles" }, vi.fn());
    expect(mocks.notify).not.toHaveBeenCalled();
    serveStatuses(status({ trusted: true, loaded: true }));
    answer("confirm");
    await expect(ready).resolves.toBe(true);
  });
});

describe("launchDebugSession", () => {
  const cfg: RunConfiguration = { id: "c1", name: "Main", pluginId: PLUGIN, type: "java", request: "launch", fields: {} };
  function debuggerInfo(adapterKind: string): DebuggerInfo {
    return {
      pluginId: PLUGIN,
      pluginName: "Java Debug",
      type: "java",
      title: "Java",
      languages: ["java"],
      configurationSnippets: [],
      hasEntryPoints: false,
      hasCheck: false,
      adapterKind,
    };
  }
  const prepared: PreparedDebugLaunch = {
    prepareId: "p1",
    type: "java",
    kind: "jdtls",
    projectRoot: ROOT,
    request: "launch",
    arguments: {},
    launch: {
      pluginId: PLUGIN,
      pluginName: "Java Debug",
      pluginVersion: "1.2.3",
      point: "debuggers",
      entryKey: "java",
      title: "Java",
      argv: ["jdtls"],
      env: {},
      cwd: ROOT,
      pluginExecutable: false,
      fingerprint: FINGERPRINT,
      trusted: true,
      previouslyTrusted: false,
    },
  };

  beforeEach(() => {
    mocks.prepare.mockResolvedValue(prepared);
    mocks.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
  });

  it("jdtls debuggers load the bundle before preparing, and stop when it is not ready", async () => {
    fakeHost();
    mocks.guard.mockResolvedValue({ proceed: true, debugger: debuggerInfo("jdtls") });
    serveStatuses(status());
    mocks.confirm.mockRejectedValueOnce("cancel");
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false })).resolves.toBeNull();
    expect(mocks.prepare).not.toHaveBeenCalled();

    serveStatuses(status({ trusted: true, loaded: true }));
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false })).resolves.toMatchObject({ sessionId: "s1" });
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
  });

  it("non-jdtls debuggers skip the bundle flow", async () => {
    mocks.guard.mockResolvedValue({ proceed: true, debugger: debuggerInfo("stdio") });
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false })).resolves.toMatchObject({ sessionId: "s1" });
    expect(mocks.invoke).not.toHaveBeenCalled();
    mocks.guard.mockResolvedValue({ proceed: true });
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false })).resolves.toMatchObject({ sessionId: "s1" });
    expect(mocks.invoke).not.toHaveBeenCalled();
  });
});


it("bundle confirmation keeps long file lists in an Element Plus scrollbar without hiding commands or risk notes", () => {
  const bundle = status().bundles[0];
  const commands = Array.from({ length: 40 }, (_, i) => `vscode.java.command${i}`);
  const wrapper = mount(JdtlsBundleConfirmContent, { props: { bundle: {
    ...bundle, jars: [{ ...bundle.jars[0]!, commands }],
  } } });
  try {
    const scrollbar = wrapper.get(".el-scrollbar");
    expect(wrapper.findAll(".el-scrollbar")).toHaveLength(1);
    expect(scrollbar.find(".el-scrollbar__wrap").exists()).toBe(true);
    expect(scrollbar.findAll("[data-jdtls-command]").map((node) => node.text())).toEqual(commands);
    expect(scrollbar.get("[data-jdtls-jar]").text()).toContain(SHA);
    expect(scrollbar.findAll(".app-jdtls-bundle-confirm__note")).toHaveLength(2);
  } finally {
    wrapper.unmount();
  }
});

it("bundle confirmation lists every jar's declared commands", () => {
  const bundle = status().bundles[0];
  const wrapper = mount(JdtlsBundleConfirmContent, { props: { bundle: {
    ...bundle, jars: [{ ...bundle.jars[0]!, commands: ["vscode.java.startDebugSession", "custom.command"] }],
  } } });
  expect(wrapper.findAll("[data-jdtls-command]").map((node) => node.text())).toEqual(["vscode.java.startDebugSession", "custom.command"]);
  expect(wrapper.get("[data-jdtls-jar]").text()).toContain(SHA);
  wrapper.unmount();
});

function dependencyBundle() {
  const bundle = status().bundles[0];
  const main = { ...bundle.jars[0]!, bytes: 1024 * 1024, commands: ["vscode.java.test.junit.argument"] };
  const dependencies = Array.from({ length: 29 }, (_, i) => ({
    name: `dep${i}.jar`, sha256: SHA, bytes: 64 * 1024, commands: [], dependencyOf: `lib/${main.name}`,
  }));
  return { ...bundle, jars: [main, ...dependencies] };
}

it("bundle status keeps which jar a dependency belongs to", async () => {
  const raw = status();
  raw.bundles[0]!.jars = [
    { name: "main.jar", sha256: SHA, bytes: 1 },
    { name: "dep.jar", sha256: SHA, bytes: 2, commands: [], dependencyOf: "lib/main.jar" },
    { name: "odd.jar", sha256: SHA, bytes: 3, dependencyOf: 7 },
  ] as typeof raw.bundles[0]["jars"];
  mocks.invoke.mockResolvedValueOnce(raw);
  const parsed = await jdtlsBundlesStatus(ROOT);
  expect(parsed.bundles[0]!.jars).toEqual([
    { name: "main.jar", sha256: SHA, bytes: 1, commands: [] },
    { name: "dep.jar", sha256: SHA, bytes: 2, commands: [], dependencyOf: "lib/main.jar" },
    { name: "odd.jar", sha256: SHA, bytes: 3, commands: [] },
  ]);
});

it("bundle confirmation summarizes the file count and total size and folds dependency jars", () => {
  const wrapper = mount(JdtlsBundleConfirmContent, { props: { bundle: dependencyBundle() } });
  try {
    // 1 MiB + 29 × 64 KiB = 2.8125 MiB
    const summary = wrapper.get("[data-jdtls-summary]").text();
    expect(summary).toContain("30");
    expect(summary).toContain("2.8 MB");
    expect(wrapper.findAll("[data-jdtls-jar]")).toHaveLength(1);
    expect(wrapper.findAll("[data-jdtls-command]").map((node) => node.text())).toEqual(["vscode.java.test.junit.argument"]);
    const details = wrapper.get("details[data-jdtls-dependencies]");
    expect(details.attributes("open")).toBeUndefined();
    expect(details.get("summary").text()).toContain("29");
    const dependencies = details.findAll("[data-jdtls-dependency]");
    expect(dependencies).toHaveLength(29);
    expect(dependencies[0]!.text()).toContain("dep0.jar");
    expect(dependencies[0]!.text()).toContain(SHA);
  } finally {
    wrapper.unmount();
  }
});

it("bundle confirmation without dependencies shows no dependency section", () => {
  const wrapper = mount(JdtlsBundleConfirmContent, { props: { bundle: status().bundles[0] } });
  expect(wrapper.find("details[data-jdtls-dependencies]").exists()).toBe(false);
  expect(wrapper.get("[data-jdtls-summary]").text()).toContain("1.0 KB");
  wrapper.unmount();
});
