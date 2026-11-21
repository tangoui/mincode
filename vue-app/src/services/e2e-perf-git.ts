// Opt-in native performance probe for the project Git panel (-tags e2e builds only).
// The harness creates a git repository with 2000 modified and 1000 untracked files; the probe
// opens it, switches to source control, and scrolls/collapses the change list.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { enqueueProjectFiles } from "@/services/external-file-open";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";

function visible<T extends HTMLElement>(selector: string) {
  return [...document.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

export async function perfGitScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfGit!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const project = useLocalProjectStore();
  const tab = workbench.openView("projects");
  if (!tab) throw new Error("Could not open project workspace");
  const rows = () => document.querySelectorAll(".git-change-scroll .git-file-row").length;
  // Large groups render only the rows near the viewport; the headers carry the full counts.
  const counts = () => [...document.querySelectorAll<HTMLElement>(".git-change-scroll .git-change-group__header b")].map((node) => Number(node.textContent));
  const listed = () => counts().includes(fixture.modified) && counts().includes(fixture.untracked) && rows() > 10;
  try {
    await measure("打开 Git 测试仓库", async () => {
      await router.push({ name: "projects" });
      enqueueProjectFiles([fixture.repoRoot]);
      await until(() => project.projectRoot === fixture.repoRoot, "project root", wait);
    });
    await measure(`切到 Git 面板（${fixture.modified} 个修改 + ${fixture.untracked} 个未跟踪）`, async () => {
      await router.push({ name: "projects", query: { panel: "git" } });
      await until(listed, "change list with full counts", wait * 2);
    });
    log.done(`PERF ${JSON.stringify({ step: "Git 面板渲染的文件行数", rows: rows(), counts: counts() })}`);
    await measure("改动列表滚动到底再回顶部", async () => {
      const element = await until(() => visible<HTMLElement>(".git-change-scroll .el-scrollbar__wrap"), "change list scroller", wait);
      const span = element.scrollHeight - element.clientHeight;
      for (let i = 0; i <= 40; i++) { element.scrollTop = (span * i) / 40; await nextFrame(); }
      for (let i = 40; i >= 0; i--) { element.scrollTop = (span * i) / 40; await nextFrame(); }
    });
    await measure("折叠再展开改动分组", async () => {
      const toggles = [...document.querySelectorAll<HTMLElement>(".git-change-scroll .git-change-group__toggle")];
      for (const toggle of toggles) { toggle.click(); await nextFrame(); }
      for (const toggle of toggles) { toggle.click(); await nextFrame(); }
      await until(listed, "rows back", wait);
    });
    await measure("切回资源管理器再切到 Git 面板 3 次", async () => {
      for (let i = 0; i < 3; i++) {
        await router.push({ name: "projects", query: {} });
        await nextFrame();
        await router.push({ name: "projects", query: { panel: "git" } });
        await until(listed, "rows again", wait);
      }
    });
    if (fixture.historyCommits) {
      const historyRows = () => document.querySelectorAll(".project-git-history-pane .app-git-history__row").length;
      const segment = (label: string) => [...document.querySelectorAll<HTMLElement>(".el-segmented__item")].find((item) => item.textContent?.trim() === label);
      await measure(`切到历史标签（${fixture.historyCommits} 个提交）`, async () => {
        (await until(() => segment("历史"), "history segment", wait)).click();
        await until(() => historyRows() >= fixture.historyCommits!, "history rows", wait);
      });
      await measure("历史列表滚动到底再回顶部", async () => {
        const element = await until(() => visible<HTMLElement>(".project-git-history-pane .app-git-history__scroll .el-scrollbar__wrap"), "history scroller", wait);
        const span = element.scrollHeight - element.clientHeight;
        for (let i = 0; i <= 30; i++) { element.scrollTop = (span * i) / 30; await nextFrame(); }
        for (let i = 30; i >= 0; i--) { element.scrollTop = (span * i) / 30; await nextFrame(); }
      });
      await measure("依次选中 10 个提交", async () => {
        const mains = [...document.querySelectorAll<HTMLElement>(".project-git-history-pane .app-git-history__main")].slice(0, 10);
        for (const main of mains) {
          main.click();
          await until(() => main.closest(".app-git-history__row")?.classList.contains("is-selected"), "selected commit", wait);
          await nextFrame();
        }
      });
      // The file list renders only the rows in view; the detail header carries the full count.
      const bigOpen = () => (document.querySelector(".project-git-commit-detail header > span")?.textContent ?? "").includes(String(fixture.modified))
        && document.querySelectorAll(".project-git-commit-files button").length > 0;
      const smallOpen = () => Boolean(document.querySelector(".project-git-commit-detail")) && !bigOpen();
      await measure(`打开含 ${fixture.modified} 个文件的提交`, async () => {
        const mains = [...document.querySelectorAll<HTMLElement>(".project-git-history-pane .app-git-history__main")];
        mains[mains.length - 1]!.click();
        await until(bigOpen, "commit files", wait * 2);
        await nextFrame();
      });
      await measure("提交文件列表滚动到底再回顶部", async () => {
        const element = await until(() => visible<HTMLElement>(".project-git-commit-files .el-scrollbar__wrap"), "commit files scroller", wait);
        const span = element.scrollHeight - element.clientHeight;
        for (let i = 0; i <= 30; i++) { element.scrollTop = (span * i) / 30; await nextFrame(); }
        for (let i = 30; i >= 0; i--) { element.scrollTop = (span * i) / 30; await nextFrame(); }
      });
      await measure("从大提交切到小提交再切回", async () => {
        const mains = [...document.querySelectorAll<HTMLElement>(".project-git-history-pane .app-git-history__main")];
        for (let i = 0; i < 3; i++) {
          mains[0]!.click();
          await until(smallOpen, "small commit", wait);
          await nextFrame();
          mains[mains.length - 1]!.click();
          await until(bigOpen, "big commit", wait);
          await nextFrame();
        }
      });
      await measure("切回改动标签", async () => {
        (await until(() => segment("变更"), "changes segment", wait)).click();
        await until(listed, "change list after history", wait);
      });
    }
    await measure("Git 面板空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
  } finally {
    workbench.closeTab(tab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
}
