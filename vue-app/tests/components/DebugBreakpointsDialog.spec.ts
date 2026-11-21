import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import DebugBreakpointsDialog from "@/components/debug/DebugBreakpointsDialog.vue";
import type { DebuggerInfo, RunConfiguration } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { messagesEn } from "@/i18n/locales/en";
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

const java: DebuggerInfo = {
  pluginId: "java-debug",
  pluginName: "Java Debugger",
  type: "java",
  title: "Java (java-debug)",
  languages: ["java"],
  configurationSnippets: [],
  hasEntryPoints: true,
  hasCheck: true,
  hasSettings: true,
  exceptionTypes: {
    placeholder: "One class per line, e.g. java.lang.NullPointerException",
    placeholderLocalized: { zh: "每行一个类，例如 java.lang.NullPointerException" },
    description: "Pause only on these types.",
    descriptionLocalized: { zh: "只在这些类型处暂停。" },
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

beforeEach(() => {
  // tests/components/setup.ts installs a fresh Pinia for each test.
  service.listDebuggers.mockReset().mockResolvedValue([java, python]);
  service.warning.mockReset();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});

async function open() {
  const configs = useDebugConfigsStore();
  await configs.load();
  configs.upsertConfig(ROOT, config(java));
  configs.upsertConfig(ROOT, config(python));
  configs.setExceptionFilters(ROOT, "python", ["raised"]);
  const sessions = useDebugSessionsStore();
  const sync = vi.fn().mockResolvedValue(undefined);
  sessions.syncExceptionTypes = sync;
  wrapper = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
  await flushPromises();
  return { configs, sync };
}

function typesInput(): HTMLTextAreaElement {
  const inputs = document.querySelectorAll<HTMLTextAreaElement>(".debug-breakpoints-dialog__types textarea");
  expect(inputs).toHaveLength(1);
  return inputs[0]!;
}

async function enter(text: string) {
  const input = typesInput();
  input.value = text;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
}

function typesText(): string {
  return document.querySelector(".debug-breakpoints-dialog__types")?.textContent ?? "";
}

/** ElFormItem shows and hides its error after a short debounce. */
async function expectError(message: string | null) {
  await vi.waitFor(() => {
    const error = document.querySelector(".debug-breakpoints-dialog__types .el-form-item__error");
    expect(error?.textContent?.trim() ?? null).toBe(message);
  });
}

/** The list is saved when the input loses focus. */
async function save() {
  typesInput().dispatchEvent(new FocusEvent("blur"));
  await flushPromises();
}

async function close() {
  await wrapper!.setProps({ modelValue: false });
  await flushPromises();
}

describe("DebugBreakpointsDialog section titles", () => {
  it("are in sentence case in English, like the other section titles", () => {
    expect([messagesEn["debug.breakpoints.exceptions"], messagesEn["debug.breakpoints.functions"], messagesEn["debug.breakpoints.dataTitle"]]).toEqual([
      "Exception breakpoints",
      "Method breakpoints",
      "Field breakpoints",
    ]);
  });
});

describe("DebugBreakpointsDialog exception types", () => {
  it("shows the list only for debuggers that declare it, with their localized texts", async () => {
    await open();
    const input = typesInput();
    expect(input.closest("[data-debugger-type]")?.getAttribute("data-debugger-type")).toBe("java");
    expect(input.placeholder).toBe("每行一个类，例如 java.lang.NullPointerException");
    expect(typesText()).toContain("只在这些类型处暂停。");
    // The Python group keeps its switches and gets no list.
    expect(document.querySelector("[data-debugger-type='python'] textarea")).toBeNull();
    expect(document.querySelector("[data-debugger-type='python']")?.textContent).toContain("raised");
  });

  it("shows nothing extra when the debugger listing fails", async () => {
    service.listDebuggers.mockRejectedValue(new Error("unavailable: debugging needs the MinCode desktop app"));
    await open();
    expect(document.querySelector(".debug-breakpoints-dialog__types")).toBeNull();
  });

  it("trims and de-duplicates before saving, then re-syncs running sessions", async () => {
    const { configs, sync } = await open();
    await enter("  java.lang.NullPointerException \n\n   \njava.lang.NullPointerException\nNPE");
    await save();
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["java.lang.NullPointerException", "NPE"]);
    expect(sync).toHaveBeenCalledExactlyOnceWith(ROOT, java);
    expect(typesInput().value).toBe("java.lang.NullPointerException\nNPE");

    // Clearing saves an empty list (sent as [] so the debugger drops the filter).
    await enter("  ");
    await save();
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual([]);
    expect(sync).toHaveBeenCalledTimes(2);
  });

  it("refuses an item that is not a class name and saves nothing", async () => {
    const { configs, sync } = await open();
    configs.setExceptionTypes(ROOT, "java", ["app.Kept"]);
    await enter("java.lang.NullPointerException\njava.lang.");
    await save();
    const { t } = useVueI18n();
    await expectError(t("debug.breakpoints.exceptionTypeInvalid", { value: "java.lang." }));
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["app.Kept"]);
    expect(sync).not.toHaveBeenCalled();

    // Fixing the input clears the message.
    await enter("java.lang.NullPointerException");
    await save();
    await expectError(null);
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["java.lang.NullPointerException"]);
  });

  it("reports too long and too many items", async () => {
    const { configs } = await open();
    const { t } = useVueI18n();
    await enter(`a.${"b".repeat(300)}`);
    await save();
    await expectError(t("debug.breakpoints.exceptionTypeTooLong", { max: 256 }));
    await enter(Array.from({ length: 65 }, (_, i) => `x.E${i}`).join("\n"));
    await save();
    await expectError(t("debug.breakpoints.exceptionTypesTooMany", { max: 64 }));
    expect(configs.stateFor(ROOT).exceptionTypes.java).toBeUndefined();
  });

  it("saves on leaving the field, without a save button, and hints only the format", async () => {
    const { configs, sync } = await open();
    expect(document.querySelector(".debug-breakpoints-dialog__types-save")).toBeNull();
    const { t } = useVueI18n();
    expect(typesText()).toContain(t("debug.breakpoints.exceptionTypesHint"));
    expect(t("debug.breakpoints.exceptionTypesHint")).toBe("每行一个（也可用逗号分隔）。");
    await enter("app.Boom");
    expect(configs.stateFor(ROOT).exceptionTypes.java).toBeUndefined();
    await save();
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["app.Boom"]);
    expect(sync).toHaveBeenCalledOnce();
    // Leaving the field again without a change does not sync again.
    await save();
    expect(sync).toHaveBeenCalledOnce();
  });

  it("saves a valid list typed just before the dialog closes", async () => {
    const { configs, sync } = await open();
    await enter("app.Boom");
    await close();
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["app.Boom"]);
    expect(sync).toHaveBeenCalledExactlyOnceWith(ROOT, java);
    expect(service.warning).not.toHaveBeenCalled();
  });

  it("warns when the dialog closes with a list it cannot save", async () => {
    const { configs, sync } = await open();
    configs.setExceptionTypes(ROOT, "java", ["app.Kept"]);
    await enter("java.lang.");
    await close();
    expect(configs.stateFor(ROOT).exceptionTypes.java).toEqual(["app.Kept"]);
    expect(sync).not.toHaveBeenCalled();
    const { t } = useVueI18n();
    expect(service.warning).toHaveBeenCalledOnce();
    expect(String(service.warning.mock.calls[0]![0].message)).toBe(
      t("debug.breakpoints.exceptionTypesNotSaved", { error: t("debug.breakpoints.exceptionTypeInvalid", { value: "java.lang." }) })
    );
  });

  it("shows the saved list", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionTypes(ROOT, "java", ["java.lang.IllegalStateException", "java.lang.NullPointerException"]);
    await open();
    expect(typesInput().value).toBe("java.lang.IllegalStateException\njava.lang.NullPointerException");
  });
});
