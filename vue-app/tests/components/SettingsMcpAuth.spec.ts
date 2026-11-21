import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import McpServerAuthControls from "@/components/settings/McpServerAuthControls.vue";

const native = vi.hoisted(() => {
  const handlers = new Map<string, Set<(payload: unknown) => void>>();
  return {
    handlers,
    invoke: vi.fn<(command: string, args?: Record<string, unknown>, options?: { signal?: AbortSignal }) => Promise<unknown>>(),
    copyText: vi.fn(async (_text: string) => {}),
    openSystemBrowser: vi.fn(async (_url: string) => {}),
    emit(name: string, payload: unknown) {
      for (const handler of handlers.get(name) ?? []) handler(payload);
    },
  };
});

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: native.invoke,
  copyText: native.copyText,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    if (!native.handlers.has(name)) native.handlers.set(name, new Set());
    native.handlers.get(name)!.add(handler);
    return () => native.handlers.get(name)?.delete(handler);
  },
}));
vi.mock("@/services/web-automation-service", () => ({ openSystemBrowser: native.openSystemBrowser }));
vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
  }),
}));
vi.mock("@/services/mcp-auth-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/mcp-auth-service")>();
  return {
    ...actual,
    mcpAuthStatus: vi.fn(actual.mcpAuthStatus),
    signInMcpServer: vi.fn(actual.signInMcpServer),
    signOutMcpServer: vi.fn(actual.signOutMcpServer),
  };
});

const service = await import("@/services/mcp-auth-service");
const actualService = await vi.importActual<typeof import("@/services/mcp-auth-service")>("@/services/mcp-auth-service");

const server = { id: "github", url: "https://api.githubcopilot.com/mcp/", auth: "oauth" as const };
let wrapper: VueWrapper | undefined;

function answer(responses: Record<string, unknown | Error | (() => Promise<unknown>)>) {
  native.invoke.mockImplementation(async (command: string) => {
    const response = responses[command];
    if (typeof response === "function") return (response as () => Promise<unknown>)();
    if (response instanceof Error) throw response;
    return response;
  });
}

