// Loaded only by the native -tags e2e smoke runner. Exercises the project sidebar
// splitter in the real workspace, starting from a saved collapsed state.
import router from "@/router";
import { useWorkbenchStore } from "@/stores/workbench";
import { savePersistedValue } from "@/services/persistent-store";

async function until<T>(read: () => T | undefined | false | null, label: string): Promise<T> {
  const deadline = performance.now() + 15_000;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 30));
  }
  throw new Error(`Timed out waiting for ${label}`);
}

async function frame() {
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

export async function projectSidebarScenario(log: { done(step: string): void }) {
  // Reproduce a user who collapsed the sidebar earlier: the state is restored on open.
  localStorage.setItem("app_project_sidebar_collapsed", "true");
  localStorage.setItem("app_project_sidebar_width", "260");
  await savePersistedValue("app_project_sidebar_collapsed", true);
  await savePersistedValue("app_project_sidebar_width", 260);
  const workbench = useWorkbenchStore();
  const tab = workbench.openView("projects");
  if (!tab) throw new Error("Could not open project workspace");
  await router.push({ name: "projects" });
  try {
    const pane = await until(() => document.querySelector<HTMLElement>(".project-workspace > .app-split-pane"), "project split pane");
    const panel = () => {
      const element = pane.querySelector<HTMLElement>(":scope > .el-splitter > .el-splitter-panel");
      if (!element) throw new Error("Project sidebar panel missing");
      return element;
    };
    await until(() => pane.classList.contains("is-collapsed") && panel().getBoundingClientRect().width < 1, "restored collapsed sidebar");
    log.done("project sidebar restored collapsed");
    const expand = await until(() => pane.querySelector<HTMLElement>(".el-splitter-bar__collapse-icon"), "expand control");
    expand.click();
    await until(() => !pane.classList.contains("is-collapsed") && panel().getBoundingClientRect().width > 150, "expanded sidebar");
    await frame();
    const drag = async (delta: number) => {
      const bar = pane.querySelector<HTMLElement>(":scope > .el-splitter > .el-splitter-bar .el-splitter-bar__dragger");
      if (!bar) throw new Error("Sidebar dragger missing");
      const rect = bar.getBoundingClientRect();
      const before = panel().getBoundingClientRect().width;
      const x = rect.x + rect.width / 2;
      const y = rect.y + rect.height / 2;
      bar.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, clientX: x, clientY: y }));
      window.dispatchEvent(new MouseEvent("mousemove", { clientX: x + delta, clientY: y }));
      // Complete the synthetic gesture in one JS turn: real user mouse moves
      // must not interleave before its mouseup while this desktop is in use.
      window.dispatchEvent(new MouseEvent("mouseup", { clientX: x + delta, clientY: y }));
      await frame();
      const after = panel().getBoundingClientRect().width;
      if (pane.classList.contains("is-collapsed") || after < 150) throw new Error(`Sidebar collapsed while dragging: ${before} -> ${after}`);
      if (Math.abs(after - before - delta) > 3) throw new Error(`Sidebar drag moved ${after - before}px, expected ${delta}px`);
      log.done(`expanded sidebar dragged ${delta}px (${Math.round(before)} -> ${Math.round(after)})`);
    };
    // The panel's own edge scrollbar (el-scrollbar sits 2-8px from the edge)
    // must not be covered by the splitter's drag handle.
    {
      const edge = panel().getBoundingClientRect();
      const y = edge.y + edge.height / 2;
      for (const inset of [3, 5]) {
        const hit = document.elementFromPoint(edge.right - inset, y);
        if (hit?.closest(".el-splitter-bar")) throw new Error(`splitter handle covers the sidebar ${inset}px inside its edge (scrollbar area)`);
      }
      log.done("sidebar edge scrollbar area (3px and 5px inside the edge) is not covered by the splitter handle");
    }
    await drag(60);
    await drag(-40);
  } finally {
    localStorage.removeItem("app_project_sidebar_collapsed");
    await savePersistedValue("app_project_sidebar_collapsed", false);
    workbench.closeTab(tab.id, true);
  }
}
