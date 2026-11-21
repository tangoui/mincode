import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElMessage, ElSelect } from "element-plus";
import PluginCommandParamsDialog from "@/components/plugins/PluginCommandParamsDialog.vue";
import PluginServiceCallsHost from "@/components/plugins/PluginServiceCallsHost.vue";
import { useVueI18n } from "@/i18n/messages";
import {
  cancelPluginServiceCall,
  confirmPluginServiceParams,
  dismissPluginServiceCall,
  pluginServiceCalls,
  pluginServiceParamRequest,
  pluginServiceResult,
  pluginServices,
  runPluginService,
  savePluginServiceResultAs,
  startPluginServiceSync,
} from "@/services/plugin-services";

const bridge = vi.hoisted(() => ({
  invoke: vi.fn(),
  handlers: new Map<string, Array<(payload: unknown) => void>>(),
  chooseSave: vi.fn(),
  copy: vi.fn(),
  saveText: vi.fn(),
}));

vi.mock("@/services/file-download", () => ({
  saveTextFileToDisk: (...args: unknown[]) => bridge.saveText(...args),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: (...args: unknown[]) => bridge.invoke(...args),
  isDesktopRuntime: () => true,
  listenAppEvent: (event: string, handler: (payload: unknown) => void) => {
    const list = bridge.handlers.get(event) ?? [];
    list.push(handler);
    bridge.handlers.set(event, list);
    return () => bridge.handlers.set(event, (bridge.handlers.get(event) ?? []).filter((item) => item !== handler));
  },
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  chooseNativeSaveFile: (...args: unknown[]) => bridge.chooseSave(...args),
  copyText: (...args: unknown[]) => bridge.copy(...args),
}));

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  const message = Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() });
  return { ...actual, ElMessage: message };
});

function emit(event: string, payload: unknown) {
  for (const handler of bridge.handlers.get(event) ?? []) handler(payload);
}

const provider = { pluginId: "demo.provider", entryId: "reverse" };

