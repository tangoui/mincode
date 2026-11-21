import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { ElMessage, ElMessageBox, ElTreeSelect } from "element-plus";
import type { NoteFolder, NoteSummary, NoteTree } from "@/domain/notes/types";

const service = vi.hoisted(() => ({
  getNoteTree: vi.fn(),
  searchNotes: vi.fn(),
  importNoteFiles: vi.fn(),
  importNoteFolder: vi.fn(),
  exportNotes: vi.fn(),
  listAllNoteLinks: vi.fn(),
  createNote: vi.fn(),
  moveNote: vi.fn(),
  moveNoteFolder: vi.fn(),
}));
const bridge = vi.hoisted(() => ({ chooseNativeFiles: vi.fn(), chooseNativeDirectory: vi.fn() }));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...bridge,
}));

const linkSources = vi.hoisted(() => ({ unavailable: new Set<string>() }));

// 关联分组的名称与状态来自各 app store 组装的快照；这里用固定快照。
vi.mock("@/composables/use-note-link-snapshot", async () => {
  const { computed } = await import("vue");
  const snapshot = {
    ssh: new Map([["ssh-1", "prod web"]]),
    database: new Map(),
    http: new Map(),
    httpCollections: new Map(),
    currentProjects: new Map(),
    otherWorkspaceProjects: new Map(),
    currentAi: new Map(),
    currentScopeId: "scope-a",
    liveScopeIds: new Set(["scope-a", "scope-b"]),
    workspaceNames: new Map([["scope-b", "Beta"]]),
    get unavailable() {
      return linkSources.unavailable;
    },
  };
  return { useLinkObjectSnapshot: () => computed(() => snapshot), openLinkTarget: vi.fn() };
});

import NotesSidebar from "@/components/notes/NotesSidebar.vue";
import { useNotesStore } from "@/stores/notes";

function folder(id: string, name: string, parentId = "", position = 1): NoteFolder {
  return { id, parentId, name, position, createdAt: 1, updatedAt: 1 };
}

function summary(id: string, title: string, extra: Partial<NoteSummary> = {}): NoteSummary {
  return { id, folderId: "", title, favorite: false, position: 1, updatedAt: 1, tags: [], ...extra };
}

const baseTree = (): NoteTree => ({
  folders: [folder("f1", "运维")],
  notes: [
    summary("n1", "部署步骤", { folderId: "f1" }),
    summary("n2", "随记", { position: 2, favorite: true, updatedAt: 5 }),
    summary("n3", "周报", { position: 3, tags: ["工作"] }),
  ],
});

let wrapper: VueWrapper | undefined;

