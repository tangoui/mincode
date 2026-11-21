import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { ElMessage, ElMessageBox } from "element-plus";
import CloudPairingSection from "@/components/settings/CloudPairingSection.vue";
import {
  cloudPairingQrContent,
  countLimitedMemberDevices,
  isCloudDevicePairingAvailable,
  parseCloudRealtimeError,
  type CloudPairedDevice,
} from "@/services/cloud-companion-service";

const bridge = vi.hoisted(() => ({
  invoke: vi.fn(),
  listeners: new Map<string, (payload: unknown) => void>(),
  unsubscribe: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: bridge.invoke,
  listenAppEvent: (event: string, handler: (payload: unknown) => void) => {
    bridge.listeners.set(event, handler);
    return bridge.unsubscribe;
  },
  copyText: vi.fn().mockResolvedValue(undefined),
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));
vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    intlLocale: ref("en-US"),
  }),
}));

const phone: CloudPairedDevice = {
  device_id: "phone-0000000000000001",
  device_type: "mobile",
  name: "Pixel",
  platform: "android",
  os_version: "15",
  device_model: "Pixel 9",
  architecture: "arm64",
  signing_public_key: "",
  encryption_public_key: "",
  app_version: "0.3.0",
  first_login_at: 1,
  last_login_at: 1,
  last_seen_at: 1_800_000_000,
  last_ip: "",
  login_count: 1,
  capabilities: ["AI_CHAT", "TERMINAL"],
  grant_id: "1",
};

const NOW = 1_800_000_000;

function routeInvoke(handlers: Record<string, (args: unknown) => unknown>) {
  bridge.invoke.mockImplementation(async (command: string, args: unknown) => {
    const handler = handlers[command];
    if (!handler) throw new Error(`unexpected command ${command}`);
    return handler(args);
  });
}

