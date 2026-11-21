// Loaded only by the native -tags e2e smoke runner, using its disposable SQLite file.
import router from "@/router";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { overviewConnectAction } from "@/services/e2e-database-overview";

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

const CONTROLS = [
  ".db-object-browser__database",
  ".db-object-browser__search",
  ".db-object-browser__filters",
  ".db-object-browser__sort",
  ".db-object-browser__toolbar > .el-button",
];

/** Narrows the real object browser in WebKit and checks every toolbar control stays fully inside it. */
export async function databaseObjectToolbarScenario(filePath: string, log: { done(step: string): void }) {
  const workbench = useWorkbenchStore();
  const tab = workbench.openView("database");
  if (!tab) throw new Error("Could not open database workspace");
  await router.push({ name: "database" });
  const store = useDatabaseWorkspaceStore();
  await store.initialize();
  const id = "e2e-object-toolbar";
  const now = Date.now();
  await store.saveConnection({
    id, name: id, dbType: "sqlite", host: "", port: 0, filePath,
    ssl: false, savePassword: false, createdAt: now, updatedAt: now,
  });
  let browser: HTMLElement | undefined;
  try {
    const connect = await until(() => overviewConnectAction(id), "SQLite connection row");
    connect.click();
    // An earlier scenario may leave a table/query tab active; open the overview tab.
    const overview = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-tabs [role=tab]")]
      .find((element) => element.id.includes("database:")), "database overview tab");
    overview.click();
    browser = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-browser")]
      .find((element) => element.getBoundingClientRect().width > 0 && element.querySelector(".db-object-browser__name")), "object browser");
    const results: string[] = [];
    // The test window bounds the pane; only narrow it, never ask for more width than exists.
    const natural = Math.floor(browser.getBoundingClientRect().width);
    const widths = [natural, ...[700, 560, 420].filter((width) => width < natural)];
    for (const width of widths) {
      browser.style.flex = `0 0 ${width}px`;
      browser.style.maxWidth = `${width}px`;
      await frame();
      const toolbar = browser.querySelector<HTMLElement>(".db-object-browser__toolbar");
      if (!toolbar) throw new Error("Toolbar missing");
      const box = toolbar.getBoundingClientRect();
      if (Math.abs(box.width - width) > 2) throw new Error(`Toolbar width ${Math.round(box.width)} did not follow ${width}px`);
      const rows = new Set<number>();
      for (const selector of CONTROLS) {
        const control = toolbar.querySelector<HTMLElement>(selector);
        if (!control) throw new Error(`${selector} missing at ${width}px`);
        const rect = control.getBoundingClientRect();
        if (rect.width < 1 || rect.height < 1) throw new Error(`${selector} hidden at ${width}px`);
        if (rect.left < box.left - 1 || rect.right > box.right + 1 || rect.bottom > box.bottom + 1) {
          throw new Error(`${selector} overflows the toolbar at ${width}px: ${JSON.stringify({ control: [rect.left, rect.right, rect.bottom], toolbar: [box.left, box.right, box.bottom] })}`);
        }
        // Controls differ in height; group rows by vertical centre, not top edge.
        rows.add(Math.round((rect.top + rect.height / 2 - box.top) / 20));
      }
      const filters = toolbar.querySelector<HTMLElement>(".db-object-browser__filters");
      if (filters && filters.scrollWidth > filters.clientWidth + 1) throw new Error(`Filter counts clipped at ${width}px`);
      if (rows.size > 2) throw new Error(`Toolbar used ${rows.size} rows at ${width}px`);
      results.push(`${width}px:${rows.size} row${rows.size > 1 ? "s" : ""}`);
    }
    log.done(`object toolbar controls stay fully visible in real WebKit (${results.join(", ")})`);
  } finally {
    if (browser) {
      browser.style.flex = "";
      browser.style.maxWidth = "";
    }
    await store.disconnect(id);
    await store.deleteConnection(id);
    workbench.closeTab(tab.id, true);
  }
}
