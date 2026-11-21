import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { installPluginHostUiBridge } from "@/services/plugin-host-ui";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";

const bridge = vi.hoisted(() => ({
  invoke: vi.fn(),
  handlers: new Map<string, (payload: unknown) => void>(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: (...args: unknown[]) => bridge.invoke(...args),
  isDesktopRuntime: () => true,
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    bridge.handlers.set(name, handler);
    return () => bridge.handlers.delete(name);
  },
}));
vi.mock("@/router", () => ({ default: { push: vi.fn() } }));

const pick = {
  pluginId: "notes",
  pluginName: "Notes",
  id: "prompt-1-1",
  kind: "quickPick",
  title: "Profile",
  items: [{ id: "dev", label: "Dev" }],
};

function emit(name: string, payload: unknown) {
  bridge.handlers.get(name)?.(payload);
}

let uninstall: (() => void) | undefined;

beforeEach(() => {
  setActivePinia(createPinia());
  bridge.invoke.mockReset();
  bridge.invoke.mockResolvedValue(undefined);
  uninstall = installPluginHostUiBridge();
});

afterEach(() => {
  uninstall?.();
  bridge.handlers.clear();
});

describe("plugin host prompts (parser loaded on demand)", () => {
  it("shows a prompt once the parser has loaded", async () => {
    emit("plugin:prompt", pick);
    await flushPromises();
    await vi.dynamicImportSettled();
    expect(usePluginHostUiStore().prompts.map((prompt) => prompt.id)).toEqual(["prompt-1-1"]);
  });

  it("answers a prompt it cannot parse as cancelled", async () => {
    emit("plugin:prompt", { ...pick, items: [] });
    await vi.dynamicImportSettled();
    await flushPromises();
    expect(usePluginHostUiStore().prompts).toEqual([]);
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_prompt_reply", { id: "prompt-1-1", reply: { cancelled: true } });
  });

  it("drops a prompt cancelled while it was being parsed", async () => {
    emit("plugin:prompt", pick);
    emit("plugin:prompt-cancel", { id: "prompt-1-1" });
    await vi.dynamicImportSettled();
    await flushPromises();
    expect(usePluginHostUiStore().prompts).toEqual([]);
    expect(bridge.invoke).not.toHaveBeenCalled();
  });
});