async function mountSidebar(tree: NoteTree = baseTree()) {
  service.getNoteTree.mockResolvedValue(tree);
  await useNotesStore().loadTree();
  wrapper = mount(NotesSidebar, { props: { selectedId: "" }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function buttonByLabel(label: string) {
  return wrapper!.find(`button[aria-label="${label}"]`);
}

beforeEach(() => {
  for (const fn of [...Object.values(service), ...Object.values(bridge)]) fn.mockReset();
  service.listAllNoteLinks.mockResolvedValue([]);
  service.searchNotes.mockResolvedValue([]);
  linkSources.unavailable = new Set();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

describe("NotesSidebar", () => {
  it("renders folders with note counts and notes, and emits select on note click", async () => {
    await mountSidebar();

    const tree = wrapper!.find(".notes-folder-tree");
    expect(tree.text()).toContain("运维");
    expect(tree.find(".notes-folder-tree__count").text()).toBe("1");
    expect(tree.text()).toContain("随记");

    const row = tree.findAll(".notes-folder-tree__node").find((node) => node.text().includes("随记"));
    await row!.trigger("click");

    expect(wrapper!.emitted("select")).toEqual([["n2"]]);
  });

  it("searches after 200 ms, shows snippets literally with highlighted matches, and restores the tree when cleared", async () => {
    vi.useFakeTimers();
    service.searchNotes.mockResolvedValue([
      { note: summary("n9", "Nginx 配置"), snippet: "<b>nginx</b> reload 与 NGINX 日志" },
    ]);
    await mountSidebar();

    await wrapper!.find('input[aria-label="搜索笔记"]').setValue("nginx");
    await vi.advanceTimersByTimeAsync(199);
    expect(service.searchNotes).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await flushPromises();

    expect(service.searchNotes).toHaveBeenCalledWith("nginx", 50);
    const results = wrapper!.find(".notes-sidebar__results");
    expect(results.exists()).toBe(true);
    expect(wrapper!.find(".notes-folder-tree").exists()).toBe(false);
    const snippet = results.find(".notes-sidebar__snippet");
    expect(snippet.text()).toContain("<b>nginx</b>");
    expect(snippet.find("b").exists()).toBe(false);
    expect(snippet.findAll("mark").map((mark) => mark.text())).toEqual(["nginx", "NGINX"]);
    expect(results.find(".notes-sidebar__item-title mark").text()).toBe("Nginx");

    await results.find(".notes-sidebar__item").trigger("click");
    expect(wrapper!.emitted("select")).toEqual([["n9"]]);

    await wrapper!.find('input[aria-label="搜索笔记"]').setValue("");
    await vi.advanceTimersByTimeAsync(200);
    await flushPromises();
    expect(wrapper!.find(".notes-sidebar__results").exists()).toBe(false);
    expect(wrapper!.find(".notes-folder-tree").exists()).toBe(true);
  });

  it("shows only favorite notes in the favorites segment", async () => {
    await mountSidebar();

    const items = wrapper!.find(".el-segmented").findAll(".el-segmented__item");
    await items.find((item) => item.text() === "收藏")!.trigger("click");
    await flushPromises();

    const list = wrapper!.find(".notes-sidebar__list");
    expect(list.findAll(".notes-sidebar__item").map((item) => item.text())).toEqual(["随记"]);
    expect(wrapper!.find(".notes-folder-tree").exists()).toBe(false);
  });

  it("groups notes by tag in the tags segment", async () => {
    await mountSidebar();

    const items = wrapper!.find(".el-segmented").findAll(".el-segmented__item");
    await items.find((item) => item.text() === "标签")!.trigger("click");
    await flushPromises();

    expect(wrapper!.find(".el-collapse").text()).toContain("工作");
  });

  it("shows resolved names and states in the links segment", async () => {
    const link = (objectType: "ssh" | "ai", objectId: string, nameSnapshot: string, scopeId = "") => ({
      objectType, objectId, parentId: "", scopeId, nameSnapshot, scopeNameSnapshot: "", createdAt: 1,
    });
    service.listAllNoteLinks.mockResolvedValue([
      { link: link("ssh", "ssh-1", "old name"), noteId: "n1", noteTitle: "随记" },
      { link: link("ssh", "ssh-gone", "gone box"), noteId: "n1", noteTitle: "随记" },
      { link: link("ai", "conv-9", "B chat", "scope-b"), noteId: "n1", noteTitle: "随记" },
    ]);
    await mountSidebar();

    const items = wrapper!.find(".el-segmented").findAll(".el-segmented__item");
    await items.find((item) => item.text() === "关联")!.trigger("click");
    await flushPromises();

    const names = wrapper!.findAll(".notes-sidebar__link-name");
    expect(names.map((name) => name.text())).toEqual(["gone box已删除", "prod web", "B chat（其他工作区：Beta）"]);
    expect(names[0]!.classes()).toContain("is-deleted");
  });

  it("labels objects whose state is unknown with text, not only color", async () => {
    linkSources.unavailable = new Set(["database"]);
    service.listAllNoteLinks.mockResolvedValue([
      { link: { objectType: "database", objectId: "db-x", parentId: "", scopeId: "", nameSnapshot: "orders", scopeNameSnapshot: "", createdAt: 1 }, noteId: "n1", noteTitle: "随记" },
    ]);
    await mountSidebar();
    const items = wrapper!.find(".el-segmented").findAll(".el-segmented__item");
    await items.find((item) => item.text() === "关联")!.trigger("click");
    await flushPromises();
    expect(wrapper!.find(".notes-sidebar__link-name").text()).toBe("orders状态未知");
  });

  it("lists skipped items with localized reasons after importing a folder", async () => {
    bridge.chooseNativeDirectory.mockResolvedValue("/tmp/docs");
    service.importNoteFolder.mockResolvedValue({
      notes: 2,
      assets: 1,
      skipped: [
        { path: "/tmp/docs/a.md: ../outside.png", reason: "outside_root" },
        { path: "/tmp/docs/b.md: x.png", reason: "brand_new_reason" },
      ],
      skippedTotal: 5,
      truncated: true,
    });
    const success = vi.spyOn(ElMessage, "success");
    const warning = vi.spyOn(ElMessage, "warning");
    await mountSidebar();
    service.getNoteTree.mockClear();

    await buttonByLabel("导入文件夹").trigger("click");
    await flushPromises();

    expect(service.importNoteFolder).toHaveBeenCalledWith("/tmp/docs", "");
    expect(service.getNoteTree).toHaveBeenCalled();
    expect(success).toHaveBeenCalledWith("导入 2 篇笔记、1 张图片");
    expect(warning).toHaveBeenCalledWith("文件夹过大，只导入了一部分");
    const body = document.body.textContent ?? "";
    expect(body).toContain("/tmp/docs/a.md: ../outside.png");
    expect(body).toContain("引用了导入目录之外的文件");
    expect(body).toContain("未知原因");
    expect(body).toContain("共跳过 5 项，仅列出前 2 项");
  });

  it("does nothing when the folder picker is cancelled", async () => {
    bridge.chooseNativeDirectory.mockResolvedValue(null);
    await mountSidebar();

    await buttonByLabel("导入文件夹").trigger("click");
    await flushPromises();

    expect(service.importNoteFolder).not.toHaveBeenCalled();
  });

  it("imports markdown files into the root", async () => {
    bridge.chooseNativeFiles.mockResolvedValue(["/tmp/a.md"]);
    service.importNoteFiles.mockResolvedValue({ notes: 1, assets: 0, skipped: [], skippedTotal: 0, truncated: false });
    await mountSidebar();

    await buttonByLabel("导入文件").trigger("click");
    await flushPromises();

    expect(bridge.chooseNativeFiles).toHaveBeenCalledWith(expect.any(String), [".md", ".markdown"]);
    expect(service.importNoteFiles).toHaveBeenCalledWith(["/tmp/a.md"], "");
  });

  it("exports the selected note and warns about missing images", async () => {
    bridge.chooseNativeDirectory.mockResolvedValue("/tmp/out");
    service.exportNotes.mockResolvedValue({ written: ["/tmp/out/随记.md"], missingAssets: ["/__notes-assets/a.png", "/__notes-assets/b.png"] });
    const success = vi.spyOn(ElMessage, "success");
    const warning = vi.spyOn(ElMessage, "warning");
    await mountSidebar();
    await wrapper!.setProps({ selectedId: "n2" });

    await buttonByLabel("导出").trigger("click");
    await flushPromises();

    expect(service.exportNotes).toHaveBeenCalledWith({ noteIds: ["n2"], destDir: "/tmp/out" });
    expect(success).toHaveBeenCalledWith("已导出 1 篇笔记");
    expect(warning).toHaveBeenCalledWith("有 2 张图片找不到，已保留原引用");
  });

  it("shows the live tree title for a search hit and falls back to the hit title", async () => {
    vi.useFakeTimers();
    service.searchNotes.mockResolvedValue([
      { note: summary("n2", "旧标题"), snippet: "" },
      { note: summary("gone", "已删除的笔记"), snippet: "" },
    ]);
    await mountSidebar();

    await wrapper!.find('input[aria-label="搜索笔记"]').setValue("记");
    await vi.advanceTimersByTimeAsync(200);
    await flushPromises();

    const titles = wrapper!.findAll(".notes-sidebar__results .notes-sidebar__item-title").map((title) => title.text());
    expect(titles).toEqual(["随记", "已删除的笔记"]);
  });

  async function openMenuWithKey(label: string, init: KeyboardEventInit) {
    const row = wrapper!.findAll('[role="treeitem"]').find((item) => item.find(".notes-folder-tree__label").text() === label)!;
    (row.element as HTMLElement).focus();
    await row.trigger("keydown", init);
    await flushPromises();
    const items = [...document.body.querySelectorAll<HTMLElement>(".app-context-menu__popper .el-dropdown-menu__item")];
    return { row, items };
  }

  it("opens the note context menu with the ContextMenu key and confirms deletion", async () => {
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockRejectedValue("cancel");
    await mountSidebar();

    const { items } = await openMenuWithKey("随记", { key: "ContextMenu" });
    expect(items.map((item) => item.textContent?.trim())).toEqual(["重命名", "移动到…", "导出", "删除"]);

    items.find((item) => item.textContent?.trim() === "删除")!.click();
    await flushPromises();

    expect(confirm).toHaveBeenCalledWith("删除笔记「随记」？删除后无法恢复。", "删除笔记", expect.anything());
  });

  it("opens the folder context menu with Shift+F10 and confirms deletion", async () => {
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockRejectedValue("cancel");
    await mountSidebar();

    const { items } = await openMenuWithKey("运维", { key: "F10", shiftKey: true });
    expect(items.map((item) => item.textContent?.trim())).toEqual(["新建笔记", "新建子文件夹", "重命名", "移动到…", "导出", "删除"]);

    items.find((item) => item.textContent?.trim() === "删除")!.click();
    await flushPromises();

    expect(confirm).toHaveBeenCalledWith(
      "删除文件夹「运维」？其中的全部笔记和子文件夹都会被删除，且无法恢复。",
      "删除文件夹",
      expect.anything(),
    );
  });

  it("moves a folder from the keyboard menu, never into itself or its subfolders", async () => {
    service.moveNoteFolder.mockResolvedValue(undefined);
    await mountSidebar({
      folders: [folder("f1", "运维"), folder("f2", "子目录", "f1"), folder("f3", "其他", "", 2), folder("f4", "归档", "f3", 1)],
      notes: [],
    });

    const { items } = await openMenuWithKey("其他", { key: "F10", shiftKey: true });
    items.find((item) => item.textContent?.trim() === "移动到…")!.click();
    await flushPromises();

    const select = wrapper!.findComponent(ElTreeSelect);
    type Option = { value: string; children: Option[] };
    const values = (list: Option[]): string[] => list.flatMap((option) => [option.value, ...values(option.children)]);
    expect(values(select.props("data") as Option[])).toEqual(["root", "f1", "f2"]);
    expect(select.props("modelValue")).toBe("root");

    select.vm.$emit("update:modelValue", "f2");
    await flushPromises();
    const confirm = [...document.body.querySelectorAll<HTMLButtonElement>(".el-dialog button")].find((button) => button.textContent?.trim() === "确认");
    confirm!.click();
    await flushPromises();

    expect(service.moveNoteFolder).toHaveBeenCalledWith("f3", "f2", 1);
    expect(useNotesStore().tree!.folders.find((item) => item.id === "f3")?.parentId).toBe("f2");
  });
});
