// Loaded only by the native -tags e2e smoke runner. Drives the real project
// workspace against a real gopls: tree click, Monaco providers, file watching
// and the problems panel, i.e. the Monaco → IPC → LSP chain end to end.
import router from "@/router";
import * as monaco from "@/services/monaco-runtime.js";
import type { editor } from "@/services/monaco-runtime.js";
import { enqueueProjectFiles } from "@/services/external-file-open";
import { lspRuntimeState } from "@/services/lsp-runtime-state";
import { invokeBackend } from "@/services/desktop-bridge";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";
import { messagesZh } from "@/i18n/locales/zh";
import { messagesEn } from "@/i18n/locales/en";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";

const SERVER = "gopls";

async function until<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`Timed out waiting for ${label}`);
}

function modelFor(fileName: string): editor.ITextModel | undefined {
  return monaco.editor.getModels().find((model) => model.uri.path.endsWith(`/${fileName}`));
}

function editorFor(model: editor.ITextModel): editor.ICodeEditor | undefined {
  return monaco.editor.getEditors().find((item) => item.getModel() === model);
}

function markerMessages(model: editor.ITextModel): string[] {
  return monaco.editor.getModelMarkers({ resource: model.uri }).map((marker) => marker.message);
}

/** 1-based position of the first `needle` on the line that also contains `lineHint`. */
function positionOf(model: editor.ITextModel, lineHint: string, needle: string) {
  for (let line = 1; line <= model.getLineCount(); line++) {
    const text = model.getLineContent(line);
    const index = text.indexOf(needle);
    if (text.includes(lineHint) && index >= 0) return { lineNumber: line, column: index + 2 };
  }
  throw new Error(`"${needle}" not found on a line with "${lineHint}"`);
}

function visibleText(selector: string): string {
  return [...document.querySelectorAll<HTMLElement>(selector)]
    .filter((element) => element.offsetParent !== null)
    .map((element) => element.textContent ?? "")
    .join("\n");
}

