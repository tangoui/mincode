import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElButton } from "element-plus";
import EditorToolbar from "../../../official-plugins/wechat-formatter/ui-src/EditorToolbar.vue";
import PublishCheck from "../../../official-plugins/wechat-formatter/ui-src/PublishCheck.vue";
import ArticlePreview from "../../../official-plugins/wechat-formatter/ui-src/ArticlePreview.vue";
import WorkbenchGallery from "../../../official-plugins/wechat-formatter/ui-src/WorkbenchGallery.vue";
import WechatFormatter from "../../../official-plugins/wechat-formatter/ui-src/WechatFormatter.vue";
import { restoreForClipboard } from "../../../official-plugins/wechat-formatter/ui-src/clipboard.mjs";

const state = vi.hoisted(() => ({ options: null as null | { onReady: () => void }, saved: [] as string[] }));
vi.mock("@mincode/plugin-ui/transport.js", () => ({
  createTransport: (options: { onReady: () => void }) => {
    state.options = options;
    return {
      close: () => {}, pickPath: async () => "",
      clipboard: { writeText: async () => {} },
      call: async (action: string, body: { markdown?: string }) => {
        const article = { id: "0123456789abcdef", revision: 1, title: "Title", markdown: "# Title\n\nparagraph", settings: {}, updatedAt: new Date().toISOString() };
        if (action === "prefs") return { defaults: {} };
        if (action === "articles") return { articles: [article], categories: [], version: 1 };
        if (action === "article") return { article };
        if (action === "save") { state.saved.push(body.markdown ?? ""); return { article }; }
        return {};
      },
    };
  },
}));
const t = (key: string) => key;
describe("formatter workbench controls", () => {
  it("edits the actual article selection and opens pre-copy review without copying early", async () => {
    const wrapper = mount(WechatFormatter, { attachTo: document.body });
    state.options?.onReady(); await flushPromises();
    const editor = wrapper.get("textarea");
    await editor.setValue("hello world");
    (editor.element as HTMLTextAreaElement).setSelectionRange(0, 5);
    await editor.trigger("select");
    const bold = wrapper.findAllComponents(ElButton).find(button => button.attributes("aria-label") === "粗体")!;
    await bold.trigger("click"); await flushPromises();
    expect((editor.element as HTMLTextAreaElement).value).toBe("**hello** world");
    const review = wrapper.findAllComponents(ElButton).find(button => button.text().startsWith("复制前检查"))!;
    await review.trigger("click"); await flushPromises();
    expect(document.body.textContent).toContain("确认并复制");
    wrapper.unmount();
  });
  it("opens the requested platform rather than resetting every menu to Zhihu", async () => {
    const wrapper = mount(WorkbenchGallery, { props: { modelValue: true, kind: "platforms", initialPlatform: "csdn", language: "en", markdown: "# Article", settings: {}, transport: {}, t }, attachTo: document.body });
    await flushPromises();
    expect(wrapper.findComponent({ name: "ElSelect" }).props("modelValue")).toBe("csdn");
    wrapper.unmount();
  });

  it("preserves the editor selection and emits explicit formatting operations", async () => {
    const host = document.createElement("textarea");
    document.body.append(host); host.value = "selected text"; host.focus(); host.setSelectionRange(0, 8);
    const wrapper = mount(EditorToolbar, { props: { t } });
    const bold = wrapper.findAllComponents(ElButton).find(button => button.attributes("aria-label") === "tool_bold")!;
    const event = new MouseEvent("mousedown", { cancelable: true, bubbles: true });
    bold.element.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(host.selectionStart).toBe(0); expect(host.selectionEnd).toBe(8);
    await bold.trigger("click");
    expect(wrapper.emitted("action")?.[0]).toEqual(["bold"]);
    wrapper.unmount(); host.remove();
  });

  it("requires an explicit fresh confirmation click and blocks over-limit articles", async () => {
    const wrapper = mount(PublishCheck, { props: { modelValue: true, t, issues: [{ kind: "local-image", start: 0, end: 9, severity: "warning" }] }, global: { stubs: { teleport: true } } });
    await flushPromises();
    expect(wrapper.emitted("copy")).toBeUndefined();
    const confirm = wrapper.findAllComponents(ElButton).find(button => button.text() === "confirmCopy")!;
    await confirm.trigger("click"); expect(wrapper.emitted("copy")).toHaveLength(1);
    await wrapper.setProps({ issues: [{ kind: "too-large", start: 0, end: 9, severity: "error" }] });
    expect(confirm.attributes("disabled")).toBeDefined();
    wrapper.unmount();
  });

  it("evicts removed preview images and ignores stale loads after article changes", async () => {
    let finish: (value: unknown) => void = () => {};
    const call = vi.fn(() => new Promise(resolve => { finish = resolve; }));
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:stale");
    const wrapper = mount(ArticlePreview, { props: { html: '<section><img src="https://example.com/old.png"></section>', t, transport: { call } } });
    await flushPromises();
    await wrapper.setProps({ html: '<section><p>New article</p></section>' });
    finish({ data: "aW1hZ2U=", size: 5, nextOffset: 5, eof: true, contentType: "image/png" });
    await flushPromises();
    expect(wrapper.find("img").exists()).toBe(false);
    expect(revoke).toHaveBeenCalledWith("blob:stale");
    wrapper.unmount();
  });

  it("loads local images through readAsset and never leaks blob or asset addresses to manual copy", async () => {
    const call = vi.fn(async () => ({ data: "aW1hZ2U=", size: 5, offset: 0, nextOffset: 5, eof: true, contentType: "image/png" }));
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:preview");
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const wrapper = mount(ArticlePreview, { props: { html: '<section><img data-wxf-asset="0123456789abcdef" alt="caption"></section>', loadImages: false, t, transport: { call } } });
    await flushPromises();
    expect(call).toHaveBeenCalledWith("readAsset", expect.objectContaining({ id: "0123456789abcdef" }));
    expect(wrapper.find("img").attributes("src")).toBe("blob:preview");
    const cloned = wrapper.element.cloneNode(true) as HTMLElement;
    restoreForClipboard(cloned);
    expect(cloned.innerHTML).not.toMatch(/blob:|wxf-asset/);
    expect(cloned.textContent).toContain("caption");
    wrapper.unmount();
  });
});
