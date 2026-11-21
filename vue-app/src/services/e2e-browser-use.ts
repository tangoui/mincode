/**
 * macOS browser-use smoke (design §9): every page interaction goes through the
 * AI tool path (`embedded_browser_ai_execute` → ExecuteAITool), against the
 * loopback pages from tools/e2e/desktop-smoke.mjs. The browser workspace view
 * is the real one: `browser.open_tab` routes to it, it adopts popups and
 * answers `browser.list_tabs` / `browser.switch_tab`. The file chooser and the
 * save panel are answered by the `-tags e2e` native hooks
 * (backend/browser/e2e_hooks_e2e.go). Loaded only by the smoke runner.
 */
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { useBrowserWorkspaceStore } from "@/stores/browser-workspace";
import { invokeBackend, listenAppEvent, nativeErrorMessage } from "@/services/desktop-bridge";

interface StepLog {
  done(step: string): void;
}

interface SnapshotElement {
  ref: string;
  tag: string;
  role?: string;
  name?: string;
  frame?: string;
  shadow?: boolean;
  inViewport?: boolean;
  selectedCount?: number;
  options?: Array<{ value: string; selected: boolean }>;
}

interface PageSnapshot {
  snapshotId: string;
  url: string;
  title: string;
  text: string;
  elements: SnapshotElement[];
}

interface ToolFailure {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}

interface DownloadRecord {
  id: string;
  tabId: string;
  filename: string;
  state: string;
  error?: string;
  bytes: number;
}

const delay = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

function toolFailure(error: unknown): ToolFailure {
  const message = nativeErrorMessage(error);
  try {
    const parsed = JSON.parse(message) as Partial<ToolFailure>;
    if (typeof parsed.code === "string") return { code: parsed.code, message: String(parsed.message ?? ""), details: parsed.details };
  } catch {
    // plain-text failure
  }
  return { code: "", message };
}

