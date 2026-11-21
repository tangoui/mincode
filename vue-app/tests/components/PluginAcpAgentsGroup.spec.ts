import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PluginAcpAgent } from "@/domain/plugins/acp-agents";

const bridge = vi.hoisted(() => ({ invoke: vi.fn() }));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: bridge.invoke,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import PluginAcpAgentsGroup from "@/components/settings/PluginAcpAgentsGroup.vue";
import { pluginAcpAgents, pluginAcpAgentsReady } from "@/services/contributions/acp-agents-state";

const demo: PluginAcpAgent = {
  pluginId: "acme.agents",
  pluginName: "Acme Agents",
  agentId: "helper",
  title: "Helper Agent",
  titleLocalized: { zh: "助手智能体" },
  description: "Answers questions",
  descriptionLocalized: { zh: "回答问题" },
};
const plain: PluginAcpAgent = {
  pluginId: "acme.agents",
  pluginName: "Acme Agents",
  agentId: "plain",
  title: "Plain Agent",
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  pluginAcpAgents.value = [];
  pluginAcpAgentsReady.value = false;
  bridge.invoke.mockReset();
});

async function mountGroup(selectedId: string) {
  wrapper = mount(PluginAcpAgentsGroup, { props: { selectedId }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

describe("plugin ACP agents in settings", () => {
  it("lists plugin agents as a group with localized title, plugin name and description", async () => {
    pluginAcpAgents.value = [demo, plain];
    pluginAcpAgentsReady.value = true;
    const group = await mountGroup("claude-code");
    expect(group.text()).toContain("插件提供");
    const cards = group.findAll('[role="radio"]');
    expect(cards).toHaveLength(2);
    expect(cards[0]!.text()).toContain("助手智能体");
    expect(cards[0]!.text()).toContain("Acme Agents");
    expect(cards[0]!.text()).toContain("回答问题");
    expect(cards[1]!.text()).toContain("Plain Agent");
    expect(cards[1]!.find(".settings-ai-entry-card__desc").exists()).toBe(false);
    expect(cards.every((card) => card.attributes("aria-checked") === "false")).toBe(true);
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(false);
  });

  it("selects a plugin agent by its plugin:<pluginId>/<agentId> id without starting anything", async () => {
    pluginAcpAgents.value = [demo, plain];
    pluginAcpAgentsReady.value = true;
    const group = await mountGroup("claude-code");
    await group.findAll('[role="radio"]')[0]!.trigger("click");
    await group.findAll('[role="radio"]')[1]!.trigger("keydown", { key: "Enter" });
    expect(group.emitted("select")).toEqual([["plugin:acme.agents/helper"], ["plugin:acme.agents/plain"]]);
    await group.setProps({ selectedId: "plugin:acme.agents/helper" });
    const cards = group.findAll('[role="radio"]');
    expect(cards[0]!.attributes("aria-checked")).toBe("true");
    expect(cards[0]!.classes()).toContain("is-active");
    expect(cards[1]!.attributes("aria-checked")).toBe("false");
    expect(bridge.invoke).not.toHaveBeenCalled();
  });

  it("says the selected plugin agent is unavailable once the list is known, keeping the selection", async () => {
    pluginAcpAgents.value = [plain];
    pluginAcpAgentsReady.value = true;
    const group = await mountGroup("plugin:gone.plugin/helper");
    expect(group.get("[data-plugin-acp-unavailable]").text()).toContain("所选插件智能体当前不可用");
    expect(group.findAll('[role="radio"]')).toHaveLength(1);
    expect(group.emitted("select")).toBeUndefined();

    // Also with no plugin agents at all.
    pluginAcpAgents.value = [];
    await flushPromises();
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(true);
    expect(group.emitted("select")).toBeUndefined();
  });

  it("reads a selected id with surrounding whitespace as that agent", async () => {
    pluginAcpAgents.value = [demo, plain];
    pluginAcpAgentsReady.value = true;
    const group = await mountGroup("  plugin:acme.agents/helper\n");
    const cards = group.findAll('[role="radio"]');
    expect(cards[0]!.attributes("aria-checked")).toBe("true");
    expect(cards[0]!.classes()).toContain("is-active");
    expect(cards[0]!.find(".settings-ai-entry-card__check").exists()).toBe(true);
    expect(cards[1]!.attributes("aria-checked")).toBe("false");
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(false);

    await group.setProps({ selectedId: " plugin:gone.plugin/helper " });
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(true);
    expect(group.findAll('[role="radio"]').every((card) => card.attributes("aria-checked") === "false")).toBe(true);
  });

  it("shows nothing special while the list is not known yet", async () => {
    const group = await mountGroup("plugin:acme.agents/helper");
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(false);
    expect(group.find('[role="radio"]').exists()).toBe(false);
    expect(group.emitted("select")).toBeUndefined();

    // The list arrives and contains the agent: it shows as selected.
    pluginAcpAgents.value = [demo];
    pluginAcpAgentsReady.value = true;
    await flushPromises();
    expect(group.find("[data-plugin-acp-unavailable]").exists()).toBe(false);
    expect(group.get('[role="radio"]').attributes("aria-checked")).toBe("true");
  });

  it("does not render the group for a built-in selection without plugin agents", async () => {
    pluginAcpAgentsReady.value = true;
    const group = await mountGroup("custom:agent-1");
    expect(group.text()).toBe("");
  });
});
