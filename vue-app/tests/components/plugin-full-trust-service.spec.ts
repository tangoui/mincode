import { afterEach, describe, expect, it, vi } from "vitest";

const bridge = vi.hoisted(() => ({
  desktop: true, reply: undefined as unknown, fail: false, calls: [] as Array<[string, unknown]>,
  listener: undefined as undefined | ((payload: unknown) => void),
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return {
    ...actual,
    isDesktopRuntime: () => bridge.desktop,
    invokeBackend: (command: string, args: unknown) => {
      bridge.calls.push([command, args]);
      return bridge.fail ? Promise.reject(new Error("boom")) : Promise.resolve(bridge.reply);
    },
    listenAppEvent: (_event: string, handler: (payload: unknown) => void) => {
      bridge.listener = handler;
      return () => undefined;
    },
  };
});
vi.mock("@/services/plugin-service", () => ({ listPlugins: () => Promise.resolve([{ id: "trust.demo", name: "Demo" }]) }));
const warning = vi.hoisted(() => vi.fn());
vi.mock("element-plus", () => ({ ElMessage: { warning } }));

const { authorizeWithoutPrompt, listenFullTrustChanged, setFullTrust } = await import("@/services/plugin-full-trust");

afterEach(() => {
  bridge.desktop = true;
  bridge.reply = undefined;
  bridge.fail = false;
  bridge.calls = [];
  warning.mockClear();
});

describe("authorizeWithoutPrompt", () => {
  it("skips only when Go allows", async () => {
    bridge.reply = { allowed: true };
    expect(await authorizeWithoutPrompt("trust.demo", "viewAction", "read", "run")).toBe(true);
    expect(bridge.calls).toEqual([["plugin_full_trust_authorize", { pluginId: "trust.demo", surface: "viewAction", risk: "dangerous", subject: "run" }]]);
  });

  it("fails closed outside the desktop, on errors and on odd replies", async () => {
    bridge.desktop = false;
    bridge.reply = { allowed: true };
    expect(await authorizeWithoutPrompt("trust.demo", "testRun", "write", "suite")).toBe(false);
    expect(bridge.calls).toEqual([]);
    bridge.desktop = true;
    bridge.fail = true;
    expect(await authorizeWithoutPrompt("trust.demo", "testRun", "write", "suite")).toBe(false);
    bridge.fail = false;
    bridge.reply = { allowed: "true" };
    expect(await authorizeWithoutPrompt("trust.demo", "testRun", "write", "suite")).toBe(false);
    bridge.reply = null;
    expect(await authorizeWithoutPrompt("trust.demo", "testRun", "write", "suite")).toBe(false);
  });

  it("warns once per plugin when trust lapsed", async () => {
    bridge.reply = { allowed: false, lapsed: "package_changed" };
    expect(await authorizeWithoutPrompt("trust.demo", "viewAction", "write", "run")).toBe(false);
    expect(await authorizeWithoutPrompt("trust.demo", "viewAction", "write", "run")).toBe(false);
    await vi.waitFor(() => expect(warning).toHaveBeenCalledTimes(1));
    expect(String(warning.mock.calls[0]?.[0])).toContain("Demo");
  });
});

describe("lapse notice after trust is granted again", () => {
  const subject = (pluginId: string) => ({ pluginId, name: pluginId, version: "1", capabilities: [], fingerprint: "ab".repeat(32), eligible: true });

  async function lapse(pluginId: string) {
    bridge.reply = { allowed: false, lapsed: "upgraded" };
    expect(await authorizeWithoutPrompt(pluginId, "viewAction", "write", "run")).toBe(false);
    await new Promise((resolve) => setTimeout(resolve, 0));
  }

  it("a regrant through the switch shows a later lapse again", async () => {
    await lapse("regrant.set");
    await lapse("regrant.set");
    await vi.waitFor(() => expect(warning).toHaveBeenCalledTimes(1));
    bridge.reply = { pluginId: "regrant.set", trusted: true, grantedAt: 2, legacyDangerous: false, subject: subject("regrant.set") };
    await setFullTrust("regrant.set", true, "ab".repeat(32));
    await lapse("regrant.set");
    await vi.waitFor(() => expect(warning).toHaveBeenCalledTimes(2));
  });

  it("a trusted change event re-arms the notice", async () => {
    const stop = listenFullTrustChanged(() => undefined);
    await lapse("regrant.event");
    bridge.listener?.({ pluginId: "regrant.event", trusted: false, reason: "revoked" });
    await lapse("regrant.event");
    await vi.waitFor(() => expect(warning).toHaveBeenCalledTimes(1));
    bridge.listener?.({ pluginId: "regrant.event", trusted: true, reason: "granted" });
    await lapse("regrant.event");
    await vi.waitFor(() => expect(warning).toHaveBeenCalledTimes(2));
    stop();
  });
});
