import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import AiMessageBubble from "@/components/ai/AiMessageBubble.vue";

const { invoke, copy, openWorkspaceLink } = vi.hoisted(() => ({
  invoke: vi.fn(),
  copy: vi.fn(),
  openWorkspaceLink: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: invoke,
  copyText: copy,
  isDesktopRuntime: () => true,
}));
vi.mock("@/services/markdown-link-navigation", () => ({
  openWorkspaceLink,
}));

const files = new Set([
  "/tmp/中文 image.png",
  "/tmp/with spaces.txt",
  "/project/src/main.ts",
  "/home/test/notes.md",
  "C:/Users/Test/file.txt",
  "/tmp/中文 duplicate.png",
  "/tmp/folder",
  "/tmp/build.zip",
  "/tmp/main.go",
  ...Array.from({ length: 6 }, (_, i) => `/tmp/file${i}.txt`),
]);
const imageData = "data:image/png;base64,iVBORw0KGgo=";

beforeEach(() => {
  vi.clearAllMocks();
  invoke.mockImplementation((command: string, { path }: { path?: string }) => {
    if (command === "get_user_home_dir") return Promise.resolve("/home/test");
    if (command === "local_path_info") {
      return Promise.resolve({
        exists: files.has(path ?? ""), isFile: path !== "/tmp/folder", isDir: path === "/tmp/folder",
        path: path === "/tmp/中文 duplicate.png" ? "/tmp/中文 image.png" : path,
        mime: path?.endsWith(".png") ? "image/png" : "text/plain", size: 5,
      });
    }
    if (command === "local_image_thumbnail") return Promise.resolve(imageData);
    return Promise.resolve();
  });
});

describe("AI reply file cards", () => {
  it("shows up to six unique files in reply order and uses the verified path for actions", async () => {
    const content = [
      "`/tmp/中文 image.png`", "`/tmp/中文 duplicate.png`", "`/tmp/folder`", "`/tmp/missing.txt`",
      ...Array.from({ length: 6 }, (_, i) => `\`/tmp/file${i}.txt\``),
    ].join("\n\n");
    const wrapper = mount(AiMessageBubble, { props: {
      message: { id: "assistant", role: "assistant", content },
      fileDirectory: "",
      emptyMessageLabel: "", metadataLabel: "", expandLabel: "", collapseLabel: "",
      copyLabel: "", copiedLabel: "", copyFailedLabel: "", copyImageLabel: "",
      copiedImageLabel: "", copyImageFailedLabel: "",
    } });
    await flushPromises();
    const cards = wrapper.findAll(".ai-message-file-card");
    expect(cards.map((card) => card.get(".ai-message-file-card__name").text())).toEqual([
      "中文 image.png", "file0.txt", "file1.txt", "file2.txt", "file3.txt", "file4.txt",
    ]);
    expect(cards[0].get("img").attributes("src")).toBe(imageData);
    expect(cards[1].text()).toContain("5 B");
    await cards[0].get(".ai-message-file-card__open").trigger("click");
    expect(invoke).toHaveBeenCalledWith("local_path_open", { path: "/tmp/中文 image.png", reveal: false });
    await cards[1].get(".ai-message-file-card__reveal").trigger("click");
    expect(invoke).toHaveBeenCalledWith("local_path_open", { path: "/tmp/file0.txt", reveal: true });
    await cards[1].get(".ai-message-file-card__copy").trigger("click");
    expect(copy).toHaveBeenCalledWith("/tmp/file0.txt");
    wrapper.unmount();
  });

  it("shows file-type icons for non-image files", async () => {
    const wrapper = mount(AiMessageBubble, { props: {
      message: { id: "assistant", role: "assistant", content: "`/tmp/build.zip`\n\n`/tmp/main.go`" },
      fileDirectory: "",
      emptyMessageLabel: "", metadataLabel: "", expandLabel: "", collapseLabel: "",
      copyLabel: "", copiedLabel: "", copyFailedLabel: "", copyImageLabel: "",
      copiedImageLabel: "", copyImageFailedLabel: "",
    } });
    await flushPromises();
    const icons = wrapper.findAll(".ai-message-file-card__icon .project-file-icon").map((icon) => icon.classes().find((name) => name.startsWith("project-file-icon--")));
    expect(icons).toEqual(["project-file-icon--archive", "project-file-icon--code"]);
    wrapper.unmount();
  });
});

