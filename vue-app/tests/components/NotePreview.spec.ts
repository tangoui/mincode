import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

const navigation = vi.hoisted(() => ({ openWorkspaceLink: vi.fn(), openLocalMarkdownPath: vi.fn() }));

vi.mock("@/services/markdown-link-navigation", () => navigation);

import NotePreview from "@/components/notes/NotePreview.vue";
import { restrictPreviewImages } from "@/domain/notes/preview-images";
import { cachedMarkdownHtml } from "@/services/markdown-render";

const ASSET = `/__notes-assets/${"a".repeat(64)}.png`;

describe("NotePreview", () => {
  it("blocks scripts and external images in preview", () => {
    const content = [
      "<script>alert(1)</script>",
      "",
      "[x](javascript:alert(1))",
      "",
      "![a](https://e.com/a.png)",
      "",
      `![b](${ASSET})`,
    ].join("\n");
    const wrapper = mount(NotePreview, { props: { content } });

    expect(wrapper.find("script").exists()).toBe(false);
    const hrefs = wrapper.findAll("a").map((anchor) => anchor.attributes("href") ?? "");
    expect(hrefs.some((href) => href.trim().toLowerCase().startsWith("javascript:"))).toBe(false);
    const images = wrapper.findAll("img");
    expect(images).toHaveLength(1);
    expect(images[0].attributes("src")).toBe(ASSET);
    const external = wrapper.findAll("a").find((anchor) => anchor.attributes("href") === "https://e.com/a.png");
    expect(external?.text()).toBe("a");
    expect(external?.attributes("rel")).toBe("noreferrer");
  });

  it("opens web links in the built-in browser and never navigates the webview", () => {
    navigation.openWorkspaceLink.mockResolvedValue(undefined);
    const wrapper = mount(NotePreview, { props: { content: "[site](https://example.com/docs)" } });
    const event = new MouseEvent("click", { bubbles: true, cancelable: true });
    wrapper.get("a").element.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(navigation.openWorkspaceLink).toHaveBeenCalledWith("https://example.com/docs");
  });

  it("blocks relative and local path links", () => {
    const wrapper = mount(NotePreview, { props: { content: "[doc](./other.md) [abs](/etc/hosts)" } });
    for (const anchor of wrapper.findAll("a")) {
      const event = new MouseEvent("click", { bubbles: true, cancelable: true });
      anchor.element.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(true);
    }
    expect(navigation.openLocalMarkdownPath).not.toHaveBeenCalled();
    expect(navigation.openWorkspaceLink).not.toHaveBeenCalled();
  });

  it("never submits forms from the note body", () => {
    const content = `<form action="https://e.com/collect"><input name="q" value="v"><button formaction="https://e.com/b">go</button></form>`;
    const wrapper = mount(NotePreview, { props: { content } });
    expect(wrapper.find("form").exists()).toBe(false);
    expect(wrapper.find("[action], [formaction]").exists()).toBe(false);
    expect(wrapper.find("button").text()).toBe("go");
    const submit = new Event("submit", { bubbles: true, cancelable: true });
    wrapper.get("button").element.dispatchEvent(submit);
    expect(submit.defaultPrevented).toBe(true);
  });

  it("does not fill the shared markdown cache while typing", () => {
    const content = `# draft ${Math.random()}`;
    mount(NotePreview, { props: { content } });
    expect(cachedMarkdownHtml(content)).toBeUndefined();
  });
});

describe("restrictPreviewImages", () => {
  it("keeps only exact note asset images and escapes alt text", () => {
    const html = [
      `<img src="${ASSET}" alt="ok" srcset="https://e.com/big.png 2x" onerror="x">`,
      `<img src="/__notes-assets/../secret.png" alt="dots">`,
      `<img src="data:image/png;base64,AAAA" alt="&lt;b&gt;bold&lt;/b&gt;">`,
      `<img src="https://e.com/b.png">`,
    ].join("");
    const container = document.createElement("div");
    container.innerHTML = restrictPreviewImages(html, "Image");

    const images = container.querySelectorAll("img");
    expect(images).toHaveLength(1);
    expect(images[0].getAttribute("src")).toBe(ASSET);
    expect(images[0].hasAttribute("srcset")).toBe(false);
    const anchors = [...container.querySelectorAll("a")];
    expect(anchors.map((anchor) => anchor.getAttribute("href"))).toEqual([
      "/__notes-assets/../secret.png",
      null,
      "https://e.com/b.png",
    ]);
    expect(anchors[1].textContent).toBe("<b>bold</b>");
    expect(anchors[1].querySelector("b")).toBeNull();
    expect(anchors[2].textContent).toBe("Image");
  });

  it("drops other remote resource loaders", () => {
    const html = `<p style="background:url(https://e.com/t.png)">p</p><video src="https://e.com/v.mp4" poster="https://e.com/p.png"></video><picture><source srcset="https://e.com/s.png"></picture><style>body{display:none}</style>`;
    const container = document.createElement("div");
    container.innerHTML = restrictPreviewImages(html, "Image");
    expect(container.querySelector("[style]")).toBeNull();
    expect(container.querySelector("video, audio, source, style")).toBeNull();
  });

  it("strips event handlers and script URLs even if the sanitizer missed them", () => {
    const html = `<a href=" java\tscript:alert(1)" onclick="x()">a</a><img src="${ASSET}" onerror="x()"><script>x()</script>`;
    const container = document.createElement("div");
    container.innerHTML = restrictPreviewImages(html);
    expect(container.querySelector("a")?.hasAttribute("href")).toBe(false);
    expect(container.querySelector("[onclick], [onerror], script")).toBeNull();
  });

  it("removes SVG so image and feImage cannot load remote resources", () => {
    const html = [
      `<p>a</p><svg><image href="https://x/y.png"/></svg>`,
      `<svg><filter><feImage href="https://x/f.png" xlink:href="https://x/g.png"/></filter></svg>`,
      `<svg><use xlink:href="https://x/s.svg#i"/></svg><p>b</p>`,
    ].join("");
    const container = document.createElement("div");
    container.innerHTML = restrictPreviewImages(html, "Image");
    expect(container.querySelector("svg, image, feImage, use")).toBeNull();
    expect(container.innerHTML).not.toContain("https://x/");
    expect(container.textContent).toBe("ab");
  });

  it("keeps href only on links and src only on note asset images", () => {
    const html = `<a href="https://e.com/ok">ok</a><p href="https://x/p" src="https://x/s">p</p><input type="image" src="https://x/i.png"><img src="${ASSET}">`;
    const container = document.createElement("div");
    container.innerHTML = restrictPreviewImages(html, "Image");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("https://e.com/ok");
    expect(container.querySelector("p")?.hasAttribute("href")).toBe(false);
    expect(container.querySelector("p")?.hasAttribute("src")).toBe(false);
    expect(container.querySelector("input")?.hasAttribute("src")).toBe(false);
    expect(container.querySelector("img")?.getAttribute("src")).toBe(ASSET);
  });
});

describe("NotePreview with raw SVG", () => {
  it("never renders external SVG images from the note body", () => {
    const content = `<svg><image href="https://x/y.png"/></svg>\n\n<svg><filter><feImage href="https://x/f.png"/></filter></svg>`;
    const wrapper = mount(NotePreview, { props: { content } });
    expect(wrapper.find("svg").exists()).toBe(false);
    expect(wrapper.html()).not.toContain("https://x/");
  });
});
