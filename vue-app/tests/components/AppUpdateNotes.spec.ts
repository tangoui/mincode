import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import AppUpdateNotes from "@/components/settings/AppUpdateNotes.vue";
import { renderMarkdownHtml } from "@/services/markdown-render";

const opened = vi.hoisted(() => [] as string[]);
vi.mock("@/services/markdown-link-navigation", () => ({
  openWorkspaceLink: async (url: string) => { opened.push(url); },
}));
// happy-dom's DOMPurify neither strips attributes nor keeps the first element's tag, so sanitizing itself is covered
// by markdown-render-cache.test.mjs; here the notes must go through that shared marked + DOMPurify pipeline.
vi.mock("@/services/markdown-render", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/markdown-render")>();
  return { renderMarkdownHtml: vi.fn(actual.renderMarkdownHtml) };
});

describe("app update notes", () => {
  it("renders release notes as markdown through the sanitizing pipeline", () => {
    const notes = "更新说明\n\n# MinCode 0.1.0\n\n## 终端\n\n- 一\n- 二";
    const wrapper = mount(AppUpdateNotes, { props: { notes } });
    expect(renderMarkdownHtml).toHaveBeenCalledWith(notes);
    expect(wrapper.find("h1").text()).toBe("MinCode 0.1.0");
    expect(wrapper.find("h2").text()).toBe("终端");
    expect(wrapper.findAll("li")).toHaveLength(2);
    expect(wrapper.text()).not.toContain("#");
  });

  it("opens http links outside the settings page and blocks every other navigation", () => {
    const wrapper = mount(AppUpdateNotes, { props: { notes: "说明\n\n[发布页](https://example.com/r) [本地](/etc/passwd) [锚点](#x)" } });
    const links = wrapper.findAll("a");
    expect(links).toHaveLength(3);
    const clicks = links.map((link) => {
      const click = new MouseEvent("click", { bubbles: true, cancelable: true });
      link.element.dispatchEvent(click);
      return click.defaultPrevented;
    });
    expect(clicks).toEqual([true, true, true]);
    expect(opened).toEqual(["https://example.com/r"]);
  });
});
