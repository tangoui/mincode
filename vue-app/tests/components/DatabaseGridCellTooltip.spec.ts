// Result-grid cells are plain text; one tooltip per grid follows the hovered cell. A tooltip
// component per cell (hundreds mounted and destroyed on every scroll frame) made scrolling a
// wide result jank at ~80–100 ms per frame.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { ElTooltip } from "element-plus";
import VirtualResultGrid from "@/components/database/VirtualResultGrid.vue";
import DatabaseGridCellTooltip from "@/components/database/DatabaseGridCellTooltip.vue";

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

/** Makes the element report a clipped value, as the real layout would for a long cell. */
function clip(element: Element, clipped: boolean) {
  Object.defineProperty(element, "clientWidth", { configurable: true, value: 100 });
  Object.defineProperty(element, "scrollWidth", { configurable: true, value: clipped ? 300 : 100 });
}

const long = "a fairly long cell value that is clipped by its column width";
const columns = [{ name: "id", index: 0 }, { name: "note", index: 1 }];
const rows = Array.from({ length: 30 }, (_, i) => ({ row: [i, `${long} ${i}`], originalIndex: i }));

describe("result grid cell tooltips", () => {
  it("mounts one tooltip for the grid, not one per cell", async () => {
    wrapper = mount(VirtualResultGrid, {
      props: { columns, rows, fieldName: "Field", mode: "query", formatCell: (value: unknown) => String(value) },
      attachTo: document.body,
    });
    await flushPromises();
    // happy-dom has no layout, so the virtual grid renders only its first window of cells.
    const cells = wrapper.findAll(".db-grid-cell-text");
    expect(cells.length).toBeGreaterThan(2);
    expect(cells[0].attributes("data-cell-row")).toBeDefined();
    expect(cells[0].attributes("data-cell-column")).toBeDefined();
    expect(wrapper.findAllComponents(ElTooltip).length).toBeLessThanOrEqual(1);
    expect(wrapper.findAllComponents(DatabaseGridCellTooltip).length).toBe(1);
  });

  it("shows the full value of a clipped cell after hovering, and nothing for an unclipped one", async () => {
    vi.useFakeTimers();
    wrapper = mount(VirtualResultGrid, {
      props: { columns, rows, fieldName: "Field", mode: "query", formatCell: (value: unknown) => String(value) },
      attachTo: document.body,
    });
    await flushPromises();
    const cell = wrapper.findAll(".db-grid-cell-text").find((node) => node.attributes("data-cell-column") === "note")!;
    const row = Number(cell.attributes("data-cell-row"));
    const tooltip = () => wrapper!.findComponent(ElTooltip);

    clip(cell.element, false);
    await cell.trigger("mouseover");
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(tooltip().props("visible")).toBe(false);

    await cell.trigger("mouseout");
    clip(cell.element, true);
    await cell.trigger("mouseover");
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(tooltip().props("visible")).toBe(true);
    expect(document.body.textContent).toContain(`${long} ${row}`);

    // Scrolling hides it at once.
    wrapper.element.dispatchEvent(new Event("wheel", { bubbles: true }));
    await flushPromises();
    expect(tooltip().props("visible")).toBe(false);
  });
});
