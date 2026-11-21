import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";
import VirtualTextViewer from "@/components/ui/VirtualTextViewer.vue";
import { useVueI18n } from "@/i18n/messages";

// happy-dom has no layout: give the viewport a height and the character probe a width so
// the viewer windows its rows the way it does in the WebView.
function layout(wrapper: VueWrapper, { height = 190, width = 800 } = {}) {
  const viewport = wrapper.find(".app-virtual-text__viewport").element as HTMLElement;
  Object.defineProperty(viewport, "clientHeight", { configurable: true, value: height });
  Object.defineProperty(viewport, "clientWidth", { configurable: true, value: width });
  const probe = wrapper.find(".app-virtual-text__probe").element as HTMLElement;
  probe.getBoundingClientRect = () => ({ width: 80 }) as DOMRect; // 8 px per character
  return viewport;
}

async function scrollTo(viewport: HTMLElement, top: number) {
  viewport.scrollTop = top;
  viewport.dispatchEvent(new Event("scroll"));
  await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
  await nextTick();
}

function rowTexts(wrapper: VueWrapper) {
  return wrapper.findAll(".app-virtual-text__text").map((row) => row.text());
}

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountViewer(text: string, size?: { height?: number; width?: number }) {
  wrapper = mount(VirtualTextViewer, { props: { text, label: "Body" }, attachTo: document.body });
  const viewport = layout(wrapper, size);
  // What the ResizeObserver does once the viewer has a real size.
  (wrapper.vm as unknown as { remeasure: () => void }).remeasure();
  await nextTick();
  return viewport;
}

const lines = (n: number) => Array.from({ length: n }, (_, i) => `line ${i}`).join("\n");

describe("VirtualTextViewer", () => {
  it("renders only the rows near the viewport of a large text", async () => {
    await mountViewer(lines(100_000));
    const rendered = wrapper!.findAll(".app-virtual-text__row").length;
    expect(rendered).toBeGreaterThan(0);
    expect(rendered).toBeLessThan(60);
    expect(rowTexts(wrapper!)[0]).toBe("line 0");
    const spacer = wrapper!.find(".app-virtual-text__spacer").element as HTMLElement;
    expect(spacer.style.height).toBe(`${100_000 * 19}px`);
  });

  it("shows the rows of the new position after scrolling", async () => {
    const viewport = await mountViewer(lines(100_000));
    await scrollTo(viewport, 50_000 * 19);
    const texts = rowTexts(wrapper!);
    expect(texts).toContain("line 50000");
    expect(texts).not.toContain("line 0");
    expect(wrapper!.findAll(".app-virtual-text__row").length).toBeLessThan(80);
  });

  it("numbers lines, and leaves wrapped continuation rows unnumbered", async () => {
    await mountViewer(`short\n${"x".repeat(250)}\nend`, { width: 800 });
    const numbers = wrapper!.findAll(".app-virtual-text__number").map((cell) => cell.text());
    expect(numbers[0]).toBe("1");
    expect(numbers[1]).toBe("2");
    expect(numbers.slice(2, -1).every((n) => n === "")).toBe(true);
    expect(numbers.at(-1)).toBe("3");
    expect(rowTexts(wrapper!).slice(1, -1).join("")).toBe("x".repeat(250));
  });

  it("starts at the top again when the text is replaced", async () => {
    const viewport = await mountViewer(lines(10_000));
    await scrollTo(viewport, 5000 * 19);
    await wrapper!.setProps({ text: lines(20_000) });
    await nextTick();
    expect(viewport.scrollTop).toBe(0);
    expect(rowTexts(wrapper!)[0]).toBe("line 0");
  });

  it("copies the source text of a selection, with the line breaks the rows hide", async () => {
    const text = "alpha\r\nbravo\ncharlie";
    await mountViewer(text);
    const rows = wrapper!.findAll(".app-virtual-text__text");
    // Vue's fragment anchors are empty text nodes: take the one holding the row's text.
    const textNode = (element: Element) => {
      const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) if (node.textContent) return node;
      throw new Error("row has no text");
    };
    const range = document.createRange();
    range.setStart(textNode(rows[0]!.element), 2);
    range.setEnd(textNode(rows[2]!.element), 3);
    const selection = document.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    const data = new DataTransfer();
    const event = new ClipboardEvent("copy", { clipboardData: data, cancelable: true, bubbles: true });
    wrapper!.find(".app-virtual-text__viewport").element.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(data.getData("text/plain")).toBe("pha\r\nbravo\ncha");
  });

  it("copies the whole text after select-all, not only the rows in view", async () => {
    const text = lines(100_000);
    await mountViewer(text);
    const viewport = wrapper!.find(".app-virtual-text__viewport");
    await viewport.trigger("keydown", { key: "a", metaKey: true });
    const data = new DataTransfer();
    const event = new ClipboardEvent("copy", { clipboardData: data, cancelable: true, bubbles: true });
    viewport.element.dispatchEvent(event);
    expect(data.getData("text/plain")).toBe(text);
    // A click ends the select-all: the next copy is the mouse selection again.
    await viewport.trigger("pointerdown");
    const after = new DataTransfer();
    document.getSelection()?.removeAllRanges();
    viewport.element.dispatchEvent(new ClipboardEvent("copy", { clipboardData: after, cancelable: true, bubbles: true }));
    expect(after.getData("text/plain")).toBe("");
  });

  it("has an accessible name and can take focus for keyboard scrolling", async () => {
    await mountViewer("x");
    const viewport = wrapper!.find(".app-virtual-text__viewport");
    expect(viewport.attributes("aria-label")).toBe("Body");
    expect(viewport.attributes("tabindex")).toBe("0");
  });
});

