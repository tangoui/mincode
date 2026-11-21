import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed } from "vue";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import SshServerOverview, { type SshOverviewSession } from "@/components/ssh/SshServerOverview.vue";
import type { SshConnectionProfile } from "@/domain/ssh/types";

vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({ t: (key: string) => key, intlLocale: computed(() => "en-US") }),
}));
vi.mock("@/services/ssh-server-manager-service", () => ({
  loadSshServerSnapshot: vi.fn().mockResolvedValue({
    hostname: "veve",
    operatingSystem: "linux",
    kernel: "",
    architecture: "",
    uptimeSeconds: 0,
    load1: 0.2,
    load5: 0,
    load15: 0,
    memoryTotalBytes: 100,
    memoryUsedBytes: 48,
    diskTotalBytes: 100,
    diskUsedBytes: 81,
    disks: [],
    diskIO: [],
    diskIOTimestampMs: 1,
    cpuCores: 2,
    cpuTotal: 0,
    cpuIdle: 0,
    netIO: [],
  }),
}));

function profile(id: string, name: string, overrides: Partial<SshConnectionProfile> = {}): SshConnectionProfile {
  return {
    id,
    name,
    host: `${id}.example.com`,
    port: 22,
    username: "root",
    authType: "password",
    group: "CLAW",
    tags: [],
    favorite: false,
    saveCredentials: false,
    portForwards: [],
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  } as SshConnectionProfile;
}

const hosts = [profile("a", "geo-prod"), profile("b", "geo-test"), profile("c", "veve", { group: "Support" })];

let wrapper: VueWrapper | undefined;

function mountOverview(
  props: Partial<{
    profiles: SshConnectionProfile[];
    sessions: SshOverviewSession[];
    openedAt: Record<string, number>;
    recentClearedAt: number;
    groupOrder: string[];
  }> = {}
) {
  wrapper = mount(SshServerOverview, {
    attachTo: document.body,
    props: { profiles: hosts, sessions: [], openedAt: {}, recentClearedAt: 0, groupOrder: [], ...props },
  });
  return wrapper;
}