async function mountControls(props: Record<string, unknown> = {}) {
  wrapper = mount(McpServerAuthControls, { props: { server, ...props }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  native.handlers.clear();
  native.invoke.mockReset();
});

describe("connector sign-in controls", () => {
  it("shows the sign-in button and a signed-out status for an oauth connector", async () => {
    answer({ mcp_oauth_status: { status: "signed-out" } });
    const controls = await mountControls();
    expect(native.invoke).toHaveBeenCalledWith("mcp_oauth_status", { serverId: "github", url: server.url });
    expect(controls.get("[data-mcp-auth-status]").text()).toBe("settings.mcp.auth.status.signed-out");
    expect(controls.get('[data-mcp-auth-action="sign-in"]').text()).toBe("settings.mcp.auth.signIn");
    expect(controls.find('[data-mcp-auth-action="sign-out"]').exists()).toBe(false);
  });

  it("signs in through signInMcpServer and then reports signed in", async () => {
    answer({ mcp_oauth_status: { status: "signed-out" }, mcp_oauth_login: { status: "signed-in" } });
    const controls = await mountControls();
    await controls.get('[data-mcp-auth-action="sign-in"]').trigger("click");
    await flushPromises();
    expect(service.signInMcpServer).toHaveBeenCalledWith(server, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(native.invoke).toHaveBeenCalledWith("mcp_oauth_login", { serverId: "github", url: server.url }, expect.anything());
    expect(controls.emitted("signed-in")).toHaveLength(1);
    expect(controls.get("[data-mcp-auth-status]").text()).toBe("settings.mcp.auth.status.signed-in");
  });

  it("shows the device code with a copy-and-open button while the device flow waits", async () => {
    let finish: (value: unknown) => void = () => {};
    answer({
      mcp_oauth_status: { status: "signed-out" },
      mcp_oauth_login: () => new Promise((resolve) => { finish = resolve; }),
    });
    const controls = await mountControls();
    await controls.get('[data-mcp-auth-action="sign-in"]').trigger("click");
    await flushPromises();
    native.emit("mcp-oauth-device-code", { serverId: "other", userCode: "XXXX-0000", verificationUri: "https://example.com/device" });
    native.emit("mcp-oauth-device-code", { serverId: "github", userCode: "ABCD-1234", verificationUri: "https://github.com/login/device" });
    await flushPromises();
    expect(controls.get("[data-mcp-device-code]").text()).toBe("ABCD-1234");
    expect(controls.find('[data-mcp-auth-action="cancel"]').exists()).toBe(true);
    const copyOpen = controls.get('[data-mcp-auth-action="copy-open"]');
    expect(copyOpen.text()).toBe("settings.mcp.auth.copyAndOpen");
    await copyOpen.trigger("click");
    await flushPromises();
    expect(native.copyText).toHaveBeenCalledWith("ABCD-1234");
    expect(native.openSystemBrowser).toHaveBeenCalledWith("https://github.com/login/device");
    finish({ status: "signed-in" });
    await flushPromises();
    expect(controls.find("[data-mcp-device-code]").exists()).toBe(false);
  });

  it("ignores a device code whose verification page is not https", async () => {
    answer({ mcp_oauth_status: { status: "signed-out" }, mcp_oauth_login: () => new Promise(() => {}) });
    const controls = await mountControls();
    await controls.get('[data-mcp-auth-action="sign-in"]').trigger("click");
    native.emit("mcp-oauth-device-code", { serverId: "github", userCode: "ABCD-1234", verificationUri: "javascript:alert(1)" });
    await flushPromises();
    expect(controls.find("[data-mcp-device-code]").exists()).toBe(false);
  });

  it("offers sign-out when signed in and asks again when a connect was refused", async () => {
    answer({ mcp_oauth_status: { status: "signed-in" }, mcp_oauth_logout: undefined });
    const controls = await mountControls();
    expect(controls.get("[data-mcp-auth-status]").text()).toBe("settings.mcp.auth.status.signed-in");
    expect(controls.find('[data-mcp-auth-action="sign-in"]').exists()).toBe(false);
    const signOut = controls.get('[data-mcp-auth-action="sign-out"]');
    expect(signOut.text()).toBe("settings.mcp.auth.signOut");

    // A connect that failed with [MCP_AUTH_REQUIRED] wins over a signed-in status.
    await controls.setProps({ authRequired: true });
    expect(controls.get("[data-mcp-auth-status]").text()).toBe("settings.mcp.auth.status.needs-login");
    expect(controls.get('[data-mcp-auth-action="sign-in"]').text()).toBe("settings.mcp.auth.signInAgain");

    await signOut.trigger("click");
    await flushPromises();
    expect(service.signOutMcpServer).toHaveBeenCalledWith(server);
    expect(native.invoke).toHaveBeenCalledWith("mcp_oauth_logout", { serverId: "github" });
    expect(controls.emitted("signed-out")).toHaveLength(1);
  });

  it("explains a sign-in the server does not support instead of a raw error", async () => {
    answer({
      mcp_oauth_status: { status: "signed-out" },
      mcp_oauth_login: new Error("[MCP_AUTH_UNSUPPORTED] no registration endpoint"),
    });
    const controls = await mountControls();
    await controls.get('[data-mcp-auth-action="sign-in"]').trigger("click");
    await flushPromises();
    expect(controls.get("[data-mcp-auth-error]").text()).toBe("settings.mcp.auth.error.unsupported");
  });
});

describe("connector sign-in service", () => {
  it("maps login results, cancellation and failures", async () => {
    answer({ mcp_oauth_login: { status: "cancelled" } });
    expect(await actualService.signInMcpServer(server)).toBe("cancelled");
    const onError = vi.fn();
    answer({ mcp_oauth_login: new Error("[MCP_AUTH_TIMEOUT] 登录超时") });
    expect(await actualService.signInMcpServer(server, { onError })).toBe("failed");
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: "[MCP_AUTH_TIMEOUT] 登录超时" }));
    const controller = new AbortController();
    answer({ mcp_oauth_login: async () => { controller.abort(); throw new Error("call cancelled"); } });
    expect(await actualService.signInMcpServer(server, { signal: controller.signal })).toBe("cancelled");
  });

  it("hands a bearer header only for a signed-in connector", async () => {
    answer({ mcp_oauth_access_token: { token: "gho_secret" } });
    expect(await actualService.acpAuthHeaders(server)).toEqual({ Authorization: "Bearer gho_secret" });
    expect(native.invoke).toHaveBeenCalledWith("mcp_oauth_access_token", { serverId: "github", url: server.url });
    answer({ mcp_oauth_access_token: new Error("[MCP_AUTH_REQUIRED] 连接器需要登录") });
    expect(await actualService.acpAuthHeaders(server)).toBeNull();
    answer({ mcp_oauth_access_token: { token: "" } });
    expect(await actualService.acpAuthHeaders(server)).toBeNull();
    // A refresh that could not reach the server is not a missing sign-in: it is passed on.
    answer({ mcp_oauth_access_token: new Error("[MCP_AUTH_NETWORK] refresh failed") });
    await expect(actualService.acpAuthHeaders(server)).rejects.toThrow(/MCP_AUTH_NETWORK/);
  });

  it("rejects unknown status values from the backend", async () => {
    answer({ mcp_oauth_status: { status: "weird" } });
    await expect(actualService.mcpAuthStatus(server)).rejects.toThrow(/mcp_oauth_status/);
  });
});
