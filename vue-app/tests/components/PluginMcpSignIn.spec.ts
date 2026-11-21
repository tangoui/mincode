import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const native = vi.hoisted(() => ({
  copyText: vi.fn(async (_text: string) => {}),
  openSystemBrowser: vi.fn(async (_url: string) => {}),
}));

vi.mock("@/services/desktop-bridge", () => ({
  copyText: native.copyText,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));
vi.mock("@/services/web-automation-service", () => ({ openSystemBrowser: native.openSystemBrowser }));
vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
  }),
}));

const { default: PluginMcpSignIn } = await import("@/components/plugins/PluginMcpSignIn.vue");

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.clearAllMocks();
});

async function open(
  deviceCode: { userCode: string; verificationUri: string } | null,
  phase: "confirm" | "signing-in" = "signing-in",
) {
  wrapper = mount(PluginMcpSignIn, {
    props: { pluginName: "AI Market\u202e", connector: "github", host: "api.githubcopilot.com", phase, deviceCode },
    attachTo: document.body,
  });
  await flushPromises();
  return wrapper;
}

const button = (action: string) => document.querySelector<HTMLButtonElement>(`[data-mcp-auth-action="${action}"]`);

describe("PluginMcpSignIn", () => {
  it("names the plugin and connector and waits for the browser", async () => {
    await open(null);
    const text = document.body.textContent ?? "";
    expect(text).toContain("plugins.aiConfig.signIn.title");
    expect(text).toContain('"connector":"github"');
    expect(text).toContain('"host":"api.githubcopilot.com"');
    expect(button("continue")).toBeNull();
    // Hidden characters in the plugin name are shown, not applied.
    expect(text).not.toContain("\u202e");
    expect(text).toContain("settings.mcp.auth.waiting");
    expect(document.querySelector("[data-mcp-device-code]")).toBeNull();
  });

  it("shows the device code and copies it before opening the verification page", async () => {
    await open({ userCode: "ABCD-1234", verificationUri: "https://github.com/login/device" });
    expect(document.querySelector("[data-mcp-device-code]")?.textContent).toBe("ABCD-1234");
    button("copy-open")?.click();
    await flushPromises();
    expect(native.copyText).toHaveBeenCalledWith("ABCD-1234");
    expect(native.openSystemBrowser).toHaveBeenCalledWith("https://github.com/login/device");
  });

  it("asks first, naming the server, and opens nothing before Continue", async () => {
    const view = await open(null, "confirm");
    const text = document.body.textContent ?? "";
    expect(text).toContain("plugins.aiConfig.signIn.confirm");
    expect(text).toContain('"host":"api.githubcopilot.com"');
    expect(text).not.toContain("settings.mcp.auth.waiting");
    button("continue")?.click();
    await flushPromises();
    expect(view.emitted("continue")).toHaveLength(1);
    expect(view.emitted("cancel")).toBeUndefined();
  });

  it("cancels from the button", async () => {
    const view = await open(null);
    button("cancel")?.click();
    await flushPromises();
    expect(view.emitted("cancel")).toHaveLength(1);
  });
});
