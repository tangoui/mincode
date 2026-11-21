import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";

vi.mock("@/services/settings-persistence", () => ({ readSettingsSnapshot: () => ({}) }));
vi.mock("@/services/notifications", () => ({ notifyTerminalAttention: vi.fn() }));

import TerminalPaneAttention from "@/components/terminal/TerminalPaneAttention.vue";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";

describe("TerminalPaneAttention", () => {
  let pinia: ReturnType<typeof createPinia>;
  beforeEach(() => {
    pinia = createPinia();
    setActivePinia(pinia);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  });
  afterEach(() => vi.useRealTimers());

  it("spells a new attention out on its own pane, then shrinks to a short label; a click acknowledges", async () => {
    const store = useTerminalAttentionStore();
    store.configure({ owns: () => true, describe: () => "" });
    const wrapper = mount(TerminalPaneAttention, { props: { paneId: "a" }, global: { plugins: [pinia] } });
    expect(wrapper.find(".terminal-pane-attention").exists()).toBe(false);

    store.report({ paneId: "b", reason: "completed", at: 1 });
    await nextTick();
    expect(wrapper.find(".terminal-pane-attention").exists()).toBe(false);

    store.report({ paneId: "a", reason: "waiting_approval", message: "rm -rf build", at: 2 });
    await nextTick();
    const chip = wrapper.find(".terminal-pane-attention");
    expect(chip.classes()).toEqual(expect.arrayContaining(["is-urgent", "is-spelled-out"]));
    expect(chip.text()).toBe("等待你授权 · rm -rf build");

    vi.advanceTimersByTime(6_000);
    await nextTick();
    expect(wrapper.find(".terminal-pane-attention").text()).toBe("等授权");

    await wrapper.find(".terminal-pane-attention").trigger("click");
    expect(store.items.has("a")).toBe(false);
    expect(store.items.has("b")).toBe(true);
    await nextTick();
    expect(wrapper.find(".terminal-pane-attention").exists()).toBe(false);
  });
});
