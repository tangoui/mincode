import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const service = vi.hoisted(() => ({ countNoteLinks: vi.fn() }));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  isDesktopRuntime: () => true,
}));

import LinkedNotesBadge from "@/components/notes/LinkedNotesBadge.vue";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";

beforeEach(() => {
  service.countNoteLinks.mockReset();
  // 后端省略计数为 0 的对象。
  service.countNoteLinks.mockResolvedValue({ p1: 2 });
});

describe("LinkedNotesBadge", () => {
  it("shows the count, batches ids per type and opens the drawer on click", async () => {
    const drawer = useLinkedNotesDrawer();
    const open = vi.spyOn(drawer, "open").mockResolvedValue();
    const wrapper = mount({
      components: { LinkedNotesBadge },
      template: `<div><LinkedNotesBadge type="ssh" id="p1" name="prod" /><LinkedNotesBadge type="ssh" id="p2" name="dev" /></div>`,
    }, { attachTo: document.body });
    await flushPromises();
    // 计数服务是动态 import 的：等到请求发出并返回。
    await vi.waitFor(() => expect(service.countNoteLinks).toHaveBeenCalled());
    await flushPromises();

    expect(service.countNoteLinks).toHaveBeenCalledTimes(1);
    expect(service.countNoteLinks).toHaveBeenCalledWith("ssh", ["p1", "p2"]);
    const badges = wrapper.findAll(".linked-notes-badge");
    expect(badges).toHaveLength(1);
    expect(badges[0]!.text()).toBe("2");
    expect(badges[0]!.attributes("aria-label")).toBe("2 篇关联笔记");

    await badges[0]!.trigger("click");
    expect(open).toHaveBeenCalledWith({ objectType: "ssh", objectId: "p1", nameSnapshot: "prod" });
    wrapper.unmount();
  });

  it("renders nothing when the count is 0", async () => {
    service.countNoteLinks.mockResolvedValue({});
    const wrapper = mount(LinkedNotesBadge, { props: { type: "http", id: "r1" } });
    await vi.waitFor(() => expect(service.countNoteLinks).toHaveBeenCalledWith("http", ["r1"]));
    await flushPromises();

    expect(wrapper.find(".linked-notes-badge").exists()).toBe(false);
    wrapper.unmount();
  });

  it("refetches counts after links change in the notes store", async () => {
    const wrapper = mount(LinkedNotesBadge, { props: { type: "ssh", id: "p1" } });
    await vi.waitFor(() => expect(wrapper.text()).toBe("2"));

    const { useNotesStore } = await import("@/stores/notes");
    const notes = useNotesStore();
    await flushPromises();
    service.countNoteLinks.mockResolvedValue({ p1: 3 });
    notes.linksVersion++;

    await vi.waitFor(() => expect(wrapper.text()).toBe("3"));
    expect(service.countNoteLinks).toHaveBeenCalledTimes(2);
    expect(service.countNoteLinks).toHaveBeenLastCalledWith("ssh", ["p1"]);
    wrapper.unmount();
  });
});
