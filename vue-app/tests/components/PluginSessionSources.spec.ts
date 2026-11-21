import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import type { PluginSessionMessage, PluginSessionSource } from "@/domain/plugins/session-sources";
import { useVueI18n } from "@/i18n/messages";

// The desktop bridge is stubbed: every host command is recorded and answered
// from `bridge.answer`.
const bridge = vi.hoisted(() => ({
  calls: [] as Array<[string, Record<string, unknown>]>,
  answer: (_command: string, _args: Record<string, unknown>): Promise<unknown> => Promise.resolve(undefined),
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
  invokeBackend: (command: string, args: Record<string, unknown>) => {
    bridge.calls.push([command, args]);
    return bridge.answer(command, args);
  },
}));

const { default: PluginSessionSources } = await import("@/components/sessions/PluginSessionSources.vue");
const { default: PluginSessionTranscript } = await import("@/components/sessions/PluginSessionTranscript.vue");
const { t } = useVueI18n();

const source: PluginSessionSource = {
  key: "plugin:acme.agent/team",
  pluginId: "acme.agent",
  pluginName: "Acme Agent",
  id: "team",
  title: "Team Agent",
  icon: "chat",
  mark: "TA",
  activeInWorkspace: true,
};

const listResponse = (overrides: Record<string, unknown> = {}) => ({
  sessions: [
    { id: "s1", title: "Fix the build", detail: "main", lastAt: 1_700_000_000_000, messageCount: 4 },
    { id: "s2", title: "", model: "gpt" },
  ],
  dropped: 0,
  truncated: false,
  ...overrides,
});

const listCalls = () => bridge.calls.filter(([command]) => command === "plugin_host_session_list");

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  bridge.calls = [];
  bridge.answer = () => Promise.resolve(undefined);
});

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await flushPromises();
};

function mountSources(props: Record<string, unknown> = {}) {
  wrapper = mount(PluginSessionSources, {
    props: { sources: [source], scope: "workspace", projectRoot: "/work/app", refreshToken: 0, ...props },
    attachTo: document.body,
  });
  return wrapper;
}

const toggle = (view: VueWrapper) => view.get('[data-testid="plugin-session-source-toggle"]');

