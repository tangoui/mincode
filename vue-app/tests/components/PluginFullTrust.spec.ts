import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import type { VNode } from "vue";
import PluginFullTrust from "@/components/plugins/PluginFullTrust.vue";
import type { FullTrustStatus } from "@/domain/plugins/full-trust";

const mocks = vi.hoisted(() => ({
  getFullTrust: vi.fn(),
  setFullTrust: vi.fn(),
  getFullTrustAudit: vi.fn(),
  confirm: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@/services/plugin-full-trust", () => ({
  getFullTrust: mocks.getFullTrust,
  setFullTrust: mocks.setFullTrust,
  getFullTrustAudit: mocks.getFullTrustAudit,
  clearFullTrustAudit: vi.fn(),
  fullTrustReasonText: (reason: string) => reason,
}));

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  return {
    ...actual,
    ElMessageBox: { confirm: mocks.confirm },
    ElMessage: { warning: mocks.warning, error: mocks.error, success: vi.fn() },
  };
});

const fingerprint = "ab".repeat(32);

function status(overrides: Partial<FullTrustStatus> = {}, subject: Partial<FullTrustStatus["subject"]> = {}): FullTrustStatus {
  return {
    pluginId: "p.x",
    trusted: false,
    legacyDangerous: false,
    subject: { pluginId: "p.x", name: "Demo <b>", version: "1.2.3", capabilities: ["plugins"], fingerprint, eligible: true, ...subject },
    ...overrides,
  };
}

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  mocks.getFullTrust.mockResolvedValue(status());
  mocks.getFullTrustAudit.mockResolvedValue([]);
  mocks.setFullTrust.mockImplementation(async (_id: string, trusted: boolean) => status({ trusted, ...(trusted ? { grantedAt: 1 } : {}) }));
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  for (const mock of Object.values(mocks)) mock.mockReset();
});

async function open(pluginName = "Demo <b>") {
  wrapper = mount(PluginFullTrust, {
    props: { pluginId: "p.x", pluginName, pluginVersion: "1.2.3" },
    attachTo: document.body,
  });
  await flushPromises();
  const button = wrapper.find("button.plugin-full-trust__button");
  expect(button.exists()).toBe(true);
  await button.trigger("click");
  await flushPromises();
  await settlePopover();
}

/** The panel renders only once the popover opens (persistent=false), after its open timer. */
async function settlePopover() {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await flushPromises();
}

function switchEl() {
  const el = document.body.querySelector<HTMLElement>(".plugin-full-trust__panel .el-switch");
  expect(el).not.toBeNull();
  return el!;
}

function switchOn() {
  return switchEl().classList.contains("is-checked");
}

async function clickSwitch() {
  switchEl().click();
  await flushPromises();
}

/** Text of the VNode message passed to ElMessageBox.confirm. */
function vnodeText(node: unknown): string {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(vnodeText).join("");
  return vnodeText((node as VNode).children);
}

/** One string per <p> of the warning. */
function paragraphs(message: unknown): string[] {
  return ((message as VNode).children as VNode[]).map(vnodeText);
}

