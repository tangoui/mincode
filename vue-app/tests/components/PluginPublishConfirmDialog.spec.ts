import { mount, flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ElButton, ElProgress, ElTable } from "element-plus";
import PluginPublishConfirmDialog from "@/components/plugins/development/PluginPublishConfirmDialog.vue";
import type { PluginPublishRecord } from "@/domain/plugins/publish-summary";

const actions = vi.hoisted(() => ({
  confirm: vi.fn(() => Promise.resolve()), reject: vi.fn(() => Promise.resolve()),
  cancel: vi.fn(() => Promise.resolve()), refresh: vi.fn(() => Promise.resolve()), settings: vi.fn(),
}));
vi.mock("@/services/plugin-publish-service", () => ({
  confirmPluginPublish: actions.confirm, rejectPluginPublish: actions.reject,
  cancelPluginPublish: actions.cancel, refreshPluginPublish: actions.refresh,
}));
vi.mock("@/services/settings-navigation", () => ({ requestOpenSettings: actions.settings }));

const record: PluginPublishRecord = {
  id: "publish-1", status: "pending_confirmation", step: "lookup", pluginId: "demo.plugin", pluginName: "Demo",
  version: "1.0.0", releaseDir: "/project/release", account: "dev@example.com", newPlugin: true, existingDraft: false,
  submit: true, releaseNotes: "notes", expiresAt: "2026-11-04T10:00:00Z", metadataChanges: [],
  packages: ["darwin/arm64", "darwin/amd64", "windows/amd64", "linux/amd64"].map(target => ({
    os: target.split("/")[0]!, arch: target.split("/")[1]!, file: target + ".min",
    size: 100, sha256: "a".repeat(64), state: "pending", percent: 0,
  })),
};
function render(status: PluginPublishRecord["status"] = "pending_confirmation") {
  return mount(PluginPublishConfirmDialog, {
    props: { record: { ...record, status }, queued: 0 },
    global: { stubs: { ElDialog: { template: "<section><slot/><slot name='footer'/></section>" } } },
  });
}
afterEach(() => vi.clearAllMocks());

describe("publish confirmation", () => {
  it("shows all four packages and only confirms the id once while a command is pending", async () => {
    let done: () => void = () => undefined;
    actions.confirm.mockImplementationOnce(() => new Promise<void>(resolve => { done = resolve; }));
    const w = render();
    await flushPromises();
    expect(w.findComponent(ElTable).findAll(".el-table__row")).toHaveLength(4);
    expect(w.find('input[type="checkbox"]').exists()).toBe(false);
    const submit = w.findAllComponents(ElButton).find(b => b.text() === "提交")!;
    await submit.trigger("click");
    await submit.trigger("click");
    expect(actions.confirm).toHaveBeenCalledExactlyOnceWith("publish-1");
    done();
    await flushPromises();
    w.unmount();
  });

  it("opens cloud settings and provides the manual login fallback", async () => {
    const w = render("awaiting_login");
    await w.findAllComponents(ElButton).find(b => b.text() === "去登录")!.trigger("click");
    expect(actions.settings).toHaveBeenCalledExactlyOnceWith("cloud");
    await w.findAllComponents(ElButton).find(b => b.text() === "我已登录")!.trigger("click");
    expect(actions.refresh).toHaveBeenCalledExactlyOnceWith("publish-1");
    w.unmount();
  });

  it("shows four progress bars and cancel while running", async () => {
    const w = render("running");
    await flushPromises();
    expect(w.findAllComponents(ElProgress)).toHaveLength(4);
    await w.findAllComponents(ElButton).find(b => b.text() === "取消")!.trigger("click");
    expect(actions.cancel).toHaveBeenCalledExactlyOnceWith("publish-1");
    w.unmount();
  });

  it("localizes the fixed review result instead of showing the protocol enum", async () => {
    const w = render("succeeded");
    await w.setProps({ record: { ...record, status: "succeeded", result: {
      releaseId: 7, reviewStatus: "pending", developerUrl: "", uploaded: [], skipped: [],
    } } });
    expect(w.text()).toContain("待审核");
    w.unmount();
  });
});

it("shows a retryable localized staging-limit error without consuming confirmation", async () => {
  actions.confirm.mockRejectedValueOnce(new Error("publish_busy"));
  const w = render();
  await w.findAllComponents(ElButton).find(b => b.text() === "提交")!.trigger("click");
  await flushPromises();
  expect(w.text()).toContain("文件暂存繁忙，请稍后重试");
  expect(w.findAllComponents(ElButton).find(b => b.text() === "提交")!.props("disabled")).toBe(false);
  w.unmount();
});

it("preparing is cancelable but never confirms, even before package staging", async () => {
  const w = render("preparing");
  await w.setProps({ record: { ...record, status: "preparing", packages: [] } });
  expect(w.text()).toContain("正在准备文件并查询云端信息");
  expect(w.findAllComponents(ElButton).some(b => b.text() === "提交")).toBe(false);
  await w.findAllComponents(ElButton).find(b => b.text() === "取消")!.trigger("click");
  expect(actions.cancel).toHaveBeenCalledExactlyOnceWith("publish-1");
  expect(actions.confirm).not.toHaveBeenCalled();
  w.unmount();
});