describe("PluginSessionSources", () => {
  it("collapsed source calls nothing", async () => {
    bridge.answer = () => Promise.resolve(listResponse());
    const view = mountSources();
    await settle();
    expect(listCalls()).toHaveLength(0);
    await view.setProps({ refreshToken: 1 });
    await settle();
    expect(listCalls()).toHaveLength(0);
    await view.setProps({ scope: "all" });
    await settle();
    expect(listCalls()).toHaveLength(0);
    expect(view.findAll(".sessions-session-row")).toHaveLength(0);
  });

  it("expanding lists sessions once", async () => {
    bridge.answer = () => Promise.resolve(listResponse());
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    expect(listCalls()).toHaveLength(1);
    const [, args] = listCalls()[0];
    expect(args).toMatchObject({ pluginId: "acme.agent", sourceId: "team", scope: "workspace", projectRoot: "/work/app" });
    expect(toggle(view).attributes("aria-expanded")).toBe("true");
    expect(view.findAll(".sessions-session-row")).toHaveLength(2);

    await toggle(view).trigger("click");
    await settle();
    expect(toggle(view).attributes("aria-expanded")).toBe("false");
    await toggle(view).trigger("click");
    await settle();
    expect(listCalls()).toHaveLength(1);

    await view.setProps({ refreshToken: 1 });
    await settle();
    expect(listCalls()).toHaveLength(2);

    await view.get('[data-testid="plugin-session-source-refresh"]').trigger("click");
    await settle();
    expect(listCalls()).toHaveLength(3);
  });

  it("header names the plugin", async () => {
    const view = mountSources();
    await settle();
    const title = toggle(view).attributes("title") ?? "";
    expect(title).toContain("Acme Agent");
    expect(title).toContain("acme.agent");
    expect(view.get('[data-testid="plugin-session-source-badge"]').text()).toBe(t("sessions.pluginSourceBadge"));
    expect(toggle(view).text()).toContain("Team Agent");
  });

  it("rows have no terminal, rename or delete actions", async () => {
    bridge.answer = () => Promise.resolve(listResponse());
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    const rows = view.findAll(".sessions-session-row");
    expect(rows).toHaveLength(2);
    expect(view.findAll(".sessions-row-menu")).toHaveLength(0);
    expect(view.findAll(".sessions-status-dot")).toHaveLength(0);
    const text = view.text();
    for (const label of [t("sessions.openTerminal"), t("sessions.rename"), t("common.delete")]) expect(text).not.toContain(label);
    expect(rows[1].text()).toContain("s2");
    await rows[0].get("button").trigger("click");
    const opened = view.emitted("open");
    expect(opened).toHaveLength(1);
    expect(opened?.[0]).toEqual([source, expect.objectContaining({ id: "s1", title: "Fix the build" })]);
  });

  it("truncated list shows the notice", async () => {
    bridge.answer = () => Promise.resolve(listResponse({ truncated: true }));
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    expect(view.text()).toContain(t("sessions.pluginSourceTruncated"));
  });

  it("empty list shows the empty state", async () => {
    bridge.answer = () => Promise.resolve({ sessions: [], dropped: 0, truncated: false });
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    expect(view.text()).toContain(t("sessions.pluginSourceEmpty"));
  });

  it("error shows retry", async () => {
    bridge.answer = (command) =>
      command === "plugin_host_session_list" ? Promise.reject(new Error("timeout: plugin took too long")) : Promise.resolve(undefined);
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    const error = view.get('[data-testid="plugin-session-source-error"]');
    expect(error.attributes("role")).toBe("alert");
    expect(error.text()).toContain("timeout");
    bridge.answer = () => Promise.resolve(listResponse());
    await view.get('[data-testid="plugin-session-source-retry"]').trigger("click");
    await settle();
    expect(listCalls()).toHaveLength(2);
    expect(view.find('[data-testid="plugin-session-source-error"]').exists()).toBe(false);
    expect(view.findAll(".sessions-session-row")).toHaveLength(2);
  });

  it("collapsing cancels the pending list", async () => {
    let resolveList: (value: unknown) => void = () => undefined;
    bridge.answer = (command) =>
      command === "plugin_host_session_list" ? new Promise((resolve) => (resolveList = resolve)) : Promise.resolve({ cancelled: true });
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    await toggle(view).trigger("click");
    await settle();
    expect(bridge.calls.some(([command]) => command === "plugin_host_contribution_cancel")).toBe(true);
    resolveList(listResponse());
    await settle();
    bridge.answer = () => Promise.resolve(listResponse());
    await toggle(view).trigger("click");
    await settle();
    expect(listCalls()).toHaveLength(2);
    expect(view.findAll(".sessions-session-row")).toHaveLength(2);
  });

  it("header counts the sessions matching the search", async () => {
    bridge.answer = () => Promise.resolve(listResponse());
    const view = mountSources();
    await toggle(view).trigger("click");
    await settle();
    expect(toggle(view).get("small").text()).toBe("2");
    await view.setProps({ query: "build" });
    expect(toggle(view).get("small").text()).toBe("1");
    expect(view.findAll(".sessions-session-row")).toHaveLength(1);
    expect(listCalls()).toHaveLength(1);
  });

  it("lists only sources active in this workspace", async () => {
    const view = mountSources({ sources: [{ ...source, activeInWorkspace: false }] });
    await settle();
    expect(view.find('[data-testid="plugin-session-source-toggle"]').exists()).toBe(false);
  });
});

describe("PluginSessionTranscript", () => {
  it("transcript renders text, never markup", async () => {
    const text = "<img src=x onerror=alert(1)> [x](file:///etc/passwd)";
    const messages: PluginSessionMessage[] = [
      { role: "user", text, timestamp: 1_700_000_000_000 },
      { role: "assistant", text: "partial answer", model: "m1", truncated: true },
    ];
    wrapper = mount(PluginSessionTranscript, { props: { messages, truncated: false }, attachTo: document.body });
    await settle();
    expect(wrapper.findAll("img")).toHaveLength(0);
    expect(wrapper.findAll("a")).toHaveLength(0);
    const bodies = wrapper.findAll('[data-testid="plugin-session-message-text"]');
    expect(bodies).toHaveLength(2);
    expect(bodies[0].element.textContent).toBe(text);
    const articles = wrapper.findAll("article");
    expect(articles[1].text().trim().endsWith(t("sessions.messageTruncated"))).toBe(true);
    expect(articles[0].text()).not.toContain(t("sessions.messageTruncated"));
  });

  it("transcript shows the truncated and empty notices", async () => {
    wrapper = mount(PluginSessionTranscript, { props: { messages: [], truncated: true } });
    await settle();
    expect(wrapper.text()).toContain(t("sessions.noMessages"));
    expect(wrapper.text()).toContain(t("sessions.pluginMessagesTruncated"));
  });
});
