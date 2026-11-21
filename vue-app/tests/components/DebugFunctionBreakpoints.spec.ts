import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import DebugBreakpointsDialog from "@/components/debug/DebugBreakpointsDialog.vue";
import { newSessionView } from "@/domain/debug/session-model";
import type { DebuggerInfo, RunConfiguration } from "@/domain/debug/types";
import { applySavedLocale, useVueI18n } from "@/i18n/messages";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const service = vi.hoisted(() => ({ listDebuggers: vi.fn(), warning: vi.fn() }));

vi.mock("element-plus", async (importOriginal) => {
  const original = await importOriginal<typeof import("element-plus")>();
  return { ...original, ElMessage: Object.assign(original.ElMessage, { warning: service.warning }) };
});

vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => false }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: service.listDebuggers,
  applyDebuggerSettings: vi.fn(),
  debugRequest: vi.fn(),
  setSessionBreakpoints: vi.fn(),
  stopDebugSession: vi.fn(),
  onDebugEvent: vi.fn(),
}));

const ROOT = "/fixture";

// Plugin texts in the shape the Java plugin declares them (the host never writes a format itself).
const java: DebuggerInfo = {
  pluginId: "java-debug",
  pluginName: "Java Debugger",
  type: "java",
  title: "Java (java-debug)",
  languages: ["java"],
  configurationSnippets: [],
  hasEntryPoints: true,
  hasCheck: true,
  functionBreakpoints: {
    placeholder: "Class#method, e.g. com.example.Foo#bar",
    placeholderLocalized: { zh: "类#方法，例如 com.example.Foo#bar" },
    description: "Stops on entry to every overload.",
    descriptionLocalized: { zh: "在该方法的每个重载入口暂停。" },
  },
};
const python: DebuggerInfo = {
  pluginId: "python-debug",
  pluginName: "Python Debugger",
  type: "python",
  title: "Python (debugpy)",
  languages: ["python"],
  configurationSnippets: [],
  hasEntryPoints: true,
  hasCheck: true,
};

function config(debugger_: DebuggerInfo): RunConfiguration {
  return { id: `cfg-${debugger_.type}`, name: debugger_.type, pluginId: debugger_.pluginId, type: debugger_.type, request: "launch", fields: {} };
}

let wrapper: VueWrapper | undefined;

beforeEach(async () => {
  await applySavedLocale("zh");
  service.listDebuggers.mockReset().mockResolvedValue([java, python]);
  service.warning.mockReset();
});

afterEach(async () => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
  await applySavedLocale("zh");
});

async function open(setup?: (configs: ReturnType<typeof useDebugConfigsStore>) => void) {
  const configs = useDebugConfigsStore();
  await configs.load();
  configs.upsertConfig(ROOT, config(java));
  configs.upsertConfig(ROOT, config(python));
  setup?.(configs);
  const sessions = useDebugSessionsStore();
  const sync = vi.fn().mockResolvedValue(undefined);
  sessions.syncFunctionBreakpoints = sync;
  wrapper = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
  await flushPromises();
  return { configs, sessions, sync };
}

function section(): HTMLElement | null {
  return document.querySelector(".debug-breakpoints-dialog__functions");
}

function nameInput(): HTMLInputElement {
  const inputs = document.querySelectorAll<HTMLInputElement>(".debug-breakpoints-dialog__function-input input");
  expect(inputs).toHaveLength(1);
  return inputs[0]!;
}

async function type(text: string) {
  const input = nameInput();
  input.value = text;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
}

async function submit() {
  nameInput().closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await flushPromises();
}

function row(name: string): HTMLElement | null {
  return document.querySelector(`.debug-breakpoints-dialog__function[data-function-name='${name}']`);
}

/** ElFormItem shows and hides its error after a short debounce. */
async function expectError(message: string | null) {
  await vi.waitFor(() => {
    const error = document.querySelector(".debug-breakpoints-dialog__functions .el-form-item__error");
    expect(error?.textContent?.trim() ?? null).toBe(message);
  });
}

