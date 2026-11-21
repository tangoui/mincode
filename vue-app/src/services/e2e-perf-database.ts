// Opt-in native performance probe for the database workspace (-tags e2e builds only).
// Uses a harness-generated SQLite file with a wide 100k-row table and drives the real UI:
// open the connection, open the table, page through it, run a large SELECT in the SQL
// editor (capped by the result stream) and scroll the result grid both ways.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { overviewConnectAction } from "@/services/e2e-database-overview";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";
import { EditorView } from "@codemirror/view";
import { closeCompletion } from "@codemirror/autocomplete";

function visible<T extends HTMLElement>(selector: string, root: ParentNode = document) {
  return [...root.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

/**
 * The wheel target of the visible result grid under root. el-table-v2's window is an
 * overflow:hidden div driven by wheel events (it ignores scrollTop), so the probe scrolls
 * the way a user does: WheelEvents dispatched at it.
 */
function gridScroller(root: ParentNode = document): HTMLElement | undefined {
  const grid = [...root.querySelectorAll<HTMLElement>(".db-tablev2-grid, .db-virtual-grid")].find((element) => element.getBoundingClientRect().width > 0);
  if (!grid) return undefined;
  return [grid, ...grid.querySelectorAll<HTMLElement>("div")].find((element) => element.scrollHeight > element.clientHeight + 2 && element.clientHeight > 100);
}

/** Wheel through the whole content and back, `steps` frames each way. */
async function scrollThrough(element: HTMLElement, axis: "top" | "left", steps: number) {
  const span = axis === "top" ? element.scrollHeight - element.clientHeight : element.scrollWidth - element.clientWidth;
  const delta = Math.max(1, span / steps);
  for (const direction of [1, -1]) {
    for (let i = 0; i < steps; i++) {
      element.dispatchEvent(new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaMode: 0, deltaY: axis === "top" ? delta * direction : 0, deltaX: axis === "left" ? delta * direction : 0 }));
      await nextFrame();
    }
  }
}

