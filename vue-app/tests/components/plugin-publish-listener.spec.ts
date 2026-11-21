import { afterEach, describe, expect, it, vi } from "vitest";
import { shallowMount } from "@vue/test-utils";
import PluginPublishHost from "@/components/plugins/development/PluginPublishHost.vue";
const bridge = vi.hoisted(() => ({ handlers: new Map<string, (value: unknown) => void>(), invoke: vi.fn<(command: string, args: unknown) => Promise<unknown>>(), desktop: true }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => bridge.desktop, listenAppEvent: (name: string, handler: (value: unknown) => void) => { bridge.handlers.set(name, handler); return () => bridge.handlers.delete(name); }, invokeBackend: bridge.invoke }));
import { dismissPluginPublish, installPluginPublishListener, pluginPublishEvents, pluginPublishPending } from "@/services/plugin-publish-listener";
const record = (id: string, status = "awaiting_login") => ({ id, status, step: "lookup", pluginId: "demo.plugin", pluginName: "Demo", version: "1.0.0", releaseDir: "/project/release", account: "", newPlugin: true, existingDraft: false, submit: true, releaseNotes: "", expiresAt: new Date(Date.now() + 600_000).toISOString(), packages: [{ os: "darwin", arch: "arm64", file: "mac.min", size: 10, sha256: "a".repeat(64), state: "pending", percent: 0 }], metadataChanges: [] });
let stop: () => void = () => undefined;
afterEach(() => { stop(); bridge.invoke.mockReset(); bridge.handlers.clear(); bridge.desktop = true; });
describe("early publish listener", () => {
  it("captures the first request before the host loads and ignores unknown boundary fields", async () => {
    bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
    bridge.handlers.get("plugin:development-publish")?.({ ...record("first"), token: "secret" });
    expect(pluginPublishPending.value).toBe(true); await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(1)); expect(JSON.stringify(pluginPublishEvents.value)).not.toContain("secret");
    bridge.handlers.get("plugin:development-publish")?.(record("second")); await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(2));
    dismissPluginPublish("first"); await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(1));
  });
  it("auto refreshes login waits on sign-in, with only the id", async () => {
    bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
    bridge.handlers.get("plugin:development-publish")?.(record("login")); bridge.handlers.get("cloud:status")?.({ signedIn: true });
    await vi.waitFor(() => expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_development_publish_refresh", { id: "login" }));
    expect(bridge.invoke.mock.calls.filter(([name]) => name.endsWith("_confirm"))).toHaveLength(0);
  });
  it("does not replace new events with a late initial list", async () => {
    let resolve: (value: unknown) => void = () => undefined; bridge.invoke.mockImplementationOnce(() => new Promise(r => { resolve = r; })); stop = installPluginPublishListener();
    bridge.handlers.get("plugin:development-publish")?.(record("one", "canceled")); resolve([record("one")]);
    await vi.waitFor(() => expect(pluginPublishEvents.value).toMatchObject([{ id: "one", status: "canceled" }]));
  });
});

it("returns to confirmation automatically when sign-in arrives before the login-wait event", async () => {
  bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
  bridge.handlers.get("cloud:status")?.({ signedIn: true });
  bridge.handlers.get("plugin:development-publish")?.(record("late-login"));
  await vi.waitFor(() => expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_development_publish_refresh", { id: "late-login" }));
  bridge.handlers.get("plugin:development-publish")?.(record("late-login"));
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(bridge.invoke.mock.calls.filter(([name]) => name.endsWith("_refresh"))).toHaveLength(1);
});


it("shows one record and advances the global queue only when its terminal dialog closes", async () => {
  bridge.invoke.mockResolvedValue([]);
  stop = installPluginPublishListener();
  bridge.handlers.get("plugin:development-publish")?.(record("first", "succeeded"));
  bridge.handlers.get("plugin:development-publish")?.(record("second", "pending_confirmation"));
  await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(2));
  const host = shallowMount(PluginPublishHost);
  const current = () => host.findComponent({ name: "PluginPublishConfirmDialog" });
  expect(current().props("record")).toMatchObject({ id: "first" });
  expect(current().props("queued")).toBe(1);
  current().vm.$emit("close");
  await vi.waitFor(() => expect(current().props("record")).toMatchObject({ id: "second" }));
  expect(current().props("queued")).toBe(0);
  host.unmount();
});

it("reopens confirmation above cloud settings after a login wait", async () => {
  bridge.invoke.mockResolvedValue([]);
  stop = installPluginPublishListener();
  bridge.handlers.get("plugin:development-publish")?.(record("sign-in-return"));
  await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(1));
  const host = shallowMount(PluginPublishHost);
  const current = () => host.findComponent({ name: "PluginPublishConfirmDialog" });
  const loginDialog = current().vm;
  bridge.handlers.get("plugin:development-publish")?.(record("sign-in-return", "pending_confirmation"));
  await vi.waitFor(() => expect(current().props("record")).toMatchObject({ status: "pending_confirmation" }));
  expect(current().vm).not.toBe(loginDialog);
  host.unmount();
});

it("bounds retained frontend records to the backend's eight-record lifecycle", async () => {
  bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
  for (let i = 0; i < 9; i++) bridge.handlers.get("plugin:development-publish")?.(record(`bounded-${i}`, "succeeded"));
  await vi.waitFor(() => expect(pluginPublishEvents.value.length).toBeGreaterThan(0));
  expect(pluginPublishEvents.value.length).toBeLessThanOrEqual(8);
});
it("expires dismissed ids so an old close does not suppress a new bounded lifecycle", async () => {
  const now = Date.parse("2026-11-04T08:00:00Z"); const clock = vi.spyOn(Date, "now").mockReturnValue(now);
  bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
  bridge.handlers.get("plugin:development-publish")?.({ ...record("expired-dismissal", "succeeded"), expiresAt: new Date(now + 60_000).toISOString() });
  await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(1));
  dismissPluginPublish("expired-dismissal"); await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(0));
  clock.mockReturnValue(now + 120_000);
  bridge.handlers.get("plugin:development-publish")?.({ ...record("expired-dismissal"), expiresAt: new Date(now + 300_000).toISOString() });
  await vi.waitFor(() => expect(pluginPublishEvents.value).toHaveLength(1));
});

it("retains the initial preparing event with no packages without auto confirmation", async () => {
  bridge.invoke.mockResolvedValue([]); stop = installPluginPublishListener();
  bridge.handlers.get("plugin:development-publish")?.({ ...record("preparing", "preparing"), packages: [] });
  await vi.waitFor(() => expect(pluginPublishEvents.value).toMatchObject([{ id: "preparing", status: "preparing", packages: [] }]));
  const host = shallowMount(PluginPublishHost);
  expect(host.findComponent({ name: "PluginPublishConfirmDialog" }).props("record")).toMatchObject({ status: "preparing" });
  expect(bridge.invoke.mock.calls.filter(([name]) => name.endsWith("_confirm") || name.endsWith("_refresh"))).toHaveLength(0);
  host.unmount();
});