describe("paths in AI Markdown", () => {
  it("links existing paths across Markdown, inline code and prose", async () => {
    const wrapper = mount(MarkdownPreview, { props: {
      inline: true,
      aiFileDirectory: "/project",
      content: [
        "Created `/tmp/中文 image.png`, /tmp/with spaces.txt.",
        "[source](src/main.ts), `~/notes.md` and [Windows](<C:\\Users\\Test\\file.txt>).",
        "[absent](/tmp/missing.txt), `/tmp/missing.txt` and [web](https://example.test).",
        "```sh\n/tmp/with spaces.txt\n```",
      ].join("\n\n"),
    } });
    await flushPromises();
    expect(wrapper.findAll("a").map((anchor) => anchor.text())).toEqual([
      "/tmp/中文 image.png", "/tmp/with spaces.txt", "source", "~/notes.md", "Windows", "web",
    ]);
    expect(wrapper.text()).toContain("absent");
    expect(wrapper.text()).toContain("/tmp/missing.txt");
    expect(invoke).not.toHaveBeenCalledWith("local_path_open", expect.anything());
    await wrapper.get("a").trigger("click");
    expect(invoke).toHaveBeenCalledWith("local_path_open", { path: "/tmp/中文 image.png", reveal: false });
    wrapper.unmount();
  });

  it("lists local Markdown images only in the file cards", async () => {
    const wrapper = mount(MarkdownPreview, { props: {
      inline: true,
      aiFileDirectory: "/project",
      content: "Images:\n\n![first](</tmp/中文 image.png>)\n\n![](file:///tmp/%E4%B8%AD%E6%96%87%20duplicate.png)\n\n![gone](/tmp/missing.png)\n\n![web](https://example.test/a.png)",
    } });
    await flushPromises();
    expect(wrapper.findAll("img").map((image) => image.attributes("src"))).toEqual(["https://example.test/a.png"]);
    expect(wrapper.findAll("a[data-ai-file-link]")).toHaveLength(0);
    expect(wrapper.text()).toContain("gone");
    expect(wrapper.text()).not.toContain("first");
    expect(wrapper.findAll("p").filter((p) => !p.text().trim() && !p.find("img").exists())).toHaveLength(0);
    expect(wrapper.emitted("localFiles")?.at(-1)?.[0]).toHaveLength(2);
    wrapper.unmount();
  });

  it("drops a paragraph left with only line breaks after removing local images", async () => {
    const wrapper = mount(MarkdownPreview, { props: {
      inline: true,
      aiFileDirectory: "/project",
      content: "Before\n\n![1](</tmp/中文 image.png>)\n![2](</tmp/with spaces.txt>)\n\nAfter",
    } });
    await flushPromises();
    expect(wrapper.text()).toContain("Before");
    expect(wrapper.text()).toContain("After");
    expect(wrapper.findAll("p").filter((p) => !p.text().trim())).toHaveLength(0);
    expect(wrapper.findAll("br")).toHaveLength(0);
    wrapper.unmount();
  });

  it("preserves non-AI web navigation without opening local paths", async () => {
    const wrapper = mount(MarkdownPreview, { props: {
      inline: true,
      content: "[source](src/main.ts) and [web](https://example.test)",
    } });
    const [source, web] = wrapper.findAll("a");
    await source.trigger("click");
    await web.trigger("click");
    expect(openWorkspaceLink).toHaveBeenCalledWith("https://example.test");
    expect(invoke).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("adds a file menu and reveals the verified path", async () => {
    const wrapper = mount(MarkdownPreview, {
      props: { inline: true, aiFileDirectory: "", content: "`/tmp/with spaces.txt`" },
      global: { stubs: {
        AppContextMenu: { props: { open: Boolean }, template: '<div v-if="open"><slot /></div>' },
        ElDropdownItem: { template: '<button type="button"><slot /></button>' },
      } },
    });
    await flushPromises();
    await wrapper.get(".ai-markdown-file-menu-button").trigger("click");
    await wrapper.get(".ai-markdown-file-reveal").trigger("click");
    expect(invoke).toHaveBeenCalledWith("local_path_open", { path: "/tmp/with spaces.txt", reveal: true });
    wrapper.unmount();
  });
});
