// Opt-in native performance probe (-tags e2e builds only). Drives common UI paths in the
// real WebView and reports, per step: wall time, frame-time statistics from
// requestAnimationFrame, and DOM size. WebKit has no Long Tasks API or heap stats, so
// frames > 50 ms are the jank signal; process memory is sampled by the harness.
import router from "@/router";
import { executeRegisteredCommand } from "@/services/command-registry";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { enqueueProjectFiles } from "@/services/external-file-open";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { useLocalProjectStore } from "@/stores/local-project";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import * as monaco from "@/services/monaco-runtime.js";
import { createMeasure, FrameMonitor, nextFrame, round, until, type PerfStep } from "@/services/e2e-perf-kit";



export async function perfScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.perf!;
  const wait = config.timeoutMs;
  const results: PerfStep[] = [];
  const project = useLocalProjectStore();
  const workbench = useWorkbenchStore();

  const measure = createMeasure(log, results);

  const navigation = performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined;
  const scripts = performance.getEntriesByType("resource").filter((entry) => entry.name.endsWith(".js")) as PerformanceResourceTiming[];
  log.done(`PERF ${JSON.stringify({ step: "启动（页面）", ms: round(performance.now()), domContentLoadedMs: round(navigation?.domContentLoadedEventEnd ?? 0), loadMs: round(navigation?.loadEventEnd ?? 0), scripts: scripts.length, domNodes: document.getElementsByTagName("*").length })}`);
  async function pageSwitching(phase: string) {
    // Main-page switching the way the sidebar does it (AppSidebar.openItem): open or activate the
    // view's tab, then navigate. A view with an open tab stays cached by KeepAlive, so the first
    // visit mounts it and later visits only re-activate it.
    const pages = [["dashboard", "概览"], ["projects", "本地项目"], ["database", "数据库"], ["plugins", "插件管理"]] as const;
    const viewTabIds: string[] = [];
    const visit = async (name: (typeof pages)[number][0]) => {
      const tab = workbench.openView(name);
      if (tab && name !== "dashboard" && !viewTabIds.includes(tab.id)) viewTabIds.push(tab.id);
      await router.push({ name }).catch(() => undefined);
      await until(() => router.currentRoute.value.name === name, `route ${name}`, wait);
    };
    try {
      for (const [name, label] of pages.slice(1)) {
        await measure(`${phase}·首次进入「${label}」`, () => visit(name));
      }
      await measure(`${phase}·在 4 个已打开页面间切换 5 轮`, async () => {
        for (let round = 0; round < 5; round++) {
          for (const [name] of pages) { await visit(name); await nextFrame(); }
        }
      });
      // Per-switch detail: time from navigation to the first frame painted after it, for every
      // ordered pair of pages, three passes, so a slow pair stands out from background noise.
      const switches: Record<string, number[]> = {};
      await measure(`${phase}·逐次切换明细（12 种顺序 × 3 轮）`, async () => {
        for (let pass = 0; pass < 3; pass++) {
          for (const [from, fromLabel] of pages) {
            for (const [to, toLabel] of pages) {
              if (from === to) continue;
              await visit(from);
              await nextFrame();
              await nextFrame();
              const started = performance.now();
              await visit(to);
              await nextFrame();
              (switches[`${fromLabel}→${toLabel}`] ??= []).push(round(performance.now() - started));
            }
          }
          // Idle frames right after the pass: long frames here would be background work.
          const idle = new FrameMonitor();
          idle.start();
          await new Promise((resolve) => setTimeout(resolve, 2000));
          const quiet = idle.stop();
          (switches[`空闲2秒(第${pass + 1}轮后)`] ??= []).push(quiet.longFrames, round(quiet.maxFrameMs), quiet.frames);
        }
      });
      log.done(`PERF ${JSON.stringify({ step: `${phase}·逐次切换（ms，3 轮）`, ...switches })}`);
    } finally {
      for (const id of viewTabIds) workbench.closeTab(id, true);
    }
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }

  // Idle first: the memory the app settles at before any scenario step.
  await measure("空闲 5 秒（启动后）", () => new Promise((resolve) => setTimeout(resolve, 5000)));
  // Page switching once early (small heap) and again late (after heavy steps), to tell a
  // slow switch from garbage-collection pressure left by earlier work.
  await pageSwitching("前期");
  if (fixture.only === "pages") {
    await pageSwitching("再次");
    return;
  }

  const modelFor = (path: string) => monaco.editor.getModels().find((model) => model.uri.fsPath === path || model.uri.path === path);
  const editorFor = (path: string) => { const model = modelFor(path); return model ? monaco.editor.getEditors().find((item) => item.getModel() === model) : undefined; };
  const treeRows = () => [...document.querySelectorAll<HTMLElement>(".project-virtual-tree__list > .project-virtual-tree__row")];
  const treeNodes = () => treeRows().length;
  const treeScroller = () => document.querySelector<HTMLElement>(".project-virtual-tree > .el-scrollbar__wrap");

  const projectTab = workbench.openView("projects");
  if (!projectTab) throw new Error("Could not open project workspace");
  try {
    await measure("切换到项目页", async () => { await router.push({ name: "projects" }); });

    await measure(`打开大项目（${fixture.fileCount} 个文件）`, async () => {
      enqueueProjectFiles([fixture.projectRoot]);
      await until(() => project.projectRoot === fixture.projectRoot, "project root", wait);
      await until(() => treeNodes() > 3, "file tree", wait);
    });

    await measure("展开 40 个一级目录（每个 100 个文件）", async () => {
      // Expand bottom-up among rendered rows so an insertion never moves the next target;
      // wait for each folder's children, then scroll on when no collapsed folder is in view.
      let expanded = 0;
      while (expanded < 40) {
        const collapsed = treeRows().filter((row) => row.getAttribute("aria-level") === "2" && row.getAttribute("aria-expanded") === "false");
        const target = collapsed[collapsed.length - 1];
        if (!target) {
          const scroller = treeScroller();
          if (!scroller || scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight) break;
          scroller.scrollTop += scroller.clientHeight;
          await nextFrame();
          continue;
        }
        const key = target.dataset.key;
        target.click();
        await until(() => treeRows().some((row) => row.dataset.key === key && row.getAttribute("aria-expanded") === "true"), `expanded ${key}`, wait);
        expanded += 1;
        await nextFrame();
      }
      if (expanded < 40) throw new Error(`only ${expanded} folders expanded`);
    });

    await measure("文件树滚动到底再回顶部", async () => {
      const wrap = treeScroller();
      if (!wrap) throw new Error("tree scroll container not found");
      for (let i = 0; i <= 40; i++) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
      for (let i = 40; i >= 0; i--) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
    });

    // Project-wide search: "export" is in every file, so the plain search hits its cap (300)
    // and the case-sensitive (regex backend) search hits its larger cap (2000).
    const searchInput = () => document.querySelector<HTMLInputElement>(".project-search-input input");
    const searchResults = () => document.querySelectorAll(".project-search-result").length;
    const setSearch = (value: string, submit: boolean) => {
      const input = searchInput()!;
      input.value = value;
      input.dispatchEvent(new Event("input", { bubbles: true }));
      if (submit) input.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
    };
    await measure("项目搜索 export（300 条结果）", async () => {
      await until(searchInput, "project search input", wait);
      setSearch("export", true);
      await until(() => searchResults() >= 300, "300 search results", wait);
    });
    const caseTool = () => document.querySelectorAll<HTMLElement>(".project-search-tools .project-search-tool")[1];
    await measure("区分大小写再搜（2000 条结果）", async () => {
      caseTool().click();
      await until(() => searchResults() >= 2000, "2000 search results", wait);
    });
    await measure("搜索结果滚动到底再回顶部", async () => {
      const wrap = await until(() => [...document.querySelectorAll<HTMLElement>(".project-sidebar-scroll .el-scrollbar__wrap")].find((element) => element.clientHeight > 0 && element.querySelector(".project-search-result")), "search scroller", wait);
      for (let i = 0; i <= 40; i++) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
      for (let i = 40; i >= 0; i--) { wrap.scrollTop = (wrap.scrollHeight * i) / 40; await nextFrame(); }
    });
    await measure("清空搜索回到文件树", async () => {
      setSearch("", false);
      await until(() => searchResults() === 0 && treeNodes() > 3, "file tree after search", wait);
    });
    caseTool().click();

    // Command palette over the open project (default scope: project files).
    const paletteInput = () => document.querySelector<HTMLInputElement>(".app-command-palette input");
    const paletteRows = () => document.querySelectorAll(".app-command-palette__body [role=option]").length;
    await measure("打开命令面板", async () => {
      await executeRegisteredCommand("builtin:command-palette");
      await until(paletteInput, "command palette input", wait);
    });
    await measure("命令面板逐字输入 file-42（5000 个文件）", async () => {
      const input = paletteInput()!;
      for (const value of ["f", "fi", "fil", "file", "file-", "file-4", "file-42"]) {
        input.value = value;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        await nextFrame();
      }
      await until(() => paletteRows() > 0, "palette results", wait);
    });
    await measure("命令面板切到命令（>）并输入", async () => {
      const input = paletteInput()!;
      for (const value of ["", ">", "> ", "> o", "> op", "> ope", "> open"]) {
        input.value = value;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        await nextFrame();
      }
      await until(() => paletteRows() > 0, "command results", wait);
    });
    await measure("关闭命令面板", async () => {
      paletteInput()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      await until(() => !paletteInput(), "palette closed", wait);
    });

    await measure(`打开大文件（${fixture.bigFileLines} 行）`, async () => {
      enqueueProjectFiles([fixture.bigFile]);
      await until(() => editorFor(fixture.bigFile), "big file editor", wait);
    });

    const big = editorFor(fixture.bigFile);
    if (big) {
      await measure("编辑器滚动到底再回顶部（120 帧）", async () => {
        const height = big.getScrollHeight();
        for (let i = 0; i <= 60; i++) { big.setScrollTop((height * i) / 60); await nextFrame(); }
        for (let i = 60; i >= 0; i--) { big.setScrollTop((height * i) / 60); await nextFrame(); }
      });
      await measure("编辑器连续输入 200 个字符", async () => {
        big.setPosition({ lineNumber: 10, column: 1 });
        big.focus();
        for (let i = 0; i < 200; i++) { big.trigger("keyboard", "type", { text: i % 40 === 39 ? "\n" : "x" }); await nextFrame(); }
      });
      await measure("撤销 200 次输入", async () => {
        for (let i = 0; i < 200; i++) big.trigger("keyboard", "undo", null);
        await nextFrame();
      });
    }

    await measure(`依次打开 ${fixture.smallFiles.length} 个文件`, async () => {
      for (const file of fixture.smallFiles) {
        enqueueProjectFiles([file]);
        await until(() => editorFor(file), `editor for ${file}`, wait);
      }
    });

    await measure(`在 ${fixture.smallFiles.length} 个标签间来回切换 3 轮`, async () => {
      for (let round = 0; round < 3; round++) {
        for (const file of fixture.smallFiles) {
          project.openDocument(file);
          await until(() => editorFor(file)?.getDomNode()?.offsetParent, `visible ${file}`, wait);
        }
      }
    });

    const before = document.getElementsByTagName("*").length;
    await measure("反复打开关闭同一文件 30 次", async () => {
      for (let i = 0; i < 30; i++) {
        enqueueProjectFiles([fixture.smallFiles[0]]);
        await until(() => editorFor(fixture.smallFiles[0]), "reopen", wait);
        project.closeDocument(fixture.smallFiles[0]);
        await until(() => !project.documents.some((doc) => doc.path === fixture.smallFiles[0]), "closed", wait);
      }
    });
    const models = monaco.editor.getModels().length;
    log.done(`PERF ${JSON.stringify({ step: "开关文件后残留", domNodesBefore: before, domNodesAfter: document.getElementsByTagName("*").length, monacoModels: models, openDocuments: project.documents.length })}`);

    for (const file of [...fixture.smallFiles, fixture.bigFile]) project.closeDocument(file);

    // Many editor tabs at once: 60 files across modules, switch through them, close them one by one.
    const manyFiles = Array.from({ length: 60 }, (_, i) => `${fixture.projectRoot}/module-${String(i % 50).padStart(2, "0")}/file-${10 + Math.floor(i / 50)}.ts`);
    await measure(`依次打开 ${manyFiles.length} 个文件（多标签）`, async () => {
      for (const file of manyFiles) {
        enqueueProjectFiles([file]);
        await until(() => editorFor(file), `editor for ${file}`, wait);
      }
    });
    log.done(`PERF ${JSON.stringify({ step: "多标签打开后", domNodes: document.getElementsByTagName("*").length, monacoEditors: monaco.editor.getEditors().length, openDocuments: project.documents.length })}`);
    await measure(`在 ${manyFiles.length} 个标签间切换一轮`, async () => {
      for (const file of manyFiles) {
        project.openDocument(file);
        await until(() => editorFor(file)?.getDomNode()?.offsetParent, `visible ${file}`, wait);
        await nextFrame();
      }
    });
    await measure(`在编辑器里输入 100 个字符（${manyFiles.length} 个标签打开）`, async () => {
      const editor = editorFor(manyFiles[manyFiles.length - 1]!)!;
      editor.setPosition({ lineNumber: 1, column: 1 });
      editor.focus();
      for (let i = 0; i < 100; i++) { editor.trigger("keyboard", "type", { text: "x" }); await nextFrame(); }
      editor.trigger("keyboard", "undo", null);
    });
    await measure(`逐个关闭 ${manyFiles.length} 个标签`, async () => {
      for (const file of [...manyFiles].reverse()) {
        project.closeDocument(file);
        await nextFrame();
      }
      await until(() => !project.documents.some((doc) => manyFiles.includes(doc.path)), "tabs closed", wait);
    });

    // Two workspaces: A holds the large project, B a fresh project page; switch back and forth.
    const workspaceA = workbench.activeWorkspaceId;
    const tabA = workbench.activeTab;
    const workspaceB = workbench.createWorkspace(`perf-b-${config.nonce}`);
    if (workspaceB) {
      try {
        const tabB = workbench.openView("projects");
        if (tabB) await router.push(workspaceTabRoute(tabB));
        await nextFrame();
        const go = async (id: string) => {
          const tab = workbench.switchWorkspace(id);
          await router.push(tab ? workspaceTabRoute(tab) : { name: "dashboard" });
        };
        await measure("在两个工作区之间来回切换 5 轮（一个开着大项目）", async () => {
          for (let i = 0; i < 5; i++) {
            await go(workspaceA);
            await until(() => project.projectRoot === fixture.projectRoot && treeNodes() > 3, "project A back", wait);
            await nextFrame();
            await go(workspaceB.id);
            await until(() => project.projectRoot !== fixture.projectRoot, "workspace B", wait);
            await nextFrame();
          }
        });
      } finally {
        await (async () => { const tab = workbench.switchWorkspace(workspaceA); await router.push(tab ? workspaceTabRoute(tab) : { name: "projects" }); })();
        workbench.deleteWorkspace(workspaceB.id);
        if (tabA) await router.push(workspaceTabRoute(tabA)).catch(() => undefined);
      }
    } else {
      log.done("PERF-NOTE 无法新建第二个工作区（配额），跳过工作区切换");
    }

    // Big-file comparison: the same 20k lines as plain text and as TypeScript, each opened,
    // scrolled end to end and closed, with memory read at every step end.
    const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
    try {
      for (const [label, file] of [["纯文本", fixture.bigPlainFile], ["TS", fixture.bigFile]] as const) {
        if (!file) continue;
        await measure(`对照·${label}：打开前空闲 3 秒`, () => sleep(3000));
        await measure(`对照·${label}：打开 2 万行`, async () => {
          enqueueProjectFiles([file]);
          await until(() => editorFor(file), `${label} editor`, wait);
        });
        const editor = editorFor(file);
        if (editor) {
          await measure(`对照·${label}：从头滚到尾再回来`, async () => {
            const height = editor.getScrollHeight();
            for (let i = 0; i <= 60; i++) { editor.setScrollTop((height * i) / 60); await nextFrame(); }
            for (let i = 60; i >= 0; i--) { editor.setScrollTop((height * i) / 60); await nextFrame(); }
          });
          await measure(`对照·${label}：滚完停 3 秒`, () => sleep(3000));
        }
        project.closeDocument(file);
        await measure(`对照·${label}：关闭后空闲 5 秒`, () => sleep(5000));
      }
    } finally {
      for (const file of [fixture.bigPlainFile, fixture.bigFile]) if (file) project.closeDocument(file);
    }
  } finally {
    workbench.closeTab(projectTab.id, true);
  }

  // Terminal flood: a real PTY printing many lines into a real terminal tab.
  const preferences = useWorkspaceStatePreferencesStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
  const openTerminalTab = async (title: string) => {
    const tab = workbench.createTab("terminal", title);
    if (!tab) throw new Error("Could not open terminal tab");
    const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
    terminalWorkspace.activateTab(tab.id, {}, target.layoutId, target.fallbackLayoutId);
    await router.push(workspaceTabRoute(tab));
    await until(() => document.querySelector(".xterm-screen"), "xterm", wait);
    return tab;
  };
  /** The active pane's PTY once it is alive; the pane id is re-read each poll (the tab may still be wiring up). */
  const aliveShell = async () => {
    const end = performance.now() + wait;
    while (performance.now() < end) {
      const id = terminalWorkspace.activePaneId;
      if (id && (await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId: id } }).catch(() => false))) return id;
      await pause(100);
    }
    throw new Error("terminal session did not start");
  };
  const flood = async (lines: number) => {
    const paneId = await aliveShell();
    const marker = `PERF_DONE_${config.nonce}`;
    let finished = false;
    const stop = listenAppEvent(`shell-output:${paneId}`, (payload: unknown) => { if (JSON.stringify(payload).includes(marker)) finished = true; });
    try {
      // The echoed command line shows PERF_DONE_""<nonce>; only the real output joins the marker.
      await invokeBackend("write_to_shell", { payload: { sessionId: paneId, input: `seq 1 ${lines}; echo PERF_DONE_""${config.nonce}\r` } });
      await until(() => finished, "terminal flood to finish", wait);
    } finally { stop(); }
  };

  let termTabId = "";
  try {
    await measure("打开终端标签", async () => { termTabId = (await openTerminalTab(`Perf ${config.nonce}`)).id; });
    if (termTabId) await measure(`终端输出 ${fixture.terminalLines} 行`, () => flood(fixture.terminalLines));
  } finally {
    if (termTabId) workbench.closeTab(termTabId, true);
  }

  await pageSwitching("后期");
  // Idle last: what stays allocated once everything is closed and GC has had time to run.
  await measure("空闲 10 秒（全部关闭后）", () => new Promise((resolve) => setTimeout(resolve, 10000)));

  // Leak check: repeat the same open/use/close cycle. Idle memory that keeps rising round
  // after round is a leak; memory that settles after the first round is a cache/heap high-water.
  for (let cycle = 1; cycle <= 3; cycle += 1) {
    await measure(`循环 ${cycle}：大文件 + 终端，开了再关`, async () => {
      const tab = workbench.openView("projects");
      if (!tab) throw new Error("Could not open project workspace");
      try {
        await router.push({ name: "projects" });
        enqueueProjectFiles([fixture.bigFile]);
        const editor = await until(() => editorFor(fixture.bigFile), "big file editor", wait);
        const height = editor.getScrollHeight();
        for (let i = 0; i <= 30; i++) { editor.setScrollTop((height * i) / 30); await nextFrame(); }
        project.closeDocument(fixture.bigFile);
        await until(() => !project.documents.some((doc) => doc.path === fixture.bigFile), "big file closed", wait);
      } finally {
        workbench.closeTab(tab.id, true);
      }
      const term = await openTerminalTab(`Perf ${config.nonce} ${cycle}`);
      try {
        await flood(50_000);
      } finally {
        workbench.closeTab(term.id, true);
      }
      await router.push({ name: "dashboard" }).catch(() => undefined);
    });
    log.done(`PERF ${JSON.stringify({ step: `循环 ${cycle} 后残留`, monacoEditors: monaco.editor.getEditors().length, monacoModels: monaco.editor.getModels().length, xtermScreens: document.querySelectorAll(".xterm-screen").length, openTabs: workbench.tabs.length, domNodes: document.getElementsByTagName("*").length })}`);
    await measure(`循环 ${cycle} 后空闲 5 秒`, () => new Promise((resolve) => setTimeout(resolve, 5000)));
  }

  const failed = results.filter((row) => row.error);
  if (failed.length) log.done(`PERF_ERRORS ${JSON.stringify(failed.map((row) => `${row.step}: ${row.error}`))}`);
}