describe("DebugBreakpointsDialog method breakpoints", () => {
  it("offers the section only for debuggers that declare functionBreakpoints, with their texts and the slow-down warning", async () => {
    await open();
    const { t } = useVueI18n();
    expect(section()?.querySelector("h4")?.textContent).toBe(t("debug.breakpoints.functions"));
    const groups = document.querySelectorAll(".debug-breakpoints-dialog__function-group");
    expect([...groups].map((group) => group.getAttribute("data-debugger-type"))).toEqual(["java"]);
    expect(nameInput().placeholder).toBe("类#方法，例如 com.example.Foo#bar");
    expect(section()?.textContent).toContain("在该方法的每个重载入口暂停。");
    expect(section()?.querySelector(".debug-breakpoints-dialog__function-slow")?.textContent).toContain("方法断点会明显拖慢程序");
    expect(t("debug.breakpoints.functionSlow")).toBe("方法断点会明显拖慢程序");
  });

  it("uses the English texts in English", async () => {
    await applySavedLocale("en");
    await open();
    const { t } = useVueI18n();
    expect(t("debug.breakpoints.functionSlow")).toBe("Method breakpoints slow the program down noticeably");
    expect(nameInput().placeholder).toBe("Class#method, e.g. com.example.Foo#bar");
    expect(section()?.textContent).toContain("Stops on entry to every overload.");
  });

  it("shows no section when no debugger of the project declares method breakpoints", async () => {
    service.listDebuggers.mockResolvedValue([python, { ...java, functionBreakpoints: undefined }]);
    await open();
    expect(section()).toBeNull();
  });

  it("shows no section when the debugger listing fails", async () => {
    service.listDebuggers.mockRejectedValue(new Error("unavailable: debugging needs the MinCode desktop app"));
    await open();
    expect(section()).toBeNull();
  });

  it("adds a trimmed name at once and syncs the project's running sessions of the type", async () => {
    const { configs, sync } = await open();
    await type("  app.Main#run  ");
    await submit();
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toEqual([{ name: "app.Main#run", enabled: true }]);
    expect(sync).toHaveBeenCalledExactlyOnceWith(ROOT, "java");
    expect(nameInput().value).toBe("");
    expect(row("app.Main#run")?.textContent).toContain("app.Main#run");
  });

  it("adds with the Add button and ignores a blank name", async () => {
    const { configs, sync } = await open();
    const add = document.querySelector<HTMLButtonElement>(".debug-breakpoints-dialog__function-add")!;
    expect(add.disabled).toBe(true);
    await type("   ");
    expect(add.disabled).toBe(true);
    await submit();
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toBeUndefined();
    await type("app.Main#stop");
    expect(add.disabled).toBe(false);
    add.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toEqual([{ name: "app.Main#stop", enabled: true }]);
    expect(sync).toHaveBeenCalledTimes(1);
  });

  it("refuses a duplicate, an invalid or too long name and a full list, saying why", async () => {
    const { t } = useVueI18n();
    const full = Array.from({ length: 256 }, (_, i) => ({ name: `app.C#m${i}`, enabled: true }));
    const { configs, sync } = await open((store) => store.setFunctionBreakpoints(ROOT, "java", [{ name: "app.Main#run", enabled: false }]));
    await type("app.Main#run ");
    await submit();
    await expectError(t("debug.breakpoints.functionDuplicate"));
    await type("x".repeat(1025));
    await expectError(null);
    await submit();
    await expectError(t("debug.breakpoints.functionNameTooLong", { max: 1024 }));
    await type("bad\u0007name");
    await submit();
    await expectError(t("debug.breakpoints.functionNameInvalid"));
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toEqual([{ name: "app.Main#run", enabled: false }]);
    configs.setFunctionBreakpoints(ROOT, "java", full);
    await flushPromises();
    await type("app.Main#more");
    await submit();
    await expectError(t("debug.breakpoints.functionsTooMany", { max: 256 }));
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toHaveLength(256);
    expect(sync).not.toHaveBeenCalled();
  });

  it("disables and removes at once, syncing each change", async () => {
    const { configs, sync } = await open((store) =>
      store.setFunctionBreakpoints(ROOT, "java", [
        { name: "app.Main#run", enabled: true },
        { name: "app.Main#stop", enabled: true },
      ]),
    );
    const toggle = row("app.Main#run")!.querySelector<HTMLElement>(".el-switch")!;
    toggle.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toEqual([
      { name: "app.Main#run", enabled: false },
      { name: "app.Main#stop", enabled: true },
    ]);
    expect(sync).toHaveBeenCalledTimes(1);
    const { t } = useVueI18n();
    row("app.Main#stop")!.querySelector<HTMLButtonElement>(`button[aria-label='${t("debug.breakpoints.remove")}']`)!.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toEqual([{ name: "app.Main#run", enabled: false }]);
    expect(row("app.Main#stop")).toBeNull();
    expect(sync).toHaveBeenCalledTimes(2);
    expect(sync).toHaveBeenLastCalledWith(ROOT, "java");
  });

  it("shows an unverified method breakpoint with the adapter's message, and updates when it is verified later", async () => {
    const { t } = useVueI18n();
    const { sessions } = await open((store) =>
      store.setFunctionBreakpoints(ROOT, "java", [
        { name: "app.Main#run", enabled: true },
        { name: "app.Main#nope", enabled: true },
        { name: "app.Later#work", enabled: true },
        { name: "app.Main#off", enabled: false },
      ]),
    );
    const view = newSessionView({
      id: "s1",
      projectRoot: ROOT,
      configId: "cfg-java",
      configName: "java",
      type: "java",
      noDebug: false,
      capabilities: { supportsFunctionBreakpoints: true },
      startedAt: 1,
    });
    sessions.sessions.push({
      ...view,
      functionBreakpoints: [
        { name: "app.Main#run", verified: true, id: 1 },
        { name: "app.Main#nope", verified: false, id: 2, message: "no such method" },
        { name: "app.Later#work", verified: false, id: 3 },
      ],
    });
    await flushPromises();
    const status = (name: string) => row(name)?.querySelector(".debug-breakpoints-dialog__function-status")?.textContent?.trim() ?? null;
    expect(status("app.Main#run")).toBeNull();
    expect(status("app.Main#nope")).toBe(t("debug.breakpoints.functionUnverifiedReason", { message: "no such method" }));
    expect(status("app.Later#work")).toBe(t("debug.breakpoints.functionUnverified"));
    // A disabled one was not sent, so it has no status.
    expect(status("app.Main#off")).toBeNull();

    // The class was loaded: the adapter verified it.
    const index = sessions.sessions.findIndex((item) => item.id === "s1");
    sessions.sessions[index] = {
      ...sessions.sessions[index]!,
      functionBreakpoints: sessions.sessions[index]!.functionBreakpoints!.map((item) => (item.id === 3 ? { ...item, verified: true } : item)),
    };
    await flushPromises();
    expect(status("app.Later#work")).toBeNull();
  });

  it("drops the typed name when the dialog closes", async () => {
    const { configs } = await open();
    await type("app.Main#draft");
    await wrapper!.setProps({ modelValue: false });
    await flushPromises();
    await wrapper!.setProps({ modelValue: true });
    await flushPromises();
    expect(nameInput().value).toBe("");
    expect(configs.stateFor(ROOT).functionBreakpoints.java).toBeUndefined();
  });
});