describe("VirtualTextViewer find", () => {
  async function openFind(query: string) {
    const viewport = wrapper!.find(".app-virtual-text__viewport");
    await viewport.trigger("keydown", { key: "f", metaKey: true });
    await nextTick();
    const input = wrapper!.find(".app-virtual-text__search input");
    expect(input.exists()).toBe(true);
    await input.setValue(query);
    await input.trigger("keydown", { key: "Enter" });
    await nextTick();
    return input;
  }
  const status = () => wrapper!.find(".app-virtual-text__search-status").text();
  const current = () => wrapper!.find(".app-virtual-text__match.is-current");

  it("finds a match far outside the rendered rows and scrolls it into view", async () => {
    const viewport = await mountViewer(lines(100_000));
    await openFind("line 87654");
    expect(status()).toMatch(/^1[/ ]/);
    expect(viewport.scrollTop).toBeGreaterThan(80_000 * 19);
    await nextTick();
    expect(current().exists()).toBe(true);
    expect(current().text()).toBe("line 87654");
  });

  it("is case-insensitive, counts all matches and cycles with next and previous", async () => {
    await mountViewer("Alpha\nbeta ALPHA\nalpha gamma");
    const input = await openFind("alpha");
    const count = (current: number) => useVueI18n().t("textSearch.count", { current, total: 3 });
    // The Enter in openFind ran the search: on the first match.
    expect(status()).toBe(count(1));
    await input.trigger("keydown", { key: "Enter" });
    expect(status()).toBe(count(2));
    await input.trigger("keydown", { key: "Enter" });
    expect(status()).toBe(count(3));
    await input.trigger("keydown", { key: "Enter" });
    expect(status()).toBe(count(1));
    await input.trigger("keydown", { key: "Enter", shiftKey: true });
    expect(status()).toBe(count(3));
    await input.trigger("keydown", { key: "ArrowUp" });
    expect(status()).toBe(count(2));
    expect(wrapper!.findAll(".app-virtual-text__match")).toHaveLength(3);
  });

  it("highlights a match that crosses a wrapped row on both rows", async () => {
    await mountViewer(`${"x".repeat(90)}NEEDLE${"y".repeat(10)}`, { width: 800 });
    await openFind("needle");
    const parts = wrapper!.findAll(".app-virtual-text__match").map((mark) => mark.text());
    expect(parts.join("")).toBe("NEEDLE");
  });

  it("reports no results and clears highlights when closed with Escape", async () => {
    await mountViewer("one\ntwo");
    const input = await openFind("zzz");
    expect(status()).toBe(useVueI18n().t("textSearch.noResults"));
    await input.setValue("two");
    await input.trigger("input");
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper!.findAll(".app-virtual-text__match").length).toBeGreaterThan(0);
    await input.trigger("keydown", { key: "Escape" });
    await nextTick();
    expect(wrapper!.find(".app-virtual-text__search").exists()).toBe(false);
    expect(wrapper!.findAll(".app-virtual-text__match")).toHaveLength(0);
  });

  it("searches the new text when the text is replaced", async () => {
    await mountViewer("apple\nbanana");
    await openFind("banana");
    expect(wrapper!.findAll(".app-virtual-text__match")).toHaveLength(1);
    await wrapper!.setProps({ text: "banana banana banana" });
    await nextTick();
    expect(wrapper!.findAll(".app-virtual-text__match")).toHaveLength(3);
  });
});