function entry(overrides: Record<string, unknown> = {}) {
  return {
    pluginId: "demo.provider",
    pluginName: "Demo Provider",
    entryId: "reverse",
    service: "demo.text.reverse",
    title: "Reverse text",
    description: "",
    input: { kind: "text", maxBytes: 8192 },
    output: { kind: "text" },
    surfaces: ["selection", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

let services: unknown[] = [];
let startResult: unknown = { callId: "sc_1", state: "running", provider };
let stop: (() => void) | undefined;

beforeEach(async () => {
  bridge.handlers.clear();
  bridge.invoke.mockReset();
  bridge.chooseSave.mockReset();
  bridge.copy.mockReset();
  bridge.saveText.mockReset();
  vi.mocked(ElMessage.error).mockClear();
  services = [entry()];
  startResult = { callId: "sc_1", state: "running", provider };
  bridge.invoke.mockImplementation(async (command: string, args: Record<string, unknown>) => {
    if (command === "plugin_services_list") return { services };
    if (command === "plugin_services_start") return startResult;
    if (command === "plugin_services_cancel") return { callId: args.callId, state: "running", provider };
    return {};
  });
  stop = startPluginServiceSync();
  await flushPromises();
});

afterEach(() => {
  stop?.();
  stop = undefined;
});

function startCalls() {
  return bridge.invoke.mock.calls.filter(([command]) => command === "plugin_services_start");
}

describe("plugin services", () => {
  it("loads the service list and refreshes it when contributions change", async () => {
    expect(pluginServices.value.map((item) => item.service)).toEqual(["demo.text.reverse"]);
    services = [entry(), entry({ pluginId: "other.provider", pluginName: "Other" })];
    emit("plugin:contributions-changed", { revision: 9 });
    await vi.waitFor(() => expect(pluginServices.value).toHaveLength(2));
  });

  it("asks for params before starting and starts with the serialized values", async () => {
    services = [entry({ params: { type: "object", properties: { keepLines: { type: "boolean" } } } })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value[0]?.params).toBeTruthy());

    const run = runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    await flushPromises();
    expect(startCalls()).toHaveLength(0);
    expect(pluginServiceParamRequest.value?.title).toBe("Reverse text");

    await confirmPluginServiceParams({ keepLines: true });
    await run;
    expect(startCalls()).toHaveLength(1);
    expect(startCalls()[0][1]).toMatchObject({
      service: "demo.text.reverse",
      surface: "selection",
      input: "abc",
      params: { keepLines: true },
      provider,
    });
    expect(pluginServiceCalls.value.map((call) => call.callId)).toEqual(["sc_1"]);
  });

  it("does not start when the params form is cancelled", async () => {
    services = [entry({ params: { type: "object", properties: { keepLines: { type: "boolean" } } } })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value[0]?.params).toBeTruthy());
    const run = runPluginService({ service: "demo.text.reverse", surface: "palette", input: "abc" });
    await flushPromises();
    const { cancelPluginServiceParams } = await import("@/services/plugin-services");
    cancelPluginServiceParams();
    await run;
    expect(startCalls()).toHaveLength(0);
  });

  it("follows progress events and opens the text result", async () => {
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    expect(startCalls()[0][1]).not.toHaveProperty("params");
    emit("plugin:service-call-changed", { callId: "sc_1", state: "running", provider, progress: { completed: 1, total: 4, message: "step 1" } });
    expect(pluginServiceCalls.value[0]).toMatchObject({ state: "running", percent: 25, progress: { message: "step 1" } });

    emit("plugin:service-call-changed", { callId: "sc_1", state: "succeeded", provider, output: "cba" });
    expect(pluginServiceCalls.value[0].state).toBe("succeeded");
    expect(pluginServiceResult.value).toMatchObject({ callId: "sc_1", kind: "text", text: "cba" });
  });

  it("applies a change event that arrives before start returns", async () => {
    bridge.invoke.mockImplementation(async (command: string) => {
      if (command === "plugin_services_list") return { services };
      if (command === "plugin_services_start") {
        emit("plugin:service-call-changed", { callId: "sc_1", state: "succeeded", provider, output: "fast" });
        return { callId: "sc_1", state: "running", provider };
      }
      return {};
    });
    await runPluginService({ service: "demo.text.reverse", surface: "palette", input: "abc" });
    expect(pluginServiceCalls.value[0].state).toBe("succeeded");
    expect(pluginServiceResult.value?.text).toBe("fast");
  });

  it("cancels through plugin_services_cancel", async () => {
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    await cancelPluginServiceCall("sc_1");
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_cancel", { callId: "sc_1" });
  });

  it("shows an unconfirmed failure as unconfirmed, not cancelled", async () => {
    const { t } = useVueI18n();
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    emit("plugin:service-call-changed", {
      callId: "sc_1", state: "failed", provider, unconfirmed: true, error: "unavailable: the provider did not confirm the cancellation",
    });
    const wrapper = mount(PluginServiceCallsHost, { attachTo: document.body });
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain(t("plugins.services.unconfirmed"));
    expect(text).not.toContain(t("plugins.services.cancelled"));
    wrapper.unmount();
  });

  it("renders a running call with its progress and a cancel button", async () => {
    const { t } = useVueI18n();
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    emit("plugin:service-call-changed", { callId: "sc_1", state: "running", provider, progress: { completed: 1, total: 2, message: "<b>half</b>" } });
    const wrapper = mount(PluginServiceCallsHost, { attachTo: document.body });
    await flushPromises();
    expect(wrapper.text()).toContain("Reverse text");
    expect(wrapper.text()).toContain("<b>half</b>");
    expect(wrapper.find(".el-progress").exists()).toBe(true);
    const cancel = wrapper.findAll("button").find((button) => button.text() === t("plugins.services.cancel"));
    expect(cancel).toBeTruthy();
    await cancel!.trigger("click");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_cancel", { callId: "sc_1" });
    wrapper.unmount();
  });

  it("saves output files through the native save dialog", async () => {
    services = [entry({ entryId: "copy", service: "demo.file.copy", title: "Copy file", input: { kind: "file", accepts: [".txt"], maxBytes: 1024 }, output: { kind: "file" }, surfaces: ["fileMenu"] })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value[0]?.service).toBe("demo.file.copy"));
    const copyProvider = { pluginId: "demo.provider", entryId: "copy" };
    startResult = { callId: "sc_2", state: "running", provider: copyProvider };
    bridge.chooseSave.mockResolvedValue("/tmp/out.txt");
    await runPluginService({ service: "demo.file.copy", surface: "fileMenu", paths: ["/work/a.txt"] });
    expect(startCalls()[0][1]).toMatchObject({ paths: ["/work/a.txt"], surface: "fileMenu" });
    emit("plugin:service-call-changed", {
      callId: "sc_2", state: "succeeded", provider: copyProvider, files: [{ token: "sf_abc", name: "a.txt", mime: "text/plain", size: 3 }],
    });
    await vi.waitFor(() =>
      expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_output_save", { callId: "sc_2", token: "sf_abc", path: "/tmp/out.txt" }));
    await vi.waitFor(() => expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_release", { callId: "sc_2" }));
  });

  it("dismisses finished calls", async () => {
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    emit("plugin:service-call-changed", { callId: "sc_1", state: "cancelled", provider });
    dismissPluginServiceCall("sc_1");
    expect(pluginServiceCalls.value).toHaveLength(0);
  });

  it("asks which provider to use when fitting providers declare different params", async () => {
    services = [
      entry({ params: { type: "object", properties: { keepLines: { type: "boolean" } } } }),
      entry({ pluginId: "other.provider", pluginName: "Other", entryId: "flip", title: "Flip text", params: { type: "object", properties: { mode: { type: "string" } } } }),
    ];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value).toHaveLength(2));

    const run = runPluginService({ service: "demo.text.reverse", surface: "palette", input: "abc" });
    await flushPromises();
    expect(startCalls()).toHaveLength(0);
    const request = pluginServiceParamRequest.value;
    expect(request?.providers?.map((provider) => provider.label)).toEqual(["Demo Provider · Reverse text", "Other · Flip text"]);

    const confirmed: unknown[][] = [];
    const dialog = mount(PluginCommandParamsDialog, {
      props: { request, onConfirm: (...args: unknown[]) => confirmed.push(args) },
      attachTo: document.body,
    });
    await flushPromises();
    const confirmButton = () => [...document.body.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent?.trim() === useVueI18n().t("commandPalette.pluginParams.confirm"));
    confirmButton()?.click();
    await flushPromises();
    expect(confirmed).toHaveLength(0);

    dialog.findComponent(ElSelect).vm.$emit("update:modelValue", "other.provider/flip");
    await flushPromises();
    expect(document.body.textContent).toContain("mode");
    expect(document.body.textContent).not.toContain("keepLines");
    confirmButton()?.click();
    await flushPromises();
    expect(confirmed).toHaveLength(1);
    expect(confirmed[0][1]).toBe("other.provider/flip");
    dialog.unmount();

    await confirmPluginServiceParams({ mode: "upper" }, "other.provider/flip");
    await run;
    expect(startCalls()[0][1]).toMatchObject({ provider: { pluginId: "other.provider", entryId: "flip" }, params: { mode: "upper" } });
  });

  it("leaves the provider to the host when fitting providers share their params", async () => {
    const params = { type: "object", properties: { keepLines: { type: "boolean" } } };
    services = [entry({ params }), entry({ pluginId: "other.provider", pluginName: "Other", params })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value).toHaveLength(2));
    const run = runPluginService({ service: "demo.text.reverse", surface: "palette", input: "abc" });
    await flushPromises();
    expect(pluginServiceParamRequest.value?.providers).toBeUndefined();
    await confirmPluginServiceParams({ keepLines: false });
    await run;
    expect(startCalls()[0][1]).not.toHaveProperty("provider");
    expect(startCalls()[0][1]).toMatchObject({ params: { keepLines: false } });
  });

  it("saves a text or JSON result as a file", async () => {
    bridge.saveText.mockResolvedValue(true);
    await runPluginService({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    emit("plugin:service-call-changed", { callId: "sc_1", state: "succeeded", provider, output: "cba" });
    await savePluginServiceResultAs();
    expect(bridge.saveText).toHaveBeenCalledWith(expect.objectContaining({ filename: "Reverse text.txt", content: "cba" }));

    services = [entry({ entryId: "stats", service: "demo.text.stats", title: "Text: stats?", output: { kind: "json" } })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value[0]?.service).toBe("demo.text.stats"));
    const statsProvider = { pluginId: "demo.provider", entryId: "stats" };
    startResult = { callId: "sc_3", state: "running", provider: statsProvider };
    dismissPluginServiceCall("sc_1");
    await runPluginService({ service: "demo.text.stats", surface: "selection", input: "abc" });
    emit("plugin:service-call-changed", { callId: "sc_3", state: "succeeded", provider: statsProvider, output: { words: 1 } });
    await savePluginServiceResultAs();
    expect(bridge.saveText).toHaveBeenLastCalledWith(expect.objectContaining({ filename: "Text_ stats_.json", content: '{\n  "words": 1\n}' }));
  });

  it("shows the host's reason when a start is refused as busy", async () => {
    bridge.invoke.mockImplementation(async (command: string) => {
      if (command === "plugin_services_list") return { services };
      if (command === "plugin_services_start") throw new Error("busy: the exchange has no room for another file call");
      return {};
    });
    await runPluginService({ service: "demo.text.reverse", surface: "palette", input: "abc" });
    const message = vi.mocked(ElMessage.error).mock.calls.at(-1)?.[0] as { message: string };
    expect(message.message).toContain("the exchange has no room for another file call");
    expect(message.message).not.toContain("prompt");
  });

  async function useLogService() {
    services = [entry({
      entryId: "log", service: "log.analyze", title: "Analyze log",
      input: { kind: "file", accepts: [".log"], maxBytes: 1048576 }, output: { kind: "text" }, surfaces: ["sftpFile"],
    })];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value[0]?.service).toBe("log.analyze"));
    startResult = { callId: "sc_9", state: "running", provider: { pluginId: "demo.provider", entryId: "log" }, phase: "input" };
  }

  it("starts a remote file call and shows the input phase with byte progress", async () => {
    const { t } = useVueI18n();
    await useLogService();
    const logProvider = { pluginId: "demo.provider", entryId: "log" };
    const surfaceContext = { remote: { connection: { id: "c", name: "n", protocol: "sftp", host: "h", port: 22, username: "u" }, path: "/var/log/app.log" } };
    await runPluginService({ service: "log.analyze", surface: "sftpFile", remoteFile: { sessionId: "s1", path: "/var/log/app.log" }, surfaceContext });
    expect(startCalls()[0][1]).toEqual({ service: "log.analyze", surface: "sftpFile", remoteFile: { sessionId: "s1", path: "/var/log/app.log" }, surfaceContext });
    expect(pluginServiceCalls.value[0]).toMatchObject({ phase: "input", remoteInput: true, percent: undefined });

    const wrapper = mount(PluginServiceCallsHost, { attachTo: document.body });
    await flushPromises();
    expect(wrapper.text()).toContain(t("plugins.services.phaseInput"));
    expect(wrapper.find(".el-progress").exists()).toBe(true);

    emit("plugin:service-call-changed", { callId: "sc_9", state: "running", provider: logProvider, phase: "input", progress: { completed: 512, total: 2048 } });
    await flushPromises();
    expect(pluginServiceCalls.value[0]).toMatchObject({ phase: "input", percent: 25 });
    expect(wrapper.text()).toContain("512 B / 2.0 KB");

    // Dispatch: the input phase and its byte progress end.
    emit("plugin:service-call-changed", { callId: "sc_9", state: "running", provider: logProvider });
    await flushPromises();
    expect(pluginServiceCalls.value[0]).toMatchObject({ phase: undefined, progress: undefined, percent: undefined });
    expect(wrapper.text()).toContain(t("plugins.services.running"));
    wrapper.unmount();
  });

  it("explains a remote read failure by its code", async () => {
    const { t } = useVueI18n();
    await useLogService();
    const logProvider = { pluginId: "demo.provider", entryId: "log" };
    await runPluginService({ service: "log.analyze", surface: "sftpFile", remoteFile: { sessionId: "s1", path: "/var/log/app.log" } });
    emit("plugin:service-call-changed", { callId: "sc_9", state: "failed", phase: "input", provider: logProvider, error: "not_found: the remote file is not available" });
    const wrapper = mount(PluginServiceCallsHost, { attachTo: document.body });
    await flushPromises();
    expect(pluginServiceCalls.value[0]).toMatchObject({ state: "failed", failedInInput: true });
    expect(wrapper.text()).toContain(t("plugins.services.remoteError.notFound"));
    expect(wrapper.text()).not.toContain("the remote file is not available");
    wrapper.unmount();
  });

  it("does not report a failure after the remote read as a remote error", async () => {
    const { t } = useVueI18n();
    await useLogService();
    const logProvider = { pluginId: "demo.provider", entryId: "log" };
    await runPluginService({ service: "log.analyze", surface: "sftpFile", remoteFile: { sessionId: "s1", path: "/var/log/app.log" } });
    // The read finished (the last input frame), then activating the provider failed: no phase on the failure.
    emit("plugin:service-call-changed", { callId: "sc_9", state: "running", phase: "input", provider: logProvider, progress: { completed: 4, total: 4 } });
    emit("plugin:service-call-changed", { callId: "sc_9", state: "failed", provider: logProvider, error: "unavailable: plugin failed to start" });
    const wrapper = mount(PluginServiceCallsHost, { attachTo: document.body });
    await flushPromises();
    expect(pluginServiceCalls.value[0]).toMatchObject({ state: "failed", failedInInput: false });
    expect(wrapper.text()).not.toContain(t("plugins.services.remoteError.unavailable"));
    expect(wrapper.text()).toContain("plugin failed to start");
    wrapper.unmount();
  });

  it("hands content to providers whose accepts match the host's file name", async () => {
    services = [
      entry({ entryId: "types", service: "json.types", title: "Types", input: { kind: "file", accepts: [".json"], maxBytes: 1048576 }, surfaces: ["httpResponse"],
        params: { type: "object", properties: { lang: { type: "string" } } } }),
      entry({ pluginId: "other.provider", pluginName: "Other", entryId: "types", service: "json.types", title: "Types (XML)", input: { kind: "file", accepts: [".xml"], maxBytes: 1048576 }, surfaces: ["httpResponse"],
        params: { type: "object", properties: { mode: { type: "string" } } } }),
    ];
    emit("plugin:contributions-changed", {});
    await vi.waitFor(() => expect(pluginServices.value).toHaveLength(2));
    const run = runPluginService({ service: "json.types", surface: "httpResponse", content: { text: "{}", contentType: "application/json" }, surfaceContext: { response: {} } });
    await flushPromises();
    // Only the .json provider takes response.json, so its form is asked for and the call names it.
    expect(pluginServiceParamRequest.value?.providers).toBeUndefined();
    await confirmPluginServiceParams({ lang: "ts" });
    await run;
    expect(startCalls()[0][1]).toMatchObject({
      provider: { pluginId: "demo.provider", entryId: "types" },
      content: { text: "{}", contentType: "application/json" },
      surfaceContext: { response: {} },
    });
  });
});