export async function perfDatabaseScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfDatabase!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const store = useDatabaseWorkspaceStore();
  const tab = workbench.openView("database");
  if (!tab) throw new Error("Could not open database workspace");
  const id = `perf-db-${config.nonce}`;
  const redisId = `perf-redis-${config.nonce}`;
  try {
    await measure("进入数据库页", async () => {
      await router.push({ name: "database" });
      await store.initialize();
    });
    const now = Date.now();
    await store.saveConnection({ id, name: id, dbType: "sqlite", host: "", port: 0, filePath: fixture.filePath, ssl: false, savePassword: false, createdAt: now, updatedAt: now });

    await measure("连接并列出表", async () => {
      (await until(() => overviewConnectAction(id), "connection row", wait)).click();
      await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-browser__name")].find((element) => element.textContent?.trim() === fixture.table), "table in object list", wait);
    });

    if (fixture.extraTables) {
      const search = () => visible<HTMLInputElement>(".db-object-browser__search input");
      await measure(`对象列表搜索框逐字输入（共 ${fixture.extraTables + 1} 张表）`, async () => {
        const input = await until(search, "object search", wait);
        for (const value of ["t", "t_", "t_1", "t_12", "t_123", "t_12", "t_1", "t_", "t", ""]) {
          input.value = value;
          input.dispatchEvent(new Event("input", { bubbles: true }));
          await nextFrame();
        }
        await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-browser__name")].some((element) => element.textContent?.trim() === fixture.table), "full object list", wait);
      });
      await measure(`对象列表滚动（${fixture.extraTables + 1} 张表）`, async () => {
        const element = await until(() => {
          const table = visible(".db-object-table");
          return table ? [...table.querySelectorAll<HTMLElement>("div")].find((div) => div.scrollHeight > div.clientHeight + 2 && div.clientHeight > 100) : undefined;
        }, "object list scroller", wait);
        await scrollThrough(element, "top", 40);
      });
    }

    await measure(`打开 ${fixture.rows / 1000}k 行 × ${fixture.columns} 列的表（首页）`, async () => {
      const name = [...document.querySelectorAll<HTMLElement>(".db-object-browser__name")].find((element) => element.textContent?.trim() === fixture.table)!;
      name.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
      await until(() => visible(".db-tablev2-grid, .db-virtual-grid"), "table grid", wait);
      await until(() => document.querySelectorAll(".db-tablev2-grid .el-table-v2__row, .db-virtual-grid__record").length > 5, "table rows", wait);
    });

    const scroller = () => gridScroller();
    await measure("表格首页上下滚动", async () => {
      const element = await until(scroller, "table scroller", wait);
      await scrollThrough(element, "top", 40);
    });
    await measure("表格左右滚动（宽表）", async () => {
      const element = await until(scroller, "table scroller", wait);
      await scrollThrough(element, "left", 30);
    });

    // A user reads the table before opening a query tab: a short pause.
    await measure("浏览表格后停顿 2 秒", () => new Promise((resolve) => setTimeout(resolve, 2000)));
    // A new SQL tab, then the large SELECT through the real editor and Run button.
    await measure("新建 SQL 查询", async () => {
      (await until(() => visible(".db-object-tabs__new"), "new query button", wait)).click();
      await until(() => visible(".db-query-split .db-sql-editor"), "query editor", wait);
    });
    // First editor of the session vs later ones: two more query tabs.
    for (const round of [2, 3]) {
      await measure(`新建 SQL 查询（第 ${round} 个）`, async () => {
        const before = document.querySelectorAll(".db-object-tabs [role=tab]").length;
        (await until(() => visible(".db-object-tabs__new"), "new query button", wait)).click();
        await until(() => document.querySelectorAll(".db-object-tabs [role=tab]").length > before, "new query tab", wait);
        await until(() => visible(".db-query-split .db-sql-editor"), "query editor", wait);
        await nextFrame();
      });
    }
    // The SQL editor is CodeMirror (components/database/query/SqlCodeEditor.vue).
    const sqlEditor = () => {
      const host = visible<HTMLElement>(".db-query-split .db-sql-code-editor");
      return host ? EditorView.findFromDOM(host) ?? undefined : undefined;
    };
    const setSql = (view: EditorView, sql: string) => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql }, selection: { anchor: sql.length } });
    const runQuery = async (sql: string) => {
      const editor = await until(sqlEditor, "SQL editor instance", wait);
      setSql(editor, sql);
      await nextFrame();
      const run = await until(() => [...document.querySelectorAll<HTMLButtonElement>(".db-sql-toolbar .el-button--primary")].find((button) => !button.disabled), "enabled run button", wait);
      run.click();
    };
    const resultRows = () => document.querySelectorAll(".db-query-split .db-tablev2-grid .el-table-v2__row, .db-query-split .db-virtual-grid__record").length;

    await measure("SQL 编辑器逐字输入 40 个字符（含表名补全）", async () => {
      const editor = await until(sqlEditor, "SQL editor instance", wait);
      setSql(editor, "");
      editor.focus();
      const text = "SELECT * FROM t_1 WHERE c1 = 1 AND t_10.";
      // Typed input: one user-event transaction per character, as a key press makes (it opens
      // the completion list the same way).
      for (const char of text) {
        const at = editor.state.selection.main.head;
        editor.dispatch({ changes: { from: at, insert: char }, selection: { anchor: at + 1 }, userEvent: "input.type" });
        await nextFrame();
      }
      await new Promise((resolve) => setTimeout(resolve, 500));
      closeCompletion(editor);
    });

    await measure("执行 SELECT 100 行", async () => {
      await runQuery(`SELECT * FROM ${fixture.table} LIMIT 100`);
      await until(() => resultRows() > 5 && !store.queryRunning, "100-row result", wait);
    });
    await measure("执行 SELECT 全表（流式上限 5000 行）", async () => {
      await runQuery(`SELECT * FROM ${fixture.table}`);
      await until(() => !store.queryRunning && resultRows() > 5, "large result", wait * 2);
    });
    await measure("大结果上下滚动", async () => {
      const split = await until(() => visible(".db-query-split"), "query split", wait);
      const element = await until(() => gridScroller(split), "result scroller", wait);
      await scrollThrough(element, "top", 60);
    });
    await measure("大结果左右滚动", async () => {
      const split = await until(() => visible(".db-query-split"), "query split", wait);
      const element = await until(() => gridScroller(split), "result scroller", wait);
      await scrollThrough(element, "left", 30);
    });
    await measure("连续执行 10 次 SELECT 1000 行", async () => {
      for (let i = 0; i < 10; i++) {
        await runQuery(`SELECT * FROM ${fixture.table} WHERE id > ${i * 5000} LIMIT 1000`);
        await until(() => store.queryRunning, "query started", wait).catch(() => undefined);
        await until(() => !store.queryRunning, "query finished", wait);
      }
    });
    // Each direction measured on its own: a switch is one click and the frames until it settles.
    const objectTab = (kind: "table" | "query") => {
      const tabs = [...document.querySelectorAll<HTMLElement>(".db-object-tabs [role=tab]")];
      const tableTab = tabs.find((element) => element.textContent?.includes(fixture.table));
      return kind === "table" ? tableTab : tabs.find((element) => element !== tableTab && element.id.includes("query:"));
    };
    for (let i = 0; i < 3; i++) {
      await measure("切到表标签", async () => { objectTab("table")?.click(); await nextFrame(); await nextFrame(); });
      await measure("切回查询标签", async () => { objectTab("query")?.click(); await nextFrame(); await nextFrame(); });
    }
    // Second open of the same table: close its tab, go back to the object list, open it again.
    await measure("关闭表标签后再次打开同一张表", async () => {
      const tableTab = objectTab("table");
      tableTab?.querySelector<HTMLElement>(".is-icon-close")?.click();
      await until(() => !objectTab("table"), "table tab closed", wait);
      const overview = [...document.querySelectorAll<HTMLElement>(".db-object-tabs [role=tab]")].find((element) => element.id.includes("database:"));
      overview?.click();
      const name = await until(() => [...document.querySelectorAll<HTMLElement>(".db-object-browser__name")].find((element) => element.textContent?.trim() === fixture.table), "table in object list", wait);
      name.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
      await until(() => document.querySelectorAll(".db-tablev2-grid .el-table-v2__row, .db-virtual-grid__record").length > 5, "table rows again", wait);
    });
    await measure("空闲 5 秒（数据库页打开）", () => new Promise((resolve) => setTimeout(resolve, 5000)));

    if (fixture.redis) {
      const redis = fixture.redis;
      const now2 = Date.now();
      await store.saveConnection({ id: redisId, name: redisId, dbType: "redis", host: "127.0.0.1", port: redis.port, ssl: false, savePassword: false, createdAt: now2, updatedAt: now2 });
      // Back to the connections overview, as after closing the SQLite connection.
      await store.disconnect(id).catch(() => undefined);
      await router.push({ name: "dashboard" });
      await nextFrame();
      await router.push({ name: "database" });
      const keyRows = () => document.querySelectorAll(".redis-key-row").length;
      await measure(`Redis 连接并列出首批 key（共 ${redis.keys} 个）`, async () => {
        (await until(() => overviewConnectAction(redisId), "redis connection row", wait)).click();
        await until(() => keyRows() > 0, "redis keys", wait);
      });
      const selectKeys = async () => {
        const rows = [...document.querySelectorAll<HTMLElement>(".redis-key-row")].slice(0, 10);
        for (const row of rows) {
          row.click();
          await until(() => row.classList.contains("is-active"), "selected key", wait);
          await nextFrame();
        }
      };
      await measure(`Redis 依次选中 10 个 key（已加载 ${keyRows()} 个）`, selectKeys);
      await measure("Redis 加载更多 20 次", async () => {
        for (let i = 0; i < 20; i++) {
          const before = keyRows();
          (await until(() => visible<HTMLButtonElement>(".redis-load-more"), "load more", wait)).click();
          await until(() => keyRows() > before, "more keys", wait);
        }
      });
      await measure(`Redis key 列表滚动到底再回顶部（${keyRows()} 个）`, async () => {
        const wrap = await until(() => visible<HTMLElement>(".redis-key-list .el-scrollbar__wrap"), "key list scroller", wait);
        for (let i = 0; i <= 40; i++) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
        for (let i = 40; i >= 0; i--) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
      });
      await measure(`Redis 依次选中 10 个 key（已加载 ${keyRows()} 个）`, selectKeys);
      await measure("Redis key 搜索输入 6 个字符", async () => {
        const input = await until(() => visible<HTMLInputElement>(".redis-key-search input"), "redis search", wait);
        for (const value of ["p", "pe", "per", "perf", "perf:", "perf:k"]) {
          input.value = value;
          input.dispatchEvent(new Event("input", { bubbles: true }));
          await nextFrame();
        }
        await until(() => keyRows() > 0, "filtered keys", wait);
      });
    }
  } finally {
    await store.disconnect(redisId).catch(() => undefined);
    await store.deleteConnection(redisId).catch(() => undefined);
    await store.disconnect(id).catch(() => undefined);
    await store.deleteConnection(id).catch(() => undefined);
    workbench.closeTab(tab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
  await measure("关闭数据库页后空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
}
