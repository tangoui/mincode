import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import AiRewindRestoreDialog from "@/components/ai/AiRewindRestoreDialog.vue";
import type { RewindRestorePlan } from "@/domain/ai/rewind-restore-plan";

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { wrapper?.unmount(); wrapper = undefined; });

function plan(overrides: Partial<RewindRestorePlan> = {}): RewindRestorePlan {
  return {
    projectRoot: "/work/app",
    target: "a".repeat(40),
    expected: "b".repeat(40),
    restore: ["src/main.ts"],
    remove: ["src/new.ts"],
    conflicts: ["README.md"],
    uncovered: [],
    overlapped: false,
    ...overrides,
  };
}

async function open(plans: RewindRestorePlan[]) {
  wrapper = mount(AiRewindRestoreDialog, { props: { modelValue: true, plans }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

const button = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent?.trim() === label);

describe("rewind restore dialog", () => {
  it("lists the files and confirms with or without restoring them", async () => {
    const dialog = await open([plan()]);
    const text = document.body.textContent ?? "";
    expect(text).toContain("将恢复");
    expect(text).toContain("src/main.ts");
    expect(text).toContain("将删除");
    expect(text).toContain("src/new.ts");
    expect(text).toContain("冲突，不会修改");
    expect(text).toContain("README.md");

    button("恢复文件并回退")?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[true]]);

    button("仅回退对话")?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[true], [false]]);

    button("取消")?.click();
    await flushPromises();
    expect(dialog.emitted("cancel")).toEqual([[]]);
  });

  it("offers only the conversation rewind when file restore is blocked", async () => {
    const dialog = await open([plan({ blocked: "git_state_changed", restore: [], remove: [], conflicts: [] })]);
    expect(button("恢复文件并回退")).toBeUndefined();
    expect(document.body.textContent).toContain("HEAD");
    button("仅回退对话")?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[false]]);
  });

  it("explains each blocked reason", async () => {
    await open([
      plan({ projectRoot: "/one", blocked: "no_snapshot", restore: [], remove: [], conflicts: [] }),
      plan({ projectRoot: "/two", blocked: "unsupported", restore: [], remove: [], conflicts: [] }),
    ]);
    const text = document.body.textContent ?? "";
    expect(text).toContain("没有完整的快照");
    expect(text).toContain("不是 Git 仓库");
    expect(text).toContain("/one");
    expect(text).toContain("/two");
    expect(button("恢复文件并回退")).toBeUndefined();
  });

  it("shows each project root, the overlap notice and uncovered paths", async () => {
    await open([
      plan({ overlapped: true, uncovered: ["vendor/lib"] }),
      plan({ projectRoot: "/work/docs", blocked: "no_snapshot", restore: [], remove: [], conflicts: [] }),
    ]);
    const text = document.body.textContent ?? "";
    expect(text).toContain("/work/app");
    expect(text).toContain("/work/docs");
    expect(text).toContain("其他 AI 运行重叠");
    expect(text).toContain("未覆盖");
    expect(text).toContain("vendor/lib");
    // one root can still be restored
    expect(button("恢复文件并回退")).toBeDefined();
  });

  it("does not offer a restore when no file would change", async () => {
    await open([plan({ restore: [], remove: [], conflicts: ["README.md"] })]);
    expect(button("恢复文件并回退")).toBeUndefined();
    expect(button("仅回退对话")).toBeDefined();
  });
});
