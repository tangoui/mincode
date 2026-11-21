// Opt-in native performance probe for the remaining pages (-tags e2e builds only):
//  - SSH workspace with 500 saved connections (sidebar list and the server overview),
//  - the Settings dialog, every category in turn,
//  - the embedded browser with several tabs (harness-served pages), switching between them.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import type { SshConnectionProfile } from "@/domain/ssh/types";
import { requestOpenSettings } from "@/services/settings-navigation";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useNotificationsStore } from "@/stores/notifications";
import { useWorkbenchStore } from "@/stores/workbench";
import { createMeasure, nextFrame, startupTimings, until, type PerfLog } from "@/services/e2e-perf-kit";

function visible<T extends HTMLElement>(selector: string, root: ParentNode = document) {
  return [...root.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}
const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

async function scrollThrough(element: HTMLElement, steps = 40) {
  const span = element.scrollHeight - element.clientHeight;
  for (let i = 0; i <= steps; i++) { element.scrollTop = (span * i) / steps; await nextFrame(); }
  for (let i = steps; i >= 0; i--) { element.scrollTop = (span * i) / steps; await nextFrame(); }
}

export async function perfPagesScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfPages!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  log.done(`PERF-NOTE 启动 ${JSON.stringify(startupTimings())}`);

  // ---- SSH: many saved connections in a few groups.
  const ssh = useSshConnectionsStore();
  await ssh.hydrate();
  const ids: string[] = [];
  for (let i = 0; i < fixture.sshProfiles; i++) {
    const profile: SshConnectionProfile = {
      id: `perf-${config.nonce}-${i}`, name: `server-${String(i).padStart(3, "0")}`, host: `10.0.${Math.floor(i / 250)}.${i % 250}`, port: 22,
      username: "deploy", authType: "password", group: `group-${i % 10}`, tags: i % 3 === 0 ? ["prod"] : [], favorite: false,
      saveCredentials: false, portForwards: [], createdAt: Date.now(), updatedAt: Date.now(),
    };
    ssh.saveProfile(profile);
    ids.push(profile.id);
  }
  // Users load saved connections at startup; let the bulk save persist before measuring.
  await ssh.flushPersistence();
  await pause(1000);
  const sshTab = workbench.openView("ssh");
  try {
    await measure(`进入 SSH 页（${fixture.sshProfiles} 个连接、10 个分组）`, async () => {
      await router.push({ name: "ssh" });
      await until(() => visible(".db-connection-entry") && visible(".connection-overview__row"), "SSH rows", wait * 2);
    });
    // Second entry: the view is cached, so this is the cost of showing it, not of setting it up.
    await measure("切走再回到 SSH 页", async () => {
      await router.push({ name: "dashboard" });
      await nextFrame();
      await router.push({ name: "ssh" });
      await until(() => document.querySelectorAll(".db-connection-entry").length >= Math.min(fixture.sshProfiles, 10), "SSH rows again", wait);
    });
    log.done(`PERF ${JSON.stringify({ step: "SSH 页渲染规模", sidebarRows: document.querySelectorAll(".db-connection-entry").length, overviewRows: document.querySelectorAll(".connection-overview__row").length, dropdowns: document.querySelectorAll(".db-connection-node .el-dropdown").length })}`);
    await measure("SSH 侧边栏连接列表滚动", async () => {
      const element = await until(() => visible<HTMLElement>(".db-tree-scroll .el-scrollbar__wrap"), "sidebar scroller", wait);
      await scrollThrough(element);
    });
    await measure("SSH 服务器总览滚动", async () => {
      const row = await until(() => visible<HTMLElement>(".connection-overview__row"), "overview row", wait);
      const element = row.closest<HTMLElement>(".el-scrollbar__wrap");
      if (!element) throw new Error("overview scroller not found");
      await scrollThrough(element);
    });
    await measure("SSH 页搜索输入 10 个字符", async () => {
      const input = await until(() => visible<HTMLInputElement>(".db-search-box input"), "search input", wait);
      for (const ch of "server-12x") {
        input.value += ch;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        await nextFrame();
      }
      input.value = "";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await nextFrame();
    });
  } finally {
    if (sshTab) workbench.closeTab(sshTab.id, true);
    for (const id of ids) ssh.removeProfile(id);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }

  // ---- Settings: open, visit every category, close.
  await measure("打开设置", async () => {
    requestOpenSettings();
    await until(() => visible(".settings-dialog__nav-item"), "settings navigation", wait);
  });
  // Each category on its own: the first visit mounts its panel.
  for (const [index, item] of [...document.querySelectorAll<HTMLElement>(".settings-dialog__nav-item")].entries()) {
    const label = item.textContent?.trim() || String(index);
    await measure(`设置分类「${label}」首次打开`, async () => {
      item.click();
      await nextFrame();
      await nextFrame();
    });
  }
  await measure("所有设置分类再切换一轮", async () => {
    for (const item of [...document.querySelectorAll<HTMLElement>(".settings-dialog__nav-item")]) {
      item.click();
      await nextFrame();
      await nextFrame();
    }
  });
  await measure("关闭设置", async () => {
    visible<HTMLElement>(".settings-dialog__close")?.click();
    await until(() => !visible(".settings-dialog__nav-item"), "settings closed", wait);
  });

  // ---- Notification center: a burst of notifications (capped at 200), then the drawer open while more arrive.
  const notifications = useNotificationsStore();
  const notice = (i: number) => ({ source: "plugin" as const, level: (["info", "success", "warning", "error"] as const)[i % 4], title: `Perf notice ${i}`, message: `Body of notification ${i} with some explanatory text.` });
  try {
    await measure("连续推送 200 条通知", async () => {
      for (let i = 0; i < 200; i++) { notifications.push(notice(i)); if (i % 10 === 9) await nextFrame(); }
      await nextFrame();
    });
    await measure("打开通知中心（200 条）", async () => {
      // The bell button carries the unread badge once anything is unread.
      (await until(() => document.querySelector<HTMLElement>(".app-notification-button__badge")?.closest("button") ?? undefined, "notification button", wait)).click();
      await until(() => document.querySelectorAll(".notification-list__item").length >= 200, "notification list", wait);
      await nextFrame();
    });
    await measure("通知中心打开时每帧来一条，共 60 条", async () => {
      for (let i = 200; i < 260; i++) { notifications.push(notice(i)); await nextFrame(); }
    });
    await measure("通知列表滚动到底再回顶部", async () => {
      const wrap = await until(() => visible<HTMLElement>(".notification-drawer__scroll .el-scrollbar__wrap"), "notification scroller", wait);
      for (let i = 0; i <= 30; i++) { wrap.scrollTop = (wrap.scrollHeight * i) / 30; await nextFrame(); }
      for (let i = 30; i >= 0; i--) { wrap.scrollTop = (wrap.scrollHeight * i) / 30; await nextFrame(); }
    });
    await measure("关闭通知中心", async () => {
      document.querySelector<HTMLElement>(".notification-drawer .el-drawer__close-btn")?.click();
      await until(() => !visible(".notification-list__item"), "notification drawer closed", wait);
    });
  } finally {
    notifications.markAllRead();
    notifications.clearRead();
  }

  // ---- Embedded browser: several tabs of harness pages, switch between them.
  const browserTab = workbench.openView("browser");
  try {
    await measure(`内置浏览器打开 ${fixture.browserUrls.length} 个标签`, async () => {
      await router.push({ name: "browser" });
      await until(() => visible(".browser-tabbar"), "browser tab bar", wait);
      // The way a user does it: "+" opens a tab, the address bar navigates it.
      for (const [index, url] of fixture.browserUrls.entries()) {
        if (index > 0) {
          const before = document.querySelectorAll(".browser-tabbar__tab").length;
          (await until(() => visible<HTMLElement>(".browser-tabbar__add"), "new tab button", wait)).click();
          await until(() => document.querySelectorAll(".browser-tabbar__tab").length > before, "new browser tab", wait);
        }
        const input = await until(() => visible<HTMLInputElement>("[data-browser-address] input, input[data-browser-address]"), "address bar", wait);
        input.value = url;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        await nextFrame();
        // Enter in the address bar opens the *system* browser; the embedded view has its own button.
        (await until(() => visible<HTMLButtonElement>(".browser-toolbar__preview"), "open embedded button", wait)).click();
        // Tab titles show the host; wait until this tab stops loading.
        await until(() => document.querySelectorAll(".browser-tabbar__tab").length === index + 1 && !document.querySelector(".browser-tabbar__tab.is-active .browser-tabbar__loading"), `page ${index} loaded`, wait * 2);
      }
      await until(() => ![...document.querySelectorAll(".browser-tabbar__loading")].length, "pages loaded", wait * 2).catch(() => undefined);
    });
    await measure("浏览器标签来回切换 3 轮", async () => {
      for (let round = 0; round < 3; round++) {
        for (const button of [...document.querySelectorAll<HTMLElement>(".browser-tabbar__select")]) {
          button.click();
          await nextFrame();
          await nextFrame();
        }
      }
    });
    await measure("浏览器页空闲 5 秒", () => pause(5000));
  } finally {
    for (const close of [...document.querySelectorAll<HTMLElement>(".browser-tabbar__close")]) close.click();
    if (browserTab) workbench.closeTab(browserTab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
}