describe("PluginFullTrust", () => {
  it("enabling requires the warning", async () => {
    mocks.confirm.mockResolvedValue("confirm");
    await open();
    expect(switchOn()).toBe(false);
    await clickSwitch();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    const [message, title, options] = mocks.confirm.mock.calls[0];
    const body = paragraphs(message).join("\n");
    expect(paragraphs(message)).toHaveLength(4);
    expect(title).toContain("Demo <b>");
    expect(body).toContain("Demo <b>");
    expect(body).toContain("p.x");
    expect(body).toContain("1.2.3");
    expect(body).toContain("终端里运行命令");
    expect(options.confirmButtonClass).toContain("el-button--danger");
    expect(options.autofocus).toBe(false);
    expect(options.closeOnClickModal).toBe(false);
    expect(options.type).toBe("warning");
    expect(options.dangerouslyUseHTMLString).toBeUndefined();
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", true, fingerprint);
    expect(switchOn()).toBe(true);
    expect(wrapper!.emitted("changed")).toEqual([[true]]);
  });

  it("mounting and opening never ask Go to hash the package; enabling does", async () => {
    mocks.getFullTrust.mockResolvedValue(status({}, { fingerprint: "" }));
    await open();
    expect(mocks.getFullTrust.mock.calls.length).toBeGreaterThanOrEqual(1);
    for (const call of mocks.getFullTrust.mock.calls) expect(call).toEqual(["p.x"]);
    mocks.getFullTrust.mockClear();
    mocks.getFullTrust.mockResolvedValue(status());
    mocks.confirm.mockResolvedValue("confirm");
    await clickSwitch();
    expect(mocks.getFullTrust.mock.calls).toEqual([["p.x", { full: true }]]);
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", true, fingerprint);
  });

  it("refuses to enable without a fingerprint", async () => {
    mocks.getFullTrust.mockResolvedValue(status({}, { fingerprint: "" }));
    await open();
    await clickSwitch();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.setFullTrust).not.toHaveBeenCalled();
    expect(mocks.warning).toHaveBeenCalled();
  });

  it("a plugin name cannot add paragraphs to the warning", async () => {
    const name = "X\n\nNothing is bypassed\u0007";
    mocks.getFullTrust.mockResolvedValue(status({}, { name }));
    mocks.confirm.mockRejectedValue("cancel");
    await open(name);
    await clickSwitch();
    const [message, title] = mocks.confirm.mock.calls[0];
    const lines = paragraphs(message);
    expect(lines).toHaveLength(4);
    expect(lines).not.toContain("Nothing is bypassed");
    expect(lines[3]).toContain("X Nothing is bypassed");
    for (const text of [lines.join(""), String(title)]) {
      expect(text).not.toContain("\n");
      expect(text).not.toContain("\u0007");
    }
  });

  it("bidi controls are stripped and every value is isolated", async () => {
    // A name that would flip the rest of the line, on a plugin whose ID imitates an official one.
    const name = "Backup\u202E";
    const id = "0fficial.data-backup";
    mocks.getFullTrust.mockResolvedValue(status({ pluginId: id }, { pluginId: id, name: `${name}\u200B\u2066` }));
    mocks.confirm.mockRejectedValue("cancel");
    wrapper = mount(PluginFullTrust, { props: { pluginId: id, pluginName: `${name}\u200B\u2066`, pluginVersion: "1.2.3" }, attachTo: document.body });
    await flushPromises();
    await wrapper.find("button.plugin-full-trust__button").trigger("click");
    await settlePopover();
    await clickSwitch();
    const [message, title] = mocks.confirm.mock.calls[0];
    const cf = /\p{Cf}/u;
    expect(paragraphs(message).join("")).not.toMatch(cf);
    expect(String(title)).not.toMatch(cf);
    expect(paragraphs(message)[3]).toContain("Backup（0fficial.data-backup）v1.2.3");
    const values = ((message as VNode).children as VNode[]).flatMap((p) => (p.children as unknown[]).filter((c): c is VNode => typeof c === "object"));
    expect(values.map((v) => v.type)).toEqual(["bdi", "bdi", "bdi"]);
    expect(values.every((v) => (v.props as { class?: string }).class === "plugin-full-trust__value")).toBe(true);
  });

  it("refuses when what Go would fingerprint differs from the card", async () => {
    mocks.getFullTrust.mockResolvedValue(status({}, { version: "2.0.0" }));
    await open();
    await clickSwitch();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.setFullTrust).not.toHaveBeenCalled();
    expect(mocks.warning).toHaveBeenCalledWith("插件在你确认期间发生了变化，请重新查看后再开启");
    expect(switchOn()).toBe(false);
  });

  it("warns with the subject Go returned", async () => {
    mocks.confirm.mockResolvedValue("confirm");
    // The card's list entry and Go agree; the warning text comes from Go's subject.
    mocks.getFullTrust.mockResolvedValue(status({}, { name: "Demo <b>  ", version: "1.2.3" }));
    await open();
    await clickSwitch();
    expect(paragraphs(mocks.confirm.mock.calls[0][0])[3]).toContain("Demo <b>（p.x）v1.2.3");
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", true, fingerprint);
  });

  it("cancel keeps it off", async () => {
    mocks.confirm.mockRejectedValue("cancel");
    await open();
    await clickSwitch();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.setFullTrust).not.toHaveBeenCalled();
    expect(switchOn()).toBe(false);
  });

  it("disabling is immediate", async () => {
    mocks.getFullTrust.mockResolvedValue(status({ trusted: true, grantedAt: 5 }));
    await open();
    expect(switchOn()).toBe(true);
    await clickSwitch();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", false);
    expect(switchOn()).toBe(false);
    expect(wrapper!.emitted("changed")).toEqual([[false]]);
  });

  it("changed fingerprint asks again", async () => {
    mocks.confirm.mockResolvedValue("confirm");
    mocks.setFullTrust.mockRejectedValue(new Error("changed: the plugin changed since it was reviewed; review it again"));
    await open();
    await clickSwitch();
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", true, fingerprint);
    expect(mocks.warning).toHaveBeenCalledWith("插件在你确认期间发生了变化，请重新查看后再开启");
    expect(switchOn()).toBe(false);
  });

  it("locked refusal names the lock", async () => {
    mocks.confirm.mockResolvedValue("confirm");
    mocks.setFullTrust.mockRejectedValue(new Error("locked: unlock MinCode before trusting a plugin"));
    await open();
    await clickSwitch();
    expect(mocks.warning).toHaveBeenCalledTimes(1);
    expect(String(mocks.warning.mock.calls[0][0])).toContain("锁定");
    expect(switchOn()).toBe(false);
  });

  it("legacy row", async () => {
    mocks.getFullTrust.mockResolvedValue(status({ legacyDangerous: true }));
    await open();
    const panel = document.body.querySelector(".plugin-full-trust__panel")!;
    expect(panel.textContent).toContain("旧版：已信任此版本的危险操作");
    const revoke = panel.querySelector<HTMLButtonElement>(".plugin-full-trust__legacy button");
    expect(revoke).not.toBeNull();
    revoke!.click();
    await flushPromises();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", false);
    expect(document.body.querySelector(".plugin-full-trust__legacy")).toBeNull();
  });

  it("not eligible", async () => {
    mocks.getFullTrust.mockResolvedValue(status({}, { eligible: false, reason: "development" }));
    await open();
    expect(switchEl().classList.contains("is-disabled")).toBe(true);
    expect(document.body.querySelector(".plugin-full-trust__reason")?.textContent).toContain("开发");
    await clickSwitch();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.setFullTrust).not.toHaveBeenCalled();
  });


  it("revokes legacy trust on a disabled plugin but cannot grant", async () => {
    mocks.getFullTrust.mockResolvedValue(status({ legacyDangerous: true }, { eligible: false, reason: "disabled" }));
    await open();
    expect(switchEl().classList.contains("is-disabled")).toBe(true);
    document.body.querySelector<HTMLButtonElement>(".plugin-full-trust__legacy button")!.click();
    await flushPromises();
    expect(mocks.setFullTrust).toHaveBeenCalledWith("p.x", false);
  });

  it("with the trust state from the plugin list it asks Go only when opened", async () => {
    // The plugin manager passes plugin.fullTrust so a page of cards does not query Go once per card.
    mocks.getFullTrust.mockResolvedValue(status({ trusted: true, grantedAt: 5 }));
    wrapper = mount(PluginFullTrust, {
      props: { pluginId: "p.x", pluginName: "Demo <b>", pluginVersion: "1.2.3", trusted: true },
      attachTo: document.body,
    });
    await flushPromises();
    const button = wrapper.find("button.plugin-full-trust__button");
    expect(button.exists()).toBe(true);
    expect(button.classes()).toContain("is-trusted");
    expect(mocks.getFullTrust).not.toHaveBeenCalled();
    await button.trigger("click");
    await settlePopover();
    expect(mocks.getFullTrust).toHaveBeenCalledWith("p.x");
    expect(switchOn()).toBe(true);
  });

  it("renders the panel only once opened", async () => {
    // A page of 24 plugin cards each kept a hidden panel (switch, texts, buttons) in the document.
    wrapper = mount(PluginFullTrust, {
      props: { pluginId: "p.x", pluginName: "Demo <b>", pluginVersion: "1.2.3", trusted: false },
      attachTo: document.body,
    });
    await flushPromises();
    expect(document.body.querySelector(".plugin-full-trust__panel")).toBeNull();
    await wrapper.find("button.plugin-full-trust__button").trigger("click");
    await settlePopover();
    expect(document.body.querySelector(".plugin-full-trust__panel")).not.toBeNull();
  });

  it("says so when that first read fails", async () => {
    mocks.getFullTrust.mockRejectedValue(new Error("boom"));
    wrapper = mount(PluginFullTrust, {
      props: { pluginId: "p.x", pluginName: "Demo <b>", pluginVersion: "1.2.3", trusted: false },
      attachTo: document.body,
    });
    await flushPromises();
    await wrapper.find("button.plugin-full-trust__button").trigger("click");
    await settlePopover();
    expect(document.body.querySelector("[role=alert]")?.textContent).toContain("无法读取完全信任状态");
  });

  it("announces the panel as a dialog", async () => {
    await open();
    expect(wrapper!.find("button.plugin-full-trust__button").attributes("aria-haspopup")).toBe("dialog");
  });
});
