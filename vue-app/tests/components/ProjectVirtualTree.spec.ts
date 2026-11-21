// The explorer tree renders only the rows in view, keeps el-tree's lazy-load contract and
// event shapes, and pins expanded ancestors while their subtree scrolls.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { h } from "vue";
import ProjectVirtualTree from "@/components/projects/ProjectVirtualTree.vue";
import type { LazyTreeApi, LazyTreeNode, LoadFunction } from "@/domain/projects/lazy-tree";

interface Entry { id: string; name: string; leaf: boolean }

const folder = (id: string): Entry => ({ id, name: id, leaf: false });
const file = (id: string): Entry => ({ id, name: id, leaf: true });

/** Root "p" with folder "p/big" holding 2000 files. */
const listing: Record<string, Entry[]> = {
  "<root>": [folder("p")],
  p: [folder("p/big"), file("p/readme")],
  "p/big": Array.from({ length: 2000 }, (_, i) => file(`p/big/${i}`)),
};

/** The tree scrolls inside an el-scrollbar (thin app scrollbar, not the native one). */
const SCROLLER = ".project-virtual-tree.el-scrollbar > .el-scrollbar__wrap";

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountTree(expanded: string[] = []) {
  const load = vi.fn((node: LazyTreeNode<Entry>, resolve: (children: Entry[]) => void) => {
    resolve(listing[node.level === 0 ? "<root>" : node.data.id] ?? []);
  });
  wrapper = mount(ProjectVirtualTree, {
    // mount() cannot infer the generic parameter; the component is instantiated with object.
    props: { load: load as unknown as LoadFunction<object>, defaultExpandedKeys: expanded, rowHeight: 20, overscan: 2 },
    slots: { default: ({ data }: { data: object }) => h("span", { class: "label" }, (data as Entry).name) },
    attachTo: document.body,
  });
  const scroller = wrapper.get(SCROLLER).element as HTMLElement;
  Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
  await wrapper.get(SCROLLER).trigger("scroll");
  await flushPromises();
  return { load, wrapper, scroller, api: wrapper.vm as unknown as LazyTreeApi<Entry> };
}

const listLabels = (w: VueWrapper) => w.findAll(".project-virtual-tree__list > .project-virtual-tree__row .label").map((node) => node.text());

