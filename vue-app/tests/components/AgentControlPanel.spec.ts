import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import AgentControlPanel from "@/components/settings/AgentControlPanel.vue";

const invoke = vi.hoisted(() => vi.fn());
vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: invoke,
  isDesktopRuntime: () => true,
  nativeErrorMessage: String,
}));
vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({ t: (key: string) => key, intlLocale: { value: "en" } }),
}));

describe("AgentControlPanel", () => {
  it("loads records and calls the trust command when toggled", async () => {
    invoke.mockImplementation(async (command: string) => {
      if (command === "agent_control_status") return { trusted: false, ready: true };
      if (command === "agent_control_set_trusted") return { trusted: true, ready: true };
      if (command === "agent_control_log") {
        return [{ timestamp: "2026-11-26T10:00:00Z", source: "pane", tool: "terminal.pane_info", success: true, durationMs: 4 }];
      }
      return { state: "installed" };
    });
    const wrapper = mount(AgentControlPanel, {
      global: { stubs: {
        ElSwitch: {
          props: ["modelValue"],
          template: '<button class="trust-switch" @click="$emit(\'change\', !modelValue)">Toggle</button>',
        },
      } },
    });
    await flushPromises();
    expect(wrapper.text()).toContain("terminal.pane_info");
    await wrapper.get(".trust-switch").trigger("click");
    await flushPromises();
    expect(invoke).toHaveBeenCalledWith("agent_control_set_trusted", { enabled: true }, {});
    wrapper.unmount();
  });
});