export async function browserUseScenario(config: SmokeConfig, log: StepLog) {
  const use = config.browser?.use;
  if (!use) throw new Error("browser-use fixture pages missing from the smoke config");
  const nonce = config.nonce;
  const store = useBrowserWorkspaceStore();
  const budget = Math.min(config.timeoutMs, 15_000);

  const ai = <T>(toolName: string, args: Record<string, unknown> = {}) =>
    invokeBackend<T>("embedded_browser_ai_execute", { payload: { toolName, arguments: args } });
  const until = async <T>(read: () => Promise<T>, matches: (value: T) => boolean, what: string): Promise<T> => {
    const deadline = Date.now() + budget;
    let last: unknown;
    do {
      try {
        const value = await read();
        if (matches(value)) return value;
        last = value;
      } catch (error) {
        last = nativeErrorMessage(error);
      }
      await delay(150);
    } while (Date.now() < deadline);
    throw new Error(`browser-use smoke did not observe ${what} (last: ${JSON.stringify(last)?.slice(0, 400)})`);
  };
  const untilFound = async <T>(read: () => Promise<T | undefined>, what: string) => (await until(read, Boolean, what)) as T;
  const expectCode = async (operation: () => Promise<unknown>, code: string, what: string) => {
    let failure: ToolFailure | undefined;
    try {
      await operation();
    } catch (error) {
      failure = toolFailure(error);
    }
    if (failure?.code !== code) throw new Error(`${what}: expected error code ${code}, got ${failure ? JSON.stringify(failure) : "success"}`);
    return failure;
  };
  const snapshot = (tabId: string) => ai<PageSnapshot>("browser.get_page_snapshot", { tabId });
  const find = (page: PageSnapshot, what: string, predicate: (element: SnapshotElement) => boolean) => {
    const element = page.elements.find(predicate);
    if (!element) throw new Error(`snapshot of ${page.url} has no ${what}: ${JSON.stringify(page.elements.map((item) => [item.tag, item.name])).slice(0, 600)}`);
    return element;
  };
  const named = (tag: string, name: string) => (element: SnapshotElement) => element.tag === tag && (element.name ?? "").includes(name);
  const waitText = (tabId: string, text: string) => ai("browser.wait_for", { tabId, text, timeoutMs: budget });
  const click = (tabId: string, page: PageSnapshot, element: SnapshotElement) =>
    ai<Record<string, unknown>>("browser.click", { tabId, snapshotId: page.snapshotId, ref: element.ref });
  const navigate = (tabId: string, url: string) => ai("browser.navigate", { tabId, url, timeoutMs: budget });

  const popups: Array<{ tabId: string; openerTabId: string; url: string }> = [];
  const dialogs: Array<{ id: string; tabId: string; kind: string; message: string }> = [];
  const stopPopups = listenAppEvent<{ tabId: string; openerTabId: string; url: string }>("browser:popup-opened", (event) => popups.push(event));
  const stopDialogs = listenAppEvent<{ id: string; tabId: string; kind: string; message: string }>("browser:dialog-opened", (event) => dialogs.push(event));
  try {
    // 1. AI opens the opener page; an AI-opened tab is shared and becomes the AI target.
    const opened = await ai<{ tabId: string; url: string }>("browser.open_tab", { url: use.openerUrl });
    const tab = opened.tabId;
    if (!tab) throw new Error("browser.open_tab returned no tabId");
    await waitText(tab, `Opener ${nonce}`);
    log.done("browser.open_tab routed to the browser workspace and shared the new tab");

    // 2. target=_blank rel=opener popup, admitted by the AI click; it posts back through window.opener.
    let page = await snapshot(tab);
    await click(tab, page, find(page, "popup link", named("a", "Open popup window")));
    const popup = await untilFound(async () => popups.find((event) => event.openerTabId === tab), "browser:popup-opened for the opener tab");
    await waitText(tab, `popup:opener-${nonce}`);
    await waitText(popup.tabId, "opener:present");
    log.done(`_blank popup opened as tab ${popup.tabId} via browser.click and messaged back through window.opener`);

    // 3. Only shared tabs are listed: a tab the user opens stays unshared.
    store.requestUserOpenTab(config.browser!.firstUrl);
    const listed = await until(
      () => ai<{ tabs: Array<{ tabId: string; url: string; openerTabId?: string }>; unsharedCount: number }>("browser.list_tabs"),
      (list) => list.unsharedCount >= 1 && list.tabs.length === 2,
      "list_tabs with the two shared tabs and one unshared tab",
    );
    const ids = listed.tabs.map((item) => item.tabId).sort();
    if (JSON.stringify(ids) !== JSON.stringify([tab, popup.tabId].sort())) throw new Error(`list_tabs listed ${JSON.stringify(listed.tabs)}`);
    if (listed.tabs.some((item) => item.url.includes("/first"))) throw new Error("list_tabs leaked the unshared tab");
    if (listed.tabs.find((item) => item.tabId === popup.tabId)?.openerTabId !== tab) throw new Error("list_tabs lost the popup's opener");
    await ai("browser.switch_tab", { tabId: tab });
    if ((await ai<{ url: string }>("browser.get_url")).url !== use.openerUrl) throw new Error("switch_tab did not make the opener the AI target");
    log.done(`list_tabs listed only the 2 shared tabs (${listed.unsharedCount} unshared counted, not shown); switch_tab retargeted AI`);

    // 4. confirm answered by browser.handle_dialog; tools needing page JS are gated meanwhile.
    //    The click itself returns dialog_pending as soon as the page asks (actionDelivered: the
    //    click reached the page), well inside the 3 s eval timeout.
    const answerDialog = async (buttonName: string, kind: string, answer: Record<string, unknown>, expected: string) => {
      const before = dialogs.length;
      const current = await snapshot(tab);
      const button = find(current, `${buttonName} button`, named("button", buttonName));
      const started = Date.now();
      const clicked = await expectCode(() => click(tab, current, button), "dialog_pending", `browser.click on ${buttonName}`);
      const elapsed = Date.now() - started;
      if (elapsed >= 2_000) throw new Error(`browser.click on ${buttonName} took ${elapsed} ms to report the ${kind}; it must not wait for the eval timeout`);
      if (clicked.details?.actionDelivered !== true) throw new Error(`dialog_pending from the click lacks actionDelivered: ${JSON.stringify(clicked)}`);
      const dialog = await untilFound(async () => dialogs.slice(before).find((event) => event.tabId === tab), `browser:dialog-opened (${kind})`);
      if (dialog.kind !== kind || !dialog.message.includes(nonce)) throw new Error(`unexpected dialog ${JSON.stringify(dialog)}`);
      if ((clicked.details?.dialog as { id?: string } | undefined)?.id !== dialog.id) throw new Error("the click's dialog_pending does not describe the open dialog");
      const gated = await expectCode(() => snapshot(tab), "dialog_pending", `snapshot during the ${kind}`);
      if ((gated.details?.dialog as { id?: string } | undefined)?.id !== dialog.id) throw new Error("dialog_pending does not describe the open dialog");
      await ai("browser.handle_dialog", { tabId: tab, dialogId: dialog.id, ...answer });
      await waitText(tab, expected);
      await expectCode(() => ai("browser.handle_dialog", { tabId: tab, dialogId: dialog.id, accept: true }), "dialog_already_handled", `second answer to the ${kind}`);
      return `click returned dialog_pending with actionDelivered in ${elapsed} ms`;
    };
    const confirmClick = await answerDialog("Ask confirm", "confirm", { accept: true }, "confirm:true");
    log.done(`confirm answered by handle_dialog → page saw true; snapshot gated with dialog_pending; second answer dialog_already_handled (${confirmClick})`);
    const promptClick = await answerDialog("Ask prompt", "prompt", { accept: true, promptText: `ai-${nonce}` }, `prompt:ai-${nonce}`);
    log.done(`prompt text from handle_dialog reached the page (${promptClick})`);

    // 5. Same-origin iframe, open shadow root, select, checkbox, hover menu, scrolling.
    await navigate(tab, use.formsUrl);
    page = await snapshot(tab);
    const frameButton = find(page, "iframe button", (element) => element.frame === "0" && named("button", `Frame button ${nonce}`)(element));
    await click(tab, page, frameButton);
    await waitText(tab, `frame:clicked-${nonce}`);
    log.done("click on a button inside a same-origin iframe (frame 0) took effect");
    const shadowInput = find(page, "shadow input", (element) => element.shadow === true && element.tag === "input" && element.name === "Shadow field");
    await ai("browser.fill", { tabId: tab, snapshotId: page.snapshotId, ref: shadowInput.ref, value: `shadow-${nonce}` });
    await waitText(tab, `shadow:shadow-${nonce}`);
    log.done("fill reached an input inside an open shadow root and fired its input event");
    const select = find(page, "select", named("select", "Fixture choice"));
    if (!select.options?.some((option) => option.value === "beta")) throw new Error("snapshot does not list the select options");
    await ai("browser.select_option", { tabId: tab, snapshotId: page.snapshotId, ref: select.ref, values: ["beta"] });
    await waitText(tab, "select:beta");
    const checkbox = find(page, "checkbox", named("input", "Fixture agree"));
    await ai("browser.set_checked", { tabId: tab, snapshotId: page.snapshotId, ref: checkbox.ref, checked: true });
    await waitText(tab, "check:true");
    log.done("select_option chose beta and set_checked ticked the checkbox (page change events fired)");
    if (page.elements.some(named("a", `Menu item ${nonce}`))) throw new Error("hover menu item visible before hovering");
    await ai("browser.hover", { tabId: tab, snapshotId: page.snapshotId, ref: find(page, "menu trigger", named("button", "Fixture menu")).ref });
    const hovered = await until(() => snapshot(tab), (value) => value.elements.some(named("a", `Menu item ${nonce}`)), "the hover menu item in a fresh snapshot");
    log.done("hover opened the menu: its item appears in the next snapshot");
    const bottomBefore = find(hovered, "bottom marker", named("button", `Bottom marker ${nonce}`));
    if (bottomBefore.inViewport !== false) throw new Error("bottom marker already in the viewport before scrolling");
    await ai("browser.scroll", { tabId: tab, direction: "down", amount: 100000 });
    await until(() => snapshot(tab), (value) => value.elements.some((element) => named("button", `Bottom marker ${nonce}`)(element) && element.inViewport === true), "the bottom marker inViewport after scrolling");
    log.done("scroll down reached the bottom: the bottom marker became inViewport");

    // 6. Readable content, then history back and forward.
    await navigate(tab, use.articleUrl);
    const extracted = await ai<{ title: string; content: string }>("browser.extract_content", { tabId: tab });
    if (!extracted.content.includes(`Fixture Headline ${nonce}`) || extracted.content.includes("Fixture footer")) {
      throw new Error(`extract_content missed the article or kept the footer: ${extracted.content.slice(0, 300)}`);
    }
    log.done("extract_content returned the article with its fixture headline and without the footer");
    await ai("browser.go_back", { tabId: tab, timeoutMs: budget });
    if ((await ai<{ url: string }>("browser.get_url", { tabId: tab })).url !== use.formsUrl) throw new Error("go_back did not return to the forms page");
    await ai("browser.go_forward", { tabId: tab, timeoutMs: budget });
    if ((await ai<{ url: string }>("browser.get_url", { tabId: tab })).url !== use.articleUrl) throw new Error("go_forward did not return to the article");
    log.done("go_back and go_forward moved between the forms page and the article");

    // 7. Upload: the AI click opens the chooser; the e2e hook picks the fixture file.
    await navigate(tab, use.filesUrl);
    page = await snapshot(tab);
    const upload = find(page, "file input", (element) => element.tag === "input" && (element.name ?? "").includes("Fixture upload"));
    if (upload.selectedCount !== 0) throw new Error("file input already has a file");
    const chosen = await click(tab, page, upload);
    if (chosen.fileChooser !== "opened") throw new Error(`file input click did not open the chooser: ${JSON.stringify(chosen)}`);
    await until(() => snapshot(tab), (value) => value.elements.some((element) => element.ref === upload.ref && element.selectedCount === 1), "selectedCount 1 on the file input");
    log.done("click on the file input opened the native chooser; after the pick the snapshot reports selectedCount 1");

    // 8. Download: answered 1.5 s after the click, still authorized by that click; the e2e hook confirms the save panel.
    page = await snapshot(tab);
    await click(tab, page, find(page, "download link", named("a", "Download fixture file")));
    const records = await until(
      () => ai<DownloadRecord[]>("browser.get_downloads", { tabId: tab }),
      (list) => list.some((record) => record.filename === use.downloadName && (record.state === "completed" || record.state === "failed" || record.state === "cancelled")),
      "the fixture download to finish",
    );
    const record = records.find((item) => item.filename === use.downloadName)!;
    if (record.state !== "completed" || record.bytes <= 0) throw new Error(`download did not complete: ${JSON.stringify(record)}`);
    if (Object.keys(record).some((key) => /path/i.test(key))) throw new Error("download record exposes a path");
    log.done(`delayed attachment downloaded via the save panel (${record.bytes} bytes, record carries the file name only)`);

    // 9. Unsharing takes access away.
    store.unshareAll();
    await until(() => ai("browser.get_url", { tabId: tab }).then(() => "shared", (error: unknown) => toolFailure(error).code), (code) => code === "browser_tab_not_shared", "browser_tab_not_shared after unsharing");
    log.done("unsharing every tab made the next tool call fail with browser_tab_not_shared");
  } finally {
    stopPopups();
    stopDialogs();
    store.unshareAll();
  }
}