describe("ProjectVirtualTree", () => {
  it("renders a window of rows, not every expanded entry", async () => {
    const { wrapper } = await mountTree(["p", "p/big"]);
    // The focusable tree is the scroll layer itself, so arrow keys and focus styles target it.
    expect(wrapper.get(SCROLLER).attributes("tabindex")).toBe("0");
    expect(wrapper.find("[role=tree]").exists()).toBe(true);
    const labels = listLabels(wrapper);
    expect(labels[0]).toBe("p");
    expect(labels.length).toBeLessThan(40);
    expect(wrapper.get(".project-virtual-tree__list").attributes("style")).toContain(`height: ${(2 + 2000 + 1) * 20}px`);
  });

  it("re-renders default-expanded folders whose children arrive asynchronously", async () => {
    // Nodes created inside a load must expand through the reactive tree, or the view stays stale.
    const load = (node: LazyTreeNode<Entry>, resolve: (children: Entry[]) => void) => {
      const children = listing[node.level === 0 ? "<root>" : node.data.id] ?? [];
      setTimeout(() => resolve(children), 0);
    };
    wrapper = mount(ProjectVirtualTree, {
      props: { load: load as unknown as LoadFunction<object>, defaultExpandedKeys: ["p"], rowHeight: 20 },
      slots: { default: ({ data }: { data: object }) => h("span", { class: "label" }, (data as Entry).name) },
    });
    const scroller = wrapper.get(SCROLLER).element as HTMLElement;
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    await wrapper.get(SCROLLER).trigger("scroll");
    await new Promise((resolve) => setTimeout(resolve, 10));
    await flushPromises();
    expect(listLabels(wrapper)).toEqual(["p", "p/big", "p/readme"]);
  });

  it("loads lazily on click and emits el-tree shaped events", async () => {
    const { wrapper, load } = await mountTree();
    expect(load).toHaveBeenCalledTimes(1);
    await wrapper.get(".project-virtual-tree__list > .project-virtual-tree__row").trigger("click");
    await flushPromises();
    expect(load).toHaveBeenCalledTimes(2);
    expect(listLabels(wrapper)).toEqual(["p", "p/big", "p/readme"]);
    const click = wrapper.emitted("node-click")?.[0] as [Entry, LazyTreeNode<Entry>, MouseEvent];
    expect(click[0].id).toBe("p");
    expect(click[1].level).toBe(1);
    expect(wrapper.emitted("node-expand")?.[0]?.[0]).toMatchObject({ id: "p" });

    await wrapper.findAll(".project-virtual-tree__list > .project-virtual-tree__row")[2].trigger("contextmenu");
    const menu = wrapper.emitted("node-contextmenu")?.[0] as [MouseEvent, Entry];
    expect(menu[1].id).toBe("p/readme");
  });

  it("caret toggles without a node-click", async () => {
    const { wrapper } = await mountTree();
    await wrapper.get(".project-virtual-tree__caret").trigger("click");
    await flushPromises();
    expect(listLabels(wrapper)).toEqual(["p", "p/big", "p/readme"]);
    expect(wrapper.emitted("node-click")).toBeUndefined();
  });

  it("exposes el-tree compatible node operations", async () => {
    const { wrapper, api } = await mountTree(["p"]);
    const p = api.getNode("p")!;
    p.insertChild({ data: file("p/new") }, 0);
    api.remove(api.getNode("p/readme")!);
    await flushPromises();
    expect(listLabels(wrapper)).toEqual(["p", "p/new", "p/big"]);
    expect(api.root.childNodes.map((node) => node.data.id)).toEqual(["p"]);
    api.setCurrentKey("p/big");
    await flushPromises();
    expect(wrapper.get(".project-virtual-tree__row.is-current .label").text()).toBe("p/big");
  });

  it("pins expanded ancestors once scrolled and scrolls a node into the centre", async () => {
    const { wrapper, scroller, api } = await mountTree(["p", "p/big"]);
    scroller.scrollTop = 6 + 500 * 20;
    await wrapper.get(SCROLLER).trigger("scroll");
    expect(wrapper.findAll(".project-virtual-tree__row.is-pinned .label").map((node) => node.text())).toEqual(["p", "p/big"]);
    // Pinned rows are full height (list rows leave a 1px gap) so rows underneath never show through.
    expect(wrapper.findAll(".project-virtual-tree__row.is-pinned").map((row) => row.attributes("style"))).toEqual([
      expect.stringContaining("top: 0px; height: 20px;"),
      expect.stringContaining("top: 20px; height: 20px;"),
    ]);
    expect(listLabels(wrapper)).toContain("p/big/498");

    api.scrollToNode("p/big/10");
    await flushPromises();
    expect(scroller.scrollTop).toBe(6 + 12 * 20 - (200 - 20) / 2);
  });

  it("moves the selection with arrow keys and opens with Enter", async () => {
    const { wrapper } = await mountTree();
    const tree = wrapper.get(SCROLLER);
    await tree.trigger("click"); // focus target; no row selected yet
    await wrapper.get(".project-virtual-tree__list > .project-virtual-tree__row").trigger("click");
    await flushPromises();
    await tree.trigger("keydown", { key: "ArrowDown" });
    await tree.trigger("keydown", { key: "ArrowDown" });
    expect(wrapper.get(".project-virtual-tree__row.is-current .label").text()).toBe("p/readme");
    await tree.trigger("keydown", { key: "Enter" });
    const clicks = wrapper.emitted("node-click") as [Entry][];
    expect(clicks[clicks.length - 1][0].id).toBe("p/readme");
    await tree.trigger("keydown", { key: "ArrowLeft" });
    expect(wrapper.get(".project-virtual-tree__row.is-current .label").text()).toBe("p");
  });
});