async function search(view: VueWrapper, text: string) {
  await view.get(".connection-overview__search input").setValue(text);
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

describe("SshServerOverview", () => {
  it("shows the empty state with a create button and no recent or in-use sections when there are no hosts", async () => {
    const view = mountOverview({ profiles: [] });
    await flushPromises();

    expect(view.find(".el-empty").exists()).toBe(true);
    expect(view.text()).toContain("ssh.manager.addFirst");
    expect(view.find(".connection-overview__recent").exists()).toBe(false);
    expect(view.find(".ssh-server-live").exists()).toBe(false);

    await view.get(".el-empty .el-button").trigger("click");
    expect(view.emitted("new-connection")).toEqual([[]]);
  });

  it("lists recently opened hosts newest first", async () => {
    const view = mountOverview({ openedAt: { b: 2, a: 1 } });
    await flushPromises();

    const names = view.findAll(".connection-overview__recent-name").map((node) => node.text());
    expect(names).toEqual(["geo-test", "geo-prod"]);

    await view.findAll(".connection-overview__recent-card")[0].trigger("click");
    expect(view.emitted("connect")?.[0]?.[0]).toMatchObject({ id: "b" });
  });

  it("connects the highlighted search result with Enter and moves the highlight with arrow keys", async () => {
    const view = mountOverview();
    await search(view, "geo");
    const input = view.get(".connection-overview__search input");

    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")?.[0]?.[0]).toMatchObject({ id: "a" });

    await input.trigger("keydown", { key: "ArrowDown" });
    expect(view.get(".connection-overview__row.is-highlighted").text()).toContain("geo-test");
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")?.[1]?.[0]).toMatchObject({ id: "b" });
  });

  it("highlights nothing until the user searches, so Enter on an idle page connects nothing", async () => {
    const view = mountOverview();
    await flushPromises();
    const input = view.get(".connection-overview__search input");

    expect(view.find(".connection-overview__row.is-highlighted").exists()).toBe(false);
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")).toBeUndefined();

    await search(view, "geo");
    expect(view.get(".connection-overview__row.is-highlighted").text()).toContain("geo-prod");

    await search(view, "");
    expect(view.find(".connection-overview__row.is-highlighted").exists()).toBe(false);
  });

  it("starts highlighting from the first row when an arrow key is pressed in the empty search box", async () => {
    const view = mountOverview();
    const input = view.get(".connection-overview__search input");

    await input.trigger("keydown", { key: "ArrowDown" });
    expect(view.get(".connection-overview__row.is-highlighted").text()).toContain("geo-prod");
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")).toEqual([[hosts[0]]]);
  });

  it("offers a new host for an unmatched user@host address and nothing for invalid ports", async () => {
    const view = mountOverview();
    await search(view, "root@10.0.0.5:2222");
    expect(view.find(".ssh-server-overview__new-for-address").exists()).toBe(true);

    await view.get(".connection-overview__search input").trigger("keydown", { key: "Enter" });
    expect(view.emitted("new-connection-for-address")).toEqual([["root@10.0.0.5:2222"]]);
    expect(view.emitted("connect")).toBeUndefined();

    await search(view, "root@host:99999");
    expect(view.find(".ssh-server-overview__new-for-address").exists()).toBe(false);
    await view.get(".connection-overview__search input").trigger("keydown", { key: "Enter" });
    expect(view.emitted("new-connection-for-address")).toHaveLength(1);
    expect(view.emitted("connect")).toBeUndefined();
  });

  it("shows in-use cards only for ready sessions", async () => {
    const ready = mountOverview({
      sessions: [{ profileId: "c", sessionId: "s1", tabId: "t1", status: "ready" }],
      openedAt: { c: Date.now() - 12 * 60_000 },
    });
    await flushPromises();
    expect(ready.get(".ssh-server-live").text()).toContain("veve");
    ready.unmount();

    const connecting = mountOverview({
      sessions: [{ profileId: "c", sessionId: "s1", tabId: "t1", status: "connecting" }],
    });
    await flushPromises();
    expect(connecting.find(".ssh-server-live").exists()).toBe(false);
  });

  it("focuses the search box on mount unless a dialog holds focus", async () => {
    const view = mountOverview();
    await flushPromises();
    expect(document.activeElement).toBe(view.get(".connection-overview__search input").element);
    view.unmount();

    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.innerHTML = `<button type="button">ok</button>`;
    document.body.append(dialog);
    const dialogButton = dialog.querySelector("button")!;
    dialogButton.focus();
    mountOverview();
    await flushPromises();
    expect(document.activeElement).toBe(dialogButton);
  });

  it("selects a row on click and connects once on double-click", async () => {
    const view = mountOverview();
    const rows = view.findAll(".connection-overview__row");

    await rows[1].trigger("click");
    expect(rows[1].classes()).toContain("is-highlighted");
    expect(view.emitted("connect")).toBeUndefined();

    await rows[1].trigger("dblclick");
    expect(view.emitted("connect")).toEqual([[hosts[1]]]);
  });

  it("renders a window of host rows for many hosts and scrolls the keyboard highlight into it", async () => {
    // One group of 500 hosts: only the rows near the viewport are mounted.
    const many = Array.from({ length: 500 }, (_, i) => profile(`h${i}`, `host-${String(i).padStart(3, "0")}`));
    const view = mountOverview({ profiles: many });
    await flushPromises();
    const wrap = view.get(".ssh-server-overview__scroll .el-scrollbar__wrap").element as HTMLElement;
    // happy-dom has no layout: a 400px viewport, 40px rows, and the group's rows starting 300px down.
    Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 400 });
    wrap.getBoundingClientRect = () => ({ top: 0, left: 0, right: 800, bottom: 400, width: 800, height: 400, x: 0, y: 0, toJSON: () => ({}) });
    const layout = () => {
      for (const row of view.findAll(".connection-overview__row")) Object.defineProperty(row.element, "offsetHeight", { configurable: true, value: 40 });
      for (const group of view.findAll("[data-row-group]")) {
        const top = 300 - wrap.scrollTop;
        group.element.getBoundingClientRect = () => ({ top, left: 0, right: 800, bottom: top, width: 800, height: 0, x: 0, y: top, toJSON: () => ({}) });
      }
    };
    layout();
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    const names = () => view.findAll(".connection-overview__row .connection-overview__name").map((node) => node.text());
    expect(names().length).toBeLessThan(150);
    expect(names()).toContain("host-000");
    expect(names()).not.toContain("host-499");

    // Walking the highlight past the window scrolls the list so the row is rendered.
    await search(view, "host-");
    const input = view.get(".connection-overview__search input");
    for (let i = 0; i < 300; i++) {
      layout();
      await input.trigger("keydown", { key: "ArrowDown" });
    }
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    expect(wrap.scrollTop).toBeGreaterThan(0);
    expect(view.get(".connection-overview__row.is-highlighted").text()).toContain("host-300");
  });

  it("orders host groups like the sidebar's custom group order, with ungrouped hosts last", async () => {
    const groupedHosts = [
      profile("a", "geo-prod"),
      profile("c", "veve", { group: "Support" }),
      profile("d", "loose", { group: "" }),
    ];
    const groupNames = (view: VueWrapper) =>
      view.findAll(".ssh-server-hosts__group").map((group) => group.attributes("aria-label"));

    const alphabetical = mountOverview({ profiles: groupedHosts });
    expect(groupNames(alphabetical)).toEqual(["CLAW", "Support", "ssh.manager.ungrouped"]);
    alphabetical.unmount();

    const custom = mountOverview({ profiles: groupedHosts, groupOrder: ["Support", "CLAW"] });
    expect(groupNames(custom)).toEqual(["Support", "CLAW", "ssh.manager.ungrouped"]);
  });
});