beforeEach(() => {
  bridge.invoke.mockReset();
  bridge.listeners.clear();
  bridge.unsubscribe.mockReset();
  vi.spyOn(ElMessage, "success").mockReturnValue({ close: () => undefined } as ReturnType<typeof ElMessage.success>);
  vi.spyOn(ElMessage, "error").mockReturnValue({ close: () => undefined } as ReturnType<typeof ElMessage.error>);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("pairing QR code", () => {
  it("encodes the pairing code in the format the phone scanner accepts", () => {
    expect(cloudPairingQrContent("abcdefghijklmnopqrstuv.secret_value")).toBe(
      "mincode://pair?code=abcdefghijklmnopqrstuv.secret_value",
    );
  });

  it("shows a scannable QR code while the code is valid and hides it after expiry", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(NOW * 1000);
    routeInvoke({
      cloud_paired_devices: () => [],
      cloud_pairing_create: () => ({
        challenge_id: "abcdefghijklmnopqrstuv",
        pairing_secret: "secret_value",
        expires_at: NOW + 3,
        capabilities: ["AI_CHAT"],
        desktop: {},
      }),
    });
    const wrapper = mount(CloudPairingSection, { props: { registration: "registered" } });
    await flushPromises();
    await wrapper.get("button.el-button--primary").trigger("click");
    await flushPromises();

    const qr = wrapper.get('img[alt="settings.cloud.pairingQrAlt"]');
    expect(qr.attributes("src")).toMatch(/^data:image\/svg\+xml/);
    expect(wrapper.text()).toContain("settings.cloud.pairingQrHint");

    vi.advanceTimersByTime(3000);
    await flushPromises();
    expect(wrapper.find('img[alt="settings.cloud.pairingQrAlt"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

describe("member device usage", () => {
  it("counts desktops and phones toward the same plan limit", () => {
    expect(countLimitedMemberDevices([{ deviceType: "desktop" }, { deviceType: "mobile" }, { deviceType: "desktop" }])).toBe(3);
    expect(countLimitedMemberDevices([{ deviceType: "mobile" }])).toBe(1);
    expect(countLimitedMemberDevices([])).toBe(0);
  });
});

describe("cloud pairing availability", () => {
  it("only requires a signed-in member", () => {
    expect(isCloudDevicePairingAvailable({ signedIn: true })).toBe(true);
    expect(isCloudDevicePairingAvailable({ signedIn: false })).toBe(false);
    expect(isCloudDevicePairingAvailable(null)).toBe(false);
    expect(isCloudDevicePairingAvailable(undefined)).toBe(false);
  });

  it("prefers the readable relay message over the raw code", () => {
    expect(parseCloudRealtimeError({ type: "error", code: "not_paired", message: "该手机未配对或已解除配对" })).toEqual({
      code: "not_paired",
      message: "该手机未配对或已解除配对",
      requestId: undefined,
    });
    expect(parseCloudRealtimeError({ type: "error", code: "decrypt_failed" })?.message).toBe("");
    expect(parseCloudRealtimeError({ type: "relay", code: "x" })).toBeNull();
    expect(parseCloudRealtimeError("nope")).toBeNull();
  });
});

describe("CloudPairingSection", () => {
  it("creates a code with only optional capabilities, counts down, expires and stops its timer on unmount", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(NOW * 1000);
    const create = vi.fn(() => ({
      challenge_id: "abcdefghijklmnopqrstuv",
      pairing_secret: "secret_value",
      expires_at: NOW + 3,
      capabilities: ["AI_CHAT", "SSH_CONNECT"],
      desktop: {},
    }));
    routeInvoke({ cloud_paired_devices: () => [], cloud_pairing_create: create });
    const wrapper = mount(CloudPairingSection, { props: { registration: "registered" } });
    await flushPromises();
    expect(wrapper.text()).toContain("settings.cloud.pairedDevicesEmpty");

    const boxes = wrapper.findAll('input[type="checkbox"]');
    expect(boxes).toHaveLength(4);
    expect((boxes[0]!.element as HTMLInputElement).checked).toBe(true);
    expect((boxes[0]!.element as HTMLInputElement).disabled).toBe(true);
    await boxes[2]!.setValue(true);

    await wrapper.get("button.el-button--primary").trigger("click");
    await flushPromises();
    expect(create).toHaveBeenCalledWith({ capabilities: ["SSH_CONNECT"] });
    expect((wrapper.get('input[aria-label="settings.cloud.pairingCode"]').element as HTMLInputElement).value).toBe(
      "abcdefghijklmnopqrstuv.secret_value",
    );
    expect(wrapper.text()).toContain('settings.cloud.pairingExpiresIn:{"time":"0:03"}');
    expect(wrapper.get("button.el-button--primary").text()).toBe("settings.cloud.regeneratePairing");

    vi.advanceTimersByTime(3000);
    await flushPromises();
    expect(wrapper.text()).toContain("settings.cloud.pairingExpired");
    expect(vi.getTimerCount()).toBe(0);

    await wrapper.get("button.el-button--primary").trigger("click");
    await flushPromises();
    expect(vi.getTimerCount()).toBe(1);
    wrapper.unmount();
    expect(vi.getTimerCount()).toBe(0);
    expect(bridge.unsubscribe).toHaveBeenCalled();
  });

  it("notices a newly paired phone while the code is live and drops the used code", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(NOW * 1000);
    let devices: CloudPairedDevice[] = [];
    routeInvoke({
      cloud_paired_devices: () => devices,
      cloud_pairing_create: () => ({ challenge_id: "c".repeat(22), pairing_secret: "s", expires_at: NOW + 300, capabilities: ["AI_CHAT"], desktop: {} }),
    });
    const wrapper = mount(CloudPairingSection);
    await flushPromises();
    await wrapper.get("button.el-button--primary").trigger("click");
    await flushPromises();
    devices = [phone];
    vi.advanceTimersByTime(5000);
    await flushPromises();
    expect(ElMessage.success).toHaveBeenCalledWith('settings.cloud.pairingSucceeded:{"name":"Pixel"}');
    expect(wrapper.find('input[aria-label="settings.cloud.pairingCode"]').exists()).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    wrapper.unmount();
  });

  it("treats re-pairing an already paired phone (new grant_id) as success", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(NOW * 1000);
    let devices: CloudPairedDevice[] = [phone];
    routeInvoke({
      cloud_paired_devices: () => devices,
      cloud_pairing_create: () => ({ challenge_id: "c".repeat(22), pairing_secret: "s", expires_at: NOW + 300, capabilities: ["AI_CHAT"], desktop: {} }),
    });
    const wrapper = mount(CloudPairingSection);
    await flushPromises();
    await wrapper.get("button.el-button--primary").trigger("click");
    await flushPromises();
    vi.advanceTimersByTime(5000);
    await flushPromises();
    expect(ElMessage.success).not.toHaveBeenCalledWith('settings.cloud.pairingSucceeded:{"name":"Pixel"}');
    expect(wrapper.find('input[aria-label="settings.cloud.pairingCode"]').exists()).toBe(true);
    devices = [{ ...phone, capabilities: ["AI_CHAT"], grant_id: "2" }];
    vi.advanceTimersByTime(5000);
    await flushPromises();
    expect(ElMessage.success).toHaveBeenCalledWith('settings.cloud.pairingSucceeded:{"name":"Pixel"}');
    expect(wrapper.find('input[aria-label="settings.cloud.pairingCode"]').exists()).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    wrapper.unmount();
  });

  it("lists paired phones and unpairs after confirmation", async () => {
    let devices: CloudPairedDevice[] = [phone];
    const revoke = vi.fn(() => {
      devices = [];
      return { revoked: true };
    });
    routeInvoke({ cloud_paired_devices: () => devices, cloud_device_revoke: revoke });
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockResolvedValue("confirm" as never);
    const wrapper = mount(CloudPairingSection);
    await flushPromises();
    expect(wrapper.text()).toContain("Pixel");
    expect(wrapper.text()).toContain("Android 15 · Pixel 9");
    expect(wrapper.text()).toContain("settings.cloud.capability.TERMINAL");

    const unpair = wrapper.findAll("button").find((button) => button.text() === "settings.cloud.unpair");
    await unpair!.trigger("click");
    await flushPromises();
    expect(confirm).toHaveBeenCalled();
    expect(revoke).toHaveBeenCalledWith({ deviceId: phone.device_id });
    expect(wrapper.text()).toContain("settings.cloud.pairedDevicesEmpty");
    wrapper.unmount();
  });

  it("shows the load error with a retry, the registration warning and relay errors", async () => {
    let fail = true;
    routeInvoke({
      cloud_paired_devices: () => {
        if (fail) throw new Error("network down");
        return [];
      },
    });
    const wrapper = mount(CloudPairingSection, { props: { registration: "failed", registrationError: "device_limit reached" } });
    await flushPromises();
    expect(wrapper.text()).toContain("settings.cloud.registrationFailed");
    expect(wrapper.text()).toContain("device_limit reached");
    expect(wrapper.text()).toContain("settings.cloud.pairedDevicesFailed");
    expect(wrapper.text()).toContain("network down");

    fail = false;
    const retry = wrapper.findAll("button").find((button) => button.text() === "settings.cloud.retry");
    await retry!.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("settings.cloud.pairedDevicesEmpty");

    bridge.listeners.get("cloud:realtime")?.({ type: "error", code: "capability_denied", message: "手机没有此功能的权限" });
    await flushPromises();
    expect(wrapper.text()).toContain("手机没有此功能的权限");
    bridge.listeners.get("cloud:realtime")?.({ type: "error", code: "decrypt_failed" });
    await flushPromises();
    expect(wrapper.text()).toContain('settings.cloud.relayErrorCode:{"code":"decrypt_failed"}');
    wrapper.unmount();
  });
});
