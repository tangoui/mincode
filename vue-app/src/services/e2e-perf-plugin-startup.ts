// Opt-in native performance probe for plugin startup (-tags e2e builds only).
// Setup launch: install the harness plugins (official file-icons plus synthetic process plugins, a few
// with "startup" activation) and enable the chosen ones. Cold launch: measure how long the first
// plugin data takes, the frames while startup plugins come up, and the plugin events that cause
// refetches in the frontend.
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { fetchPluginContributions, installLocalPlugin, setPluginEnabled } from "@/services/plugin-service";
import { listenAppEvent } from "@/services/desktop-bridge";
import { createMeasure, round, startupTimings, type PerfLog } from "@/services/e2e-perf-kit";

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export async function perfPluginStartupScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfPluginStartup!;
  const measure = createMeasure(log);
  const enabled = fixture.packages.filter((item) => item.enable).length;

  if (fixture.phase === "setup") {
    for (const item of fixture.packages) await installLocalPlugin(item.packagePath);
    for (const item of fixture.packages) if (item.enable) await setPluginEnabled(item.pluginId, true);
    log.done(`installed ${fixture.packages.length} plugins, enabled ${enabled}`);
    return;
  }

  // Cold launch: everything below runs as early as the smoke runner starts.
  const startedAt = performance.now();
  log.done(`PERF-NOTE 冷启动（已装 ${fixture.packages.length} 个插件、启用 ${enabled} 个） ${JSON.stringify(startupTimings())}`);
  const events: Record<string, number[]> = {};
  const stops = ["plugin:contributions-changed", "plugin:state-changed", "plugin:startup-completed", "plugin:workspace-changed"].map((name) =>
    listenAppEvent(name, () => { (events[name] ??= []).push(round(performance.now())); }),
  );
  try {
    await measure(`冷启动后首次取到插件贡献数据（已装 ${fixture.packages.length} 个）`, async () => {
      await fetchPluginContributions(["fileIcons", "commands"]);
    });
    // Startup activation begins 1.5 s after the first plugin call and spawns two plugins at a time.
    await measure(`冷启动后到第 10 秒（启用 ${enabled} 个，其中启动型插件陆续拉起）`, () => pause(Math.max(0, 10_000 - (performance.now() - startedAt))));
    log.done(`PERF-NOTE 插件事件 ${JSON.stringify(Object.fromEntries(Object.entries(events).map(([name, times]) => [name, { count: times.length, first: times[0], last: times[times.length - 1] }])))}`);
  } finally {
    for (const stop of stops) stop();
  }
}
