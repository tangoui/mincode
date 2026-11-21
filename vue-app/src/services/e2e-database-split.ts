// Loaded only by the native -tags e2e smoke runner, using its disposable SQLite file.
import router from "@/router";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { overviewConnectAction } from "@/services/e2e-database-overview";

async function until<T>(read: () => T | undefined | false, label: string): Promise<T> {
  const deadline = performance.now() + 15_000;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 30));
  }
  throw new Error(`Timed out waiting for ${label}`);
}

function visible(selector: string) {
  return [...document.querySelectorAll<HTMLElement>(selector)]
    .find((element) => element.getBoundingClientRect().width > 0);
}

async function frame() {
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

export async function databaseSplitScenario(filePath: string, log: { done(step: string): void }) {
  const workbench = useWorkbenchStore();
  const tab = workbench.openView("database");
  if (!tab) throw new Error("Could not open database workspace");
  await router.push({ name: "database" });
  const store = useDatabaseWorkspaceStore();
  await store.initialize();
  const id = "e2e-query-split";
  const now = Date.now();
  await store.saveConnection({
    id, name: id, dbType: "sqlite", host: "", port: 0, filePath,
    ssl: false, savePassword: false, createdAt: now, updatedAt: now,
  });
  try {
    const connect = await until(() => overviewConnectAction(id), "SQLite connection row");
    connect.click();
    const table = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-browser__name")]
      .find((element) => element.textContent?.trim() === "e2e_items"), "SQLite table");
    table.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    await until(() => visible(".db-query-split"), "table result split");
    const create = await until(() => visible(".db-object-tabs__new"), "new query button");
    create.click();
    await until(() => visible(".db-query-split .db-sql-editor"), "query editor");
    await frame();
    const height = () => {
      const panel = document.querySelector<HTMLElement>(".db-query-split > .el-splitter > .el-splitter-panel");
      if (!panel) throw new Error("Query panel missing");
      return panel.getBoundingClientRect().height;
    };
    const drag = async (delta: number) => {
      const bar = visible(".db-query-split .el-splitter-bar__dragger");
      if (!bar) throw new Error("Query dragger missing");
      const rect = bar.getBoundingClientRect();
      const before = height();
      const x = rect.x + rect.width / 2;
      const y = rect.y + rect.height / 2;
      bar.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, clientX: x, clientY: y }));
      window.dispatchEvent(new MouseEvent("mousemove", { clientX: x, clientY: y + delta }));
      // Complete the synthetic gesture in one JS turn: real user mouse moves
      // must not interleave before its mouseup while this desktop is in use.
      window.dispatchEvent(new MouseEvent("mouseup", { clientX: x, clientY: y + delta }));
      await frame();
      const after = height();
      if (Math.abs(after - before - delta) > 3) {
        const split = document.querySelector<HTMLElement>(".db-query-split");
        const panels = [...(split?.querySelectorAll<HTMLElement>(":scope > .el-splitter > .el-splitter-panel") ?? [])].map((el) => Math.round(el.getBoundingClientRect().height));
        throw new Error(`Query divider jumped: ${before} -> ${after}, expected delta ${delta}; panels=${JSON.stringify(panels)} split=${Math.round(split?.getBoundingClientRect().height ?? 0)} rect=${JSON.stringify(bar.getBoundingClientRect())}`);
      }
      log.done(`real workspace divider moved ${delta}px (${Math.round(before)} -> ${Math.round(after)})`);
    };
    await drag(40);
    const savedHeight = height();
    const tableTab = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-tabs [role=tab]")]
      .find((element) => element.textContent?.includes("e2e_items")), "table tab");
    tableTab.click();
    await until(() => height() < 1, "collapsed query editor");
    const queryTab = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-tabs [role=tab]")]
      .find((element) => element !== tableTab && element.id.includes("query:")), "query tab");
    queryTab.click();
    await until(() => height() > 1, "restored query editor");
    await frame();
    if (Math.abs(height() - savedHeight) > 3) throw new Error("Editor height lost after tab switch");
    await drag(-30);
    log.done("table/query switching preserved height; real WebKit mouse drag stayed stable");
  } finally {
    await store.disconnect(id);
    await store.deleteConnection(id);
    workbench.closeTab(tab.id, true);
  }
}
