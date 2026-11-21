// Opt-in native performance probe for plugins (-tags e2e builds only).
// Installs many harness-generated plugins, enables the few that have a view, then measures the
// plugin manager and switching between plugin views (iframes) and native pages. Each view page
// posts a message when it loads, so the probe also reports how often a tab switch reloads a view.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import type { WorkspaceTab } from "@/types/workspace";
import { installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { useWorkbenchStore } from "@/stores/workbench";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";

const settle = async () => { await nextFrame(); await nextFrame(); };

export async function perfPluginsScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfPlugins!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const loads = new Map<string, number>();
  const onMessage = (event: MessageEvent<unknown>) => {
    const data = event.data as { type?: string; id?: string } | null;
    if (data?.type === "mincode.e2e.perf-plugin" && typeof data.id === "string") loads.set(data.id, (loads.get(data.id) ?? 0) + 1);
  };
  window.addEventListener("message", onMessage);
  const viewPlugins = fixture.packages.filter((item) => item.view);
  const tabs: WorkspaceTab[] = [];
  const installed: string[] = [];
  try {
    await measure(`安装 ${fixture.packages.length} 个插件并启用 ${viewPlugins.length} 个`, async () => {
      for (const item of fixture.packages) {
        await installLocalPlugin(item.packagePath);
        installed.push(item.pluginId);
      }
      for (const item of viewPlugins) await setPluginEnabled(item.pluginId, true);
    });

    const cards = () => document.querySelectorAll(".plugin-manager-card").length;
    await measure(`进入插件管理页（${fixture.packages.length} 个插件）`, async () => {
      await router.push({ name: "plugins" });
      await until(() => cards() > 0, "plugin cards", wait);
    });
    await measure("插件管理页翻页", async () => {
      for (const label of ["2", "1", "2", "1"]) {
        const pager = [...document.querySelectorAll<HTMLElement>(".app-plugin-pagination .el-pager li")].find((item) => item.textContent?.trim() === label);
        pager?.click();
        await settle();
      }
    });

    await measure(`依次打开 ${viewPlugins.length} 个插件页`, async () => {
      for (const [index, item] of viewPlugins.entries()) {
        const tab = openPluginViewTab(workbench, router, item.pluginId, "main", `Perf view ${index}`);
        if (!tab) throw new Error(`could not open ${item.pluginId}`);
        tabs.push(tab);
        await until(() => (loads.get(item.pluginId) ?? 0) > 0, `${item.pluginId} loaded`, wait);
      }
    });
    const loadsBefore = [...loads.values()].reduce((sum, count) => sum + count, 0);

    await measure(`在 ${tabs.length} 个插件页间切换 3 轮`, async () => {
      for (let round = 0; round < 3; round++) {
        for (const tab of tabs) { await router.push(workspaceTabRoute(tab)); await settle(); }
      }
    });
    await measure("插件页与本地项目、数据库来回切换 5 轮", async () => {
      for (let round = 0; round < 5; round++) {
        await router.push(workspaceTabRoute(tabs[round % tabs.length]!)); await settle();
        await router.push(workspaceTabRoute(tabs[(round + 1) % tabs.length]!)); await settle();
        await router.push({ name: "database" }); await settle();
      }
    });
    const reloads = [...loads.values()].reduce((sum, count) => sum + count, 0) - loadsBefore;
    log.done(`PERF-NOTE 切换期间插件页重新加载 ${reloads} 次`);

    await measure("空闲 5 秒（插件页已打开）", () => new Promise((resolve) => setTimeout(resolve, 5000)));
    await measure(`关闭 ${tabs.length} 个插件页`, async () => {
      for (const tab of tabs.splice(0)) { workbench.closeTab(tab.id, true); await nextFrame(); }
      await router.push({ name: "dashboard" }).catch(() => undefined);
      await settle();
    });
  } finally {
    window.removeEventListener("message", onMessage);
    for (const tab of tabs) workbench.closeTab(tab.id, true);
    for (const pluginId of installed) {
      await setPluginEnabled(pluginId, false).catch(() => undefined);
      await uninstallPlugin(pluginId, true).catch(() => undefined);
    }
  }
  await measure("卸载插件后空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
}
