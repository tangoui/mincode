import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import GitConflictResolver from "@/components/git/GitConflictResolver.vue";
import {
  getGitConflictContent,
  getGitMergeState,
  resolveGitConflict,
} from "@/services/git-service";

vi.mock("@/services/git-service", () => ({
  getGitMergeState: vi.fn(),
  getGitConflictContent: vi.fn(),
  resolveGitConflict: vi.fn(),
  abortGitMerge: vi.fn(),
}));

// Monaco 在 happy-dom 中不可用；替身保留 v-model 和 Ctrl+S 的 save 事件契约。
const MonacoTextEditorStub = defineComponent({
  name: "MonacoTextEditor",
  props: { modelValue: { type: String, default: "" }, path: { type: String, default: "" } },
  emits: ["update:modelValue", "save"],
  setup(props, { emit }) {
    return () =>
      h("textarea", {
        class: "monaco-stub",
        "data-path": props.path,
        value: props.modelValue,
        onKeydown: () => emit("save"),
      });
  },
});

const resolvedA = { ours: "a-ours\n", theirs: "a-theirs\n", base: "a-base\n", raw: "a-resolved\n" };

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

function mountResolver() {
  return mount(GitConflictResolver, {
    props: { repoPath: "/repo" },
    global: {
      stubs: { MonacoTextEditor: MonacoTextEditorStub },
      directives: { loading: {} },
    },
  });
}

describe("GitConflictResolver", () => {
  it("does not save the previous file's content when the newly selected file fails to load", async () => {
    vi.mocked(getGitMergeState).mockResolvedValue({ active: true, conflicts: ["a.txt", "b.bin"] });
    vi.mocked(getGitConflictContent).mockImplementation(async (_repo, path) => {
      if (path === "a.txt") return resolvedA;
      throw new Error("暂不支持可视化解决二进制文件冲突");
    });
    vi.mocked(resolveGitConflict).mockResolvedValue(undefined);

    wrapper = mountResolver();
    await flushPromises();
    const markResolved = () => wrapper!.find(".git-conflict-resolver__footer .el-button--primary");
    expect(markResolved().attributes("disabled")).toBeUndefined();

    const files = wrapper.findAll(".git-conflict-resolver__file");
    await files[1].trigger("click");
    await flushPromises();

    expect(getGitConflictContent).toHaveBeenLastCalledWith("/repo", "b.bin");
    expect(files[1].classes()).toContain("is-active");
    // A 的正文不能继续显示在 B 的标题下。
    expect(wrapper.find(".git-conflict-resolver__toolbar").exists()).toBe(false);
    expect(wrapper.find(".monaco-stub").exists()).toBe(false);
    expect(markResolved().attributes("disabled")).toBeDefined();

    await markResolved().trigger("click");
    await flushPromises();
    expect(resolveGitConflict).not.toHaveBeenCalled();
  });

  it("saves the loaded file through the editor save shortcut", async () => {
    vi.mocked(getGitMergeState)
      .mockResolvedValueOnce({ active: true, conflicts: ["a.txt"] })
      .mockResolvedValue({ active: true, conflicts: [] });
    vi.mocked(getGitConflictContent).mockResolvedValue(resolvedA);
    vi.mocked(resolveGitConflict).mockResolvedValue(undefined);

    wrapper = mountResolver();
    await flushPromises();
    await wrapper.find('.monaco-stub[data-path="a.txt.resolved"]').trigger("keydown");
    await flushPromises();
    expect(resolveGitConflict).toHaveBeenCalledWith("/repo", "a.txt", "a-resolved\n");
  });
});
