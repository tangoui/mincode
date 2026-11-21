import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElMessageBox } from "element-plus";
import type { DebuggerInfo, RunConfiguration } from "@/domain/debug/types";

const bridge = vi.hoisted(() => ({
  invokeBackend: vi.fn(),
  copyText: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: bridge.invokeBackend,
  copyText: bridge.copyText,
  listenAppEvent: vi.fn(() => () => undefined),
}));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: vi.fn(),
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import RunConfigDialog from "@/components/debug/RunConfigDialog.vue";
import { listDebuggers } from "@/services/debug-service";
import { savePersistedValue } from "@/services/persistent-store";
import { useDebugConfigsStore } from "@/stores/debug-configs";

const ROOT = "/Users/A/Proj";
const manifest = JSON.parse(readFileSync(resolve(process.cwd(), "../official-plugins/python-debug/plugin.json"), "utf8"));
const python: DebuggerInfo = {
  ...manifest.contributes.debuggers[0],
  pluginId: "mincode.python-debug",
  pluginName: "Python Debug",
  hasEntryPoints: true,
  hasCheck: true,
};

let wrapper: ReturnType<typeof mount> | undefined;

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.clearAllMocks();
});

async function mountDialog(mode: "edit" | "new", debuggers: DebuggerInfo[] = [python]) {
  vi.mocked(listDebuggers).mockResolvedValue(debuggers);
  wrapper = mount(RunConfigDialog, { props: { modelValue: true, projectRoot: ROOT, mode }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

async function storeWith(cfg: RunConfiguration) {
  const configs = useDebugConfigsStore();
  await configs.load();
  configs.upsertConfig(ROOT, cfg);
  configs.selectConfig(ROOT, cfg.id);
  return configs;
}

function query<T extends Element = HTMLElement>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) throw new Error(`missing ${selector}`);
  return element;
}

async function type(selector: string, value: string) {
  const input = query<HTMLInputElement | HTMLTextAreaElement>(`${selector} input, ${selector} textarea`);
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
}

async function click(selector: string) {
  query<HTMLElement>(selector).click();
  await flushPromises();
}

const attachCfg: RunConfiguration = {
  id: "cfg-attach",
  name: "Server",
  pluginId: python.pluginId,
  type: "python",
  request: "attach",
  fields: { host: "localhost", port: 6000 },
};

describe("RunConfigDialog", () => {
  it("scrolls the configuration fields with Element Plus while keeping the list and footer outside", async () => {
    const style = document.createElement("style");
    style.textContent = readFileSync(resolve(process.cwd(), "src/styles/workspaces/debug/tool-window.css"), "utf8");
    document.head.append(style);
    try {
      await storeWith({ ...attachCfg, request: "launch", fields: { program: "${file}", env: { MODE: "test" } } });
      await mountDialog("edit");
      const editor = query(".run-config-dialog__editor");
      expect(editor.classList.contains("el-scrollbar")).toBe(true);
      expect(editor.querySelector(".el-scrollbar__wrap [data-field='program']")).not.toBeNull();
      expect(editor.querySelector(".el-scrollbar__wrap [data-field='env']")).not.toBeNull();
      expect(getComputedStyle(editor).minHeight).toMatch(/^0(?:px)?$/);
      expect(getComputedStyle(editor).overflowY).not.toBe("auto");
      expect(editor.contains(query(".run-config-dialog__list"))).toBe(false);
      expect(editor.contains(query(".run-config-dialog .el-dialog__footer"))).toBe(false);
      expect(query<HTMLButtonElement>("[data-action='save-close']").disabled).toBe(false);
    } finally {
      style.remove();
    }
  });

  it("creates configuration from a snippet", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    await mountDialog("new");
    expect(document.querySelector('[data-snippet="2"]')?.textContent).toContain("附加");

    await click('[data-snippet="2"] input');
    await click('[data-action="create"]');

    const created = configs.stateFor(ROOT).configs;
    expect(created).toHaveLength(1);
    expect(created[0]).toMatchObject({ name: "附加", pluginId: python.pluginId, type: "python", request: "attach", fields: { host: "localhost", port: 5678 } });
    expect(configs.stateFor(ROOT).selectedConfigId).toBe(created[0].id);
    // The new configuration opens in the editor.
    expect(query<HTMLInputElement>('[data-field="name"] input').value).toBe("附加");
    expect(document.querySelector('[data-field="port"]')).toBeTruthy();
  });

  it("remote args template substitutes port", async () => {
    await storeWith(attachCfg);
    await mountDialog("edit");
    const args = () => query<HTMLInputElement>('[data-field="remote-args"] input').value;
    expect(args()).toBe("python -m debugpy --listen 0.0.0.0:6000 --wait-for-client <script.py>");
    expect(document.querySelector(".run-config-dialog__remote")?.textContent).toContain("远程 SSH 附加将在后续版本提供");

    await type('[data-field="port"]', "7001");
    expect(args()).toContain("0.0.0.0:7001");
    await click('[data-action="copy-remote-args"]');
    expect(bridge.copyText).toHaveBeenCalledWith("python -m debugpy --listen 0.0.0.0:7001 --wait-for-client <script.py>");
  });

  it("hides the server arguments without an args template", async () => {
    await storeWith(attachCfg);
    const { argsTemplate: _omitted, ...remote } = python.remote!;
    await mountDialog("edit", [{ ...python, remote: remote as DebuggerInfo["remote"] }]);
    expect(document.querySelector('[data-field="remote-args"]')).toBeNull();
    expect(document.querySelector('[data-field="port"]')).toBeTruthy();
  });

  it("save does not call any filesystem command", async () => {
    const configs = await storeWith({ ...attachCfg, id: "cfg-launch", name: "App", request: "launch", fields: { program: "${file}" } });
    await mountDialog("edit");

    await type('[data-field="args"]', "--x\n--y");
    await type('[data-field="env"]', "A=1");
    await type('[data-field="name"]', "App 2");
    await click('[data-action="save"]');

    expect(configs.stateFor(ROOT).configs[0]).toMatchObject({ id: "cfg-launch", name: "App 2", fields: { program: "${file}", args: ["--x", "--y"], env: { A: "1" } } });
    await configs.flush();
    expect(savePersistedValue).toHaveBeenCalled();
    expect(bridge.invokeBackend).not.toHaveBeenCalled();
  });

  it("blocks saving invalid fields", async () => {
    const configs = await storeWith({ ...attachCfg, fields: { port: 6000 } });
    await mountDialog("edit");
    await type('[data-field="pathMappings"]', "[{");
    expect(query('[data-field="pathMappings"]').classList.contains("is-error")).toBe(false);
    query('[data-field="pathMappings"] textarea').dispatchEvent(new Event("blur"));
    await flushPromises();
    expect(query('[data-field="pathMappings"]').classList.contains("is-error")).toBe(true);
    await type('[data-field="port"]', "x");
    await click('[data-action="save"]');
    // Test utils stub the message transition; the item's error state shows.
    expect(query('[data-field="pathMappings"]').classList.contains("is-error")).toBe(true);
    expect(query('[data-field="port"]').classList.contains("is-error")).toBe(true);
    expect(configs.stateFor(ROOT).configs[0].fields).toEqual({ port: 6000 });
  });

  it("shows an error when debuggers cannot be listed", async () => {
    vi.mocked(listDebuggers).mockRejectedValue(new Error("boom"));
    wrapper = mount(RunConfigDialog, { props: { modelValue: true, projectRoot: ROOT, mode: "new" }, attachTo: document.body });
    await flushPromises();
    expect(document.querySelector(".run-config-dialog")?.textContent).toContain("boom");
    expect(document.querySelector('[data-action="retry"]')).toBeTruthy();
  });
  it("refuses to switch request type while a field is invalid", async () => {
    await storeWith(attachCfg);
    await mountDialog("edit");
    await type('[data-field="port"]', "x");
    await click('[data-request="launch"] input');
    // Still attach, the edit is kept and the error shows.
    expect(document.querySelector('[data-field="program"]')).toBeNull();
    expect(query<HTMLInputElement>('[data-field="port"] input').value).toBe("x");
    expect(query('[data-field="port"]').classList.contains("is-error")).toBe(true);
  });

  it("keeps the edited configuration highlighted when discarding is cancelled", async () => {
    const configs = await storeWith(attachCfg);
    configs.upsertConfig(ROOT, { ...attachCfg, id: "cfg-b", name: "Other" });
    await mountDialog("edit");
    await type('[data-field="name"]', "Server 2");
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockRejectedValue("cancel");
    const items = () => [...document.querySelectorAll<HTMLElement>(".run-config-dialog__menu .el-menu-item")];
    items()[1]!.click();
    await flushPromises();
    expect(confirm).toHaveBeenCalled();
    expect(items()[0]!.classList.contains("is-active")).toBe(true);
    expect(items()[1]!.classList.contains("is-active")).toBe(false);
    expect(query<HTMLInputElement>('[data-field="name"] input').value).toBe("Server 2");

    await click('[data-action="add"]');
    expect(items()[0]!.classList.contains("is-active")).toBe(true);
    expect(document.querySelector('[data-action="create"]')).toBeNull();

    // Once Add goes ahead, no configuration stays highlighted.
    confirm.mockResolvedValue("confirm" as never);
    await click('[data-action="add"]');
    expect(document.querySelector('[data-action="create"]')).toBeTruthy();
    expect(items().some((item) => item.classList.contains("is-active"))).toBe(false);
  });

  it("server arguments fall back to the default port when the port is invalid", async () => {
    await storeWith(attachCfg);
    await mountDialog("edit");
    await type('[data-field="port"]', "70000");
    expect(query<HTMLInputElement>('[data-field="remote-args"] input').value).toContain("0.0.0.0:5678 ");
  });

  it("OK closes without saving when nothing changed", async () => {
    const configs = await storeWith(attachCfg);
    await mountDialog("edit");
    const upsert = vi.spyOn(configs, "upsertConfig");
    await click('[data-action="save-close"]');
    expect(upsert).not.toHaveBeenCalled();
    expect(wrapper!.emitted("update:modelValue")).toEqual([[false]]);
  });

  it("switching launch and attach restores the other request's fields; only the final ones are saved", async () => {
    const configs = await storeWith({ ...attachCfg, request: "launch", fields: { program: "app.py", args: ["-v"], justMyCode: false } });
    await mountDialog("edit");
    await click('[data-request="attach"] input');
    expect(document.querySelector('[data-field="program"]')).toBeNull();
    await type('[data-field="port"]', "7000");
    await click('[data-request="launch"] input');
    expect(query<HTMLInputElement>('[data-field="program"] input').value).toBe("app.py");
    expect(query<HTMLTextAreaElement>('[data-field="args"] textarea').value).toBe("-v");
    await click('[data-request="attach"] input');
    expect(query<HTMLInputElement>('[data-field="port"] input').value).toBe("7000");
    await click('[data-action="save"]');
    expect(configs.stateFor(ROOT).configs[0]).toMatchObject({ request: "attach", fields: { port: 7000, justMyCode: false } });
    expect(configs.stateFor(ROOT).configs[0].fields).not.toHaveProperty("program");
  });

  it("edits a list that cannot be shown one item per line as JSON", async () => {
    await storeWith({ ...attachCfg, request: "launch", fields: { args: ["", "a"] } });
    await mountDialog("edit");
    expect(query('[data-field="args"]').dataset.kind).toBe("json");
  });
});
