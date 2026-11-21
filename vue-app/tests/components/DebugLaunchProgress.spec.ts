import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import DebugLaunchProgress from "@/components/debug/DebugLaunchProgress.vue";
import { DEBUG_LAUNCH_STALL_MS, launchStatusText, LAUNCH_STATUS_MAX_CHARS } from "@/domain/debug/launch-progress";
import type { DebugLaunchView } from "@/stores/debug-sessions";

function launch(change: Partial<DebugLaunchView> = {}): DebugLaunchView {
  return {
    seq: 1,
    root: "/Users/A/Proj",
    configId: "cfg-1",
    configName: "App",
    noDebug: false,
    step: "checking",
    languageService: false,
    status: "",
    progressAt: Date.now(),
    confirming: false,
    cancelling: false,
    ...change,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("DebugLaunchProgress", () => {
  it("shows the steps up to the current one and the step's text", async () => {
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch({ step: "resolving" }) } });
    expect(wrapper.text()).toContain("正在准备调试 App");
    const titles = wrapper.findAll(".el-step__title").map((item) => item.text());
    expect(titles).toEqual(["检查调试器", "解析启动信息", "启动程序"]);
    await nextTick();
    expect(wrapper.findAll(".el-step__head.is-success")).toHaveLength(1);
    expect(wrapper.find(".debug-launch__status").text()).toBe("正在解析启动信息…");
    wrapper.unmount();
  });

  it("adds the language service step and shows its status text verbatim", () => {
    const wrapper = mount(DebugLaunchProgress, {
      props: { launch: launch({ step: "languageService", languageService: true, status: "1100% Starting Java Language Server - Refreshing '/app'." }) },
    });
    expect(wrapper.findAll(".el-step__title").map((item) => item.text())).toEqual(["检查调试器", "启动语言服务", "解析启动信息", "启动程序"]);
    expect(wrapper.find(".debug-launch__status").text()).toBe("1100% Starting Java Language Server - Refreshing '/app'.");
    wrapper.unmount();
  });

  it("hints at a slow download only after the language service made no progress for a while", async () => {
    vi.useFakeTimers();
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch({ step: "languageService", languageService: true }) } });
    await vi.advanceTimersByTimeAsync(DEBUG_LAUNCH_STALL_MS - 1_000);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    // New status text: the countdown starts again.
    await wrapper.setProps({ launch: launch({ step: "languageService", languageService: true, status: "Downloading 10%", progressAt: Date.now() }) });
    await vi.advanceTimersByTimeAsync(DEBUG_LAUNCH_STALL_MS - 1_000);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(wrapper.find(".debug-launch__slow").text()).toContain("可能正在下载依赖");
    // Another step clears it; no hint outside the language service step.
    await wrapper.setProps({ launch: launch({ step: "resolving", languageService: true, progressAt: Date.now() }) });
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    await vi.advanceTimersByTimeAsync(DEBUG_LAUNCH_STALL_MS * 2);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    wrapper.unmount();
  });

  it("does not count the time a confirmation dialog is open as a stall", async () => {
    vi.useFakeTimers();
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch({ step: "languageService", languageService: true, confirming: true }) } });
    await vi.advanceTimersByTimeAsync(DEBUG_LAUNCH_STALL_MS * 3);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    // Closed: the countdown starts from there.
    await wrapper.setProps({ launch: launch({ step: "languageService", languageService: true, confirming: false, progressAt: Date.now() }) });
    await vi.advanceTimersByTimeAsync(DEBUG_LAUNCH_STALL_MS - 1_000);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(true);
    // Another dialog opening hides the hint again.
    await wrapper.setProps({ launch: launch({ step: "languageService", languageService: true, confirming: true, progressAt: Date.now() }) });
    expect(wrapper.find(".debug-launch__slow").exists()).toBe(false);
    wrapper.unmount();
  });

  it("cancels until the start request is sent", async () => {
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch({ step: "resolving", noDebug: true }) } });
    expect(wrapper.text()).toContain("正在准备运行 App");
    await wrapper.get('[data-action="cancel-launch"]').trigger("click");
    expect(wrapper.emitted("cancel")).toHaveLength(1);
    await wrapper.setProps({ launch: launch({ step: "launching" }) });
    expect(wrapper.get('[data-action="cancel-launch"]').attributes("disabled")).toBeDefined();
    wrapper.unmount();
  });
});

describe("DebugLaunchProgress cancel feedback", () => {
  it("says it is cancelling and keeps Cancel disabled until the launch ends", async () => {
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch({ step: "resolving" }) } });
    const button = () => wrapper.get('[data-action="cancel-launch"]');
    expect(button().text()).toBe("取消");
    await wrapper.setProps({ launch: launch({ step: "resolving", cancelling: true }) });
    expect(button().text()).toBe("正在取消…");
    expect(button().attributes("disabled")).toBeDefined();
    await button().trigger("click");
    expect(wrapper.emitted("cancel")).toBeUndefined();
    wrapper.unmount();
  });

  it("reports whether it is shown, and no longer once unmounted", async () => {
    const onShown = vi.fn();
    const wrapper = mount(DebugLaunchProgress, { props: { launch: launch(), visible: true, onShown } });
    expect(onShown.mock.calls).toEqual([[true]]);
    await wrapper.setProps({ visible: false });
    expect(onShown.mock.calls).toEqual([[true], [false]]);
    await wrapper.setProps({ visible: true });
    wrapper.unmount();
    expect(onShown.mock.calls).toEqual([[true], [false], [true], [false]]);
    // A hidden panel mounting and unmounting says nothing (panels are counted).
    const hidden = vi.fn();
    mount(DebugLaunchProgress, { props: { launch: launch(), visible: false, onShown: hidden } }).unmount();
    expect(hidden).not.toHaveBeenCalled();
  });
});

describe("launchStatusText", () => {
  it("drops control and bidi characters, collapses whitespace and bounds the length", () => {
    expect(launchStatusText("a\u0000b‮c\n\td  ")).toBe("a b c d");
    expect(launchStatusText(42)).toBe("");
    expect(launchStatusText(" ​ ")).toBe("");
    const long = launchStatusText("x".repeat(500));
    expect([...long]).toHaveLength(LAUNCH_STATUS_MAX_CHARS);
    expect(long.endsWith("…")).toBe(true);
  });
});
