// Opt-in native performance probe for the sessions workspace (-tags e2e builds only).
// The harness writes many Claude Code transcripts into the isolated HOME (~/.claude/projects):
// 30 projects × 100 sessions, one of them 2000 messages long. The probe opens the workspace,
// expands projects, opens the long session and scrolls its transcript.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { useWorkbenchStore } from "@/stores/workbench";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";

function visible<T extends HTMLElement>(selector: string, root: ParentNode = document) {
  return [...root.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

export async function perfSessionsScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfSessions!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const tab = workbench.openView("sessions");
  if (!tab) throw new Error("Could not open sessions workspace");
  const projectRows = () => [...document.querySelectorAll<HTMLElement>(".sessions-project__toggle")].filter((element) => element.getBoundingClientRect().width > 0);
  try {
    await measure(`进入会话页并展开 Claude（${fixture.projects} 个项目、${fixture.projects * fixture.sessionsPerProject} 个会话）`, async () => {
      await router.push({ name: "sessions" });
      // The Claude group is collapsed until the user opens it.
      const claude = await until(() => [...document.querySelectorAll<HTMLElement>(".sessions-provider")].find((section) => section.querySelector(".sessions-provider__nav-icon")?.className.includes("claude") || section.textContent?.includes("Claude")), "Claude group", wait);
      claude.querySelector<HTMLElement>(".sessions-provider__toggle")?.click();
      await until(() => projectRows().length >= fixture.projects, "all project rows", wait * 2);
    });
    await measure("依次展开 10 个项目（每个 100 个会话，含长会话所在项目）", async () => {
      // project-00 holds the long session; projects are listed newest first, so find it by path.
      const rows = projectRows();
      // Directory names decode "-" as "/", so fixture projects are named proj00 … proj29.
      const longProject = rows.find((toggle) => toggle.closest(".sessions-project__row")?.textContent?.includes("proj00"));
      if (!longProject) throw new Error("proj00 row not found");
      const targets = [...new Set([longProject, ...rows].filter(Boolean) as HTMLElement[])].slice(0, Number(fixture.expandProjects) || 10);
      for (const toggle of targets) {
        toggle.click();
        await nextFrame();
      }
      // Long lists render only the rows near the viewport: wait for every expanded group instead.
      await until(() => document.querySelectorAll(".sessions-provider-list [data-row-group]").length >= targets.length && document.querySelectorAll(".sessions-session-row").length > 0, "expanded session groups", wait * 2);
    });
    await measure("会话列表滚动到底再回顶部", async () => {
      const element = await until(() => visible<HTMLElement>(".sessions-session-row")?.closest<HTMLElement>(".el-scrollbar__wrap"), "session list scroller", wait);
      const span = element.scrollHeight - element.clientHeight;
      for (let i = 0; i <= 40; i++) { element.scrollTop = (span * i) / 40; await nextFrame(); }
      for (let i = 40; i >= 0; i--) { element.scrollTop = (span * i) / 40; await nextFrame(); }
    });
    await measure(`打开 ${fixture.longMessages} 条消息的长会话`, async () => {
      const group = await until(() => [...document.querySelectorAll<HTMLElement>(".sessions-provider-list [data-row-group]")].find((element) => element.getAttribute("data-row-group")?.includes("proj00")), "proj00 sessions", wait);
      // The long session was written first, so it is the oldest: the last row of its group.
      group.scrollIntoView({ block: "end" });
      const row = await until(() => [...document.querySelectorAll<HTMLElement>(".sessions-session-row")].find((element) => element.textContent?.includes(fixture.longTitle)), "long session row", wait);
      row.querySelector<HTMLElement>(".sessions-session-row__main")?.click();
      // Opening also tries to resume it in a terminal (no CLI here); switch to the transcript.
      await until(() => {
        const show = [...document.querySelectorAll<HTMLButtonElement>(".sessions-primary-button")].find((button) => button.getBoundingClientRect().width > 0);
        show?.click();
        return document.querySelectorAll(".sessions-message").length > 10;
      }, "transcript messages", wait * 2);
    });
    await measure("长会话记录滚动到顶再回底", async () => {
      const element = await until(() => visible<HTMLElement>(".sessions-message")?.closest<HTMLElement>(".el-scrollbar__wrap, .sessions-conversation__messages"), "transcript scroller", wait);
      const span = element.scrollHeight - element.clientHeight;
      for (let i = 40; i >= 0; i--) { element.scrollTop = (span * i) / 40; await nextFrame(); }
      for (let i = 0; i <= 40; i++) { element.scrollTop = (span * i) / 40; await nextFrame(); }
    });
    await measure("加载更早的消息 5 次", async () => {
      for (let i = 0; i < 5; i++) {
        const earlier = visible<HTMLElement>(".sessions-message-stream__earlier button, .sessions-message-stream__earlier");
        if (!earlier) break;
        const before = document.querySelectorAll(".sessions-message").length;
        (earlier.querySelector("button") ?? earlier).click();
        await until(() => document.querySelectorAll(".sessions-message").length > before, "earlier messages", wait);
      }
    });
    await measure("会话页空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
  } finally {
    workbench.closeTab(tab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
  await measure("离开会话页后空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
}