export async function lspScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.lsp!;
  const wait = config.timeoutMs;
  const project = useLocalProjectStore();
  const workbench = useWorkbenchStore();
  const previousServers = [...project.lspSettings.enabledServers];
  const tab = workbench.openView("projects");
  if (!tab) throw new Error("Could not open project workspace");
  await router.push({ name: "projects" });
  try {
    project.updateLspSettings({ enabledServers: [...new Set([...previousServers, SERVER])] });
    enqueueProjectFiles([fixture.projectRoot]);
    await until(() => project.projectRoot === fixture.projectRoot, "project to open", wait);
    log.done("fixture Go project opened through the real external-open path");

    // Re-queried on each use: the tree re-renders when files appear (generated.txt, util.go).
    const mainNode = () => until(
      () => [...document.querySelectorAll<HTMLElement>(".project-virtual-tree__list > .project-virtual-tree__row")].find((node) => node.textContent?.trim() === "main.go"),
      "main.go in the explorer tree",
      wait,
    );
    (await mainNode()).click();
    const main = await until(() => modelFor("main.go"), "main.go Monaco model", wait);
    const mainEditor = await until(() => editorFor(main), "main.go editor", wait);
    log.done("main.go opened by clicking the explorer tree");

    await until(() => lspRuntimeState(project.projectRoot, SERVER)?.phase === "ready", "gopls ready", wait * 2);
    log.done("gopls initialized and reported ready for the project");

    // CodeLens: gopls offers "run go generate" above //go:generate; clicking it
    // (a real mousedown on the lens link) must run gopls.generate, also after
    // gopls was stopped and started again.
    const clickGenerateLens = async (label: string) => {
      await invokeBackend("local_fs_delete", { projectRoot: project.projectRoot, path: "generated.txt" }).catch(() => undefined);
      const lensLink = await until(
        () => [...document.querySelectorAll<HTMLElement>(".codelens-decoration a")].find((link) => link.isConnected && link.textContent?.trim() === "run go generate"),
        `run go generate CodeLens (${label})`,
        wait * 2,
      );
      const box = lensLink.getBoundingClientRect();
      const at = { bubbles: true, cancelable: true, clientX: box.left + 2, clientY: box.top + box.height / 2, button: 0, buttons: 1 };
      lensLink.dispatchEvent(new PointerEvent("pointerdown", { ...at, pointerId: 1, isPrimary: true, pointerType: "mouse" }));
      lensLink.dispatchEvent(new MouseEvent("mousedown", at));
      lensLink.dispatchEvent(new PointerEvent("pointerup", { ...at, buttons: 0, pointerId: 1, isPrimary: true, pointerType: "mouse" }));
      lensLink.dispatchEvent(new MouseEvent("mouseup", { ...at, buttons: 0 }));
      lensLink.dispatchEvent(new MouseEvent("click", { ...at, buttons: 0 }));
      const deadline = performance.now() + wait;
      for (;;) {
        const read = await invokeBackend("local_fs_read_text_file", { projectRoot: project.projectRoot, filePath: "generated.txt" }).then(() => true, () => false);
        if (read) break;
        if (performance.now() > deadline) throw new Error(`clicking the run go generate CodeLens did not create generated.txt (${label})`);
        await new Promise<void>((resolve) => setTimeout(resolve, 200));
      }
      // gopls reports the result through window/showMessage ("completed").
      await until(
        () => [...document.querySelectorAll<HTMLElement>(".el-message")].some((toast) => toast.textContent?.includes("gopls") && toast.textContent.includes("completed")),
        `gopls "completed" toast (${label})`,
        wait,
      );
      log.done(`clicking the gopls CodeLens ran go generate and showed gopls's "completed" message (${label})`);
    };
    await clickGenerateLens("first start");
    project.updateLspSettings({ enabledServers: previousServers.filter((id) => id !== SERVER) });
    await until(() => lspRuntimeState(project.projectRoot, SERVER)?.phase !== "ready", "gopls stopped", wait);
    await until(() => !document.querySelector(".codelens-decoration a"), "gopls CodeLens removed after stop", wait);
    project.updateLspSettings({ enabledServers: [...new Set([...previousServers, SERVER])] });
    await until(() => lspRuntimeState(project.projectRoot, SERVER)?.phase === "ready", "gopls ready again", wait * 2);
    await clickGenerateLens("after stop and start");

    await until(() => markerMessages(main).some((message) => message.includes("Helper")), "undefined Helper diagnostic", wait * 2);
    log.done(`gopls diagnostic shown as editor marker: ${markerMessages(main).find((message) => message.includes("Helper"))}`);

    const call = positionOf(main, "fmt.Println(greet", "greet");
    mainEditor.focus();
    mainEditor.setPosition(call);
    await mainEditor.getAction("editor.action.showHover")?.run();
    const hover = await until(() => {
      const text = visibleText(".monaco-hover");
      return text.includes("func greet") ? text : undefined;
    }, "hover with greet signature", wait);
    log.done(`hover returned real type info: ${hover.replace(/\s+/g, " ").trim().slice(0, 80)}`);

    const definitionLine = positionOf(main, "func greet", "greet").lineNumber;
    mainEditor.focus();
    mainEditor.setPosition(call);
    // The keyboard source is the F12 / Cmd+click path. Calling getAction(...).run()
    // directly resolves the location but does not navigate, so it is not used here.
    mainEditor.trigger("keyboard", "editor.action.revealDefinition", null);
    const moved = await until(() => mainEditor.getPosition()?.lineNumber === definitionLine, "go to definition", wait).then(() => true, () => false);
    if (!moved) {
      // Report what the providers returned, separating "no locations" from
      // "locations Monaco could not navigate to".
      const commands = (mainEditor as unknown as { _commandService?: { executeCommand(id: string, ...args: unknown[]): Promise<unknown> } })._commandService;
      const locations = await commands?.executeCommand("_executeDefinitionProvider", main.uri, call).catch((reason: unknown) => `error: ${String(reason)}`);
      throw new Error(`go to definition did not move the cursor; cursor=${JSON.stringify(mainEditor.getPosition())} model=${main.uri.toString()} providers=${JSON.stringify(locations)}`);
    }
    log.done(`go to definition moved the cursor from line ${call.lineNumber} to line ${definitionLine}`);

    // Cmd/Ctrl + hover: Monaco's link gesture must underline the call and show
    // the pointer, or a working Cmd+click looks unclickable.
    mainEditor.setPosition(call);
    mainEditor.revealPositionInCenter(call);
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    const visible = mainEditor.getScrolledVisiblePosition({ lineNumber: call.lineNumber, column: call.column + 1 });
    const editorNode = mainEditor.getDomNode();
    if (!visible || !editorNode) throw new Error("greet call is not visible for the Cmd hover check");
    const box = editorNode.getBoundingClientRect();
    const point = { clientX: box.left + visible.left + 2, clientY: box.top + visible.top + visible.height / 2 };
    const mac = /Mac/.test(navigator.platform);
    const modifier = mac ? { metaKey: true } : { ctrlKey: true };
    const hoverTarget = document.elementFromPoint(point.clientX, point.clientY);
    if (!hoverTarget) throw new Error("no element under the greet call");
    hoverTarget.dispatchEvent(new MouseEvent("mousemove", { bubbles: true, ...point, ...modifier }));
    const link = await until(() => editorNode.querySelector<HTMLElement>(".goto-definition-link"), "Cmd hover link decoration", wait).catch(() => undefined);
    const under = document.elementFromPoint(point.clientX, point.clientY) as HTMLElement | null;
    const cursor = under ? getComputedStyle(under).cursor : "none";
    hoverTarget.dispatchEvent(new MouseEvent("mousemove", { bubbles: true, ...point }));
    if (!link || cursor !== "pointer") {
      throw new Error(`Cmd hover: link=${link ? `"${link.textContent}"` : "none"} cursor=${cursor} under=${under?.className ?? "none"} target=${hoverTarget.className}`);
    }
    log.done(`Cmd hover underlined "${link.textContent}" with a pointer cursor`);

    const insertAfter = call.lineNumber;
    const insertColumn = main.getLineMaxColumn(insertAfter);
    mainEditor.executeEdits("e2e-lsp", [{ range: new monaco.Range(insertAfter, insertColumn, insertAfter, insertColumn), text: "\n\tfmt.Prin" }]);
    mainEditor.setPosition({ lineNumber: insertAfter + 1, column: main.getLineMaxColumn(insertAfter + 1) });
    await mainEditor.getAction("editor.action.triggerSuggest")?.run();
    const suggestion = await until(() => {
      const rows = [...document.querySelectorAll<HTMLElement>(".suggest-widget .monaco-list-row")];
      return rows.find((row) => (row.getAttribute("aria-label") ?? row.textContent ?? "").includes("Println"));
    }, "Println completion", wait);
    log.done(`completion offered ${suggestion.getAttribute("aria-label")?.split(",")[0] ?? "Println"}`);
    mainEditor.trigger("e2e-lsp", "hideSuggestWidget", null);
    mainEditor.trigger("e2e-lsp", "undo", null);
    await until(() => !main.getValue().includes("fmt.Prin\n"), "completion edit undone", wait);

    await invokeBackend("local_fs_write_text_file", {
      projectRoot: project.projectRoot,
      filePath: "util.go",
      content: "package main\n\nfunc Helper() string { return \"ok\" }\n",
    });
    await until(() => !markerMessages(main).some((message) => message.includes("Helper")), "Helper diagnostic cleared after util.go appeared", wait * 2);
    log.done("util.go written outside the editor; gopls picked it up through the file watcher and cleared the diagnostic");

    // Cross-file go to definition: into another project file, then outside the project (Go standard library).
    const jumpFromMain = async (lineHint: string, needle: string, label: string) => {
      const mainNow = await until(() => modelFor("main.go"), "main.go model", wait);
      const editorNow = await until(() => editorFor(mainNow), "main.go editor", wait);
      const at = positionOf(mainNow, lineHint, needle);
      editorNow.focus();
      editorNow.setPosition(at);
      editorNow.trigger("keyboard", "editor.action.revealDefinition", null);
      return { at, label };
    };
    await jumpFromMain("fmt.Println(Helper", "Helper", "Helper");
    const util = await until(() => project.activePath?.endsWith("/util.go") && modelFor("util.go"), "util.go opened by go to definition", wait);
    const utilEditor = await until(() => editorFor(util), "util.go editor", wait);
    const helperLine = positionOf(util, "func Helper", "Helper").lineNumber;
    await until(() => utilEditor.getPosition()?.lineNumber === helperLine, "cursor on func Helper", wait);
    log.done(`go to definition opened util.go at line ${helperLine} (another project file)`);

    (await mainNode()).click();
    await until(() => project.activePath?.endsWith("/main.go"), "main.go active again", wait);
    await jumpFromMain("fmt.Println(greet", "Println", "Println");
    const library = await until(() => project.activePath?.endsWith("/fmt/print.go") && modelFor("print.go"), "fmt/print.go opened by go to definition", wait * 2);
    const libraryEditor = await until(() => editorFor(library), "print.go editor", wait);
    const printlnLine = positionOf(library, "func Println(", "Println").lineNumber;
    await until(() => libraryEditor.getPosition()?.lineNumber === printlnLine, "cursor on func Println", wait);
    log.done(`go to definition opened the standard library ${project.activePath} at line ${printlnLine} (outside the project)`);

    const problemsLabels = new Set([messagesZh["project.problems.tab"], messagesEn["project.problems.tab"]]);
    const problemsTab = await until(
      () => [...document.querySelectorAll<HTMLElement>(".project-sidebar-tabs__tab")].find((item) => problemsLabels.has(item.getAttribute("aria-label") ?? "")),
      "problems sidebar tab",
      wait,
    );
    problemsTab.click();
    const brokenFile = await until(
      () => [...document.querySelectorAll<HTMLElement>(".project-problems-panel__file")].find((item) => item.querySelector(".project-problems-panel__file-name")?.textContent?.trim() === "broken.go"),
      "broken.go in the problems panel",
      wait * 2,
    );
    log.done("problems panel lists the type error in broken.go, which was never opened");
    const entry = await until(
      () => brokenFile.parentElement?.querySelector<HTMLElement>(".project-problems-panel__entry"),
      "broken.go problem entry",
      wait,
    );
    entry.click();
    const broken = await until(() => modelFor("broken.go"), "broken.go opened from the problems panel", wait);
    const brokenEditor = await until(() => editorFor(broken), "broken.go editor", wait);
    const errorLine = positionOf(broken, "brokenValue", "brokenValue").lineNumber;
    await until(() => brokenEditor.getPosition()?.lineNumber === errorLine, "cursor on the error line", wait);
    await until(() => markerMessages(broken).length > 0, "broken.go marker", wait);
    log.done(`clicking the problem opened broken.go at line ${errorLine} with the marker shown`);
  } finally {
    if (project.projectRoot === fixture.projectRoot) project.removeProject(fixture.projectRoot);
    project.updateLspSettings({ enabledServers: previousServers });
    workbench.closeTab(tab.id, true);
  }
}
