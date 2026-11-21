import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import GitCommitGraph from "@/components/git/GitCommitGraph.vue";

describe("GitCommitGraph 空状态", () => {
  it("没有提交时在 DOM 里显示“暂无提交记录”，而不是一片空白", () => {
    const wrapper = mount(GitCommitGraph, { props: { entries: [] } });

    expect(wrapper.text()).toContain("暂无提交记录");
    expect(wrapper.find(".git-inspector-empty").exists()).toBe(true);
  });

  it("有提交时渲染提交摘要与作者，不再显示空状态", () => {
    const wrapper = mount(GitCommitGraph, {
      props: {
        entries: [
          {
            hash: "a".repeat(40),
            short_hash: "aaaaaaa",
            author: "mincode",
            email: "dev@example.com",
            message: "fix(git): explain an empty commit graph",
            time: Math.floor(Date.now() / 1000) - 60,
            parents: [],
            refs: [],
          },
        ],
      },
    });

    expect(wrapper.find(".git-inspector-empty").exists()).toBe(false);
    expect(wrapper.text()).toContain("fix(git): explain an empty commit graph");
    expect(wrapper.text()).toContain("mincode");
  });
});

describe("GitCommitGraph 泳道宽度", () => {
  it("并行分支再多也不会把提交信息挤出可视区", () => {
    // 模拟 200 条提交里出现 30 条并行泳道的极端情况。
    const entries = Array.from({ length: 30 }, (_, index) => ({
      hash: String(index).padStart(40, "a"),
      short_hash: `aaaa${index}`,
      author: "mincode",
      message: `feat: commit ${index}`,
      time: Math.floor(Date.now() / 1000) - index * 60,
      parents: index < 29 ? [String(index + 1).padStart(40, "a")] : [],
      refs: [],
    }));
    const wrapper = mount(GitCommitGraph, { props: { entries } });

    const svg = wrapper.find("svg");
    // 泳道列宽上限：8 条泳道 × 17px + 8px 余量。
    expect(Number(svg.attributes("width"))).toBeLessThanOrEqual(144);
    expect(wrapper.text()).toContain("feat: commit 0");
  });
});

describe("GitCommitGraph 布局", () => {
  const commit = (index: number, parents: string[], refs: string[] = []) => ({
    hash: String(index).padStart(40, "b"),
    short_hash: `bbbb${index}`,
    author: "funadmin",
    message: `fix: something ${index}`,
    time: Math.floor(Date.now() / 1000) - index * 3600,
    parents,
    refs,
  });

  it("只有一条主线时泳道列只占一条泳道的宽度", () => {
    const entries = [commit(0, [String(1).padStart(40, "b")]), commit(1, [])];
    const wrapper = mount(GitCommitGraph, { props: { entries } });
    // 1 条泳道 × 17px + 8px 余量，而不是固定 8 条泳道的 144px。
    expect(Number(wrapper.find("svg").attributes("width"))).toBe(25);
  });

  it("分支和标签标记不额外占列，每行都是 提交号/摘要/作者/时间 四格", () => {
    const entries = [commit(0, [String(1).padStart(40, "b")], ["HEAD -> main", "tag: v0.1.0"]), commit(1, [])];
    const wrapper = mount(GitCommitGraph, { props: { entries } });
    const rows = wrapper.findAll(".git-graph__commits > button");
    for (const row of rows) expect(row.element.children).toHaveLength(4);
    expect(rows[0].findAll(".git-graph__subject .git-graph__ref")).toHaveLength(2);
    expect(rows[0].get("time").text()).toMatch(/现在|秒|分钟|小时|now|second|minute|hour/);
  });
});
