// Loaded only by the native -tags e2e smoke runner. Installs the packaged Port
// Manager plugin and drives it against a real TCP server the harness started:
// lookup and list find it, kill through a UI session stops it, and the page opens.
import router from "@/router";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { useWorkbenchStore } from "@/stores/workbench";
import {
  beginPluginUISession, closePluginUISession, installLocalPlugin, invokePlugin, invokePluginUI, listPlugins, previewLocalPlugin, setPluginEnabled, uninstallPlugin,
} from "@/services/plugin-service";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";

const PLUGIN_ID = "port-manager";

interface Holder {
  pid: number;
  port: number;
  protocol: string;
  process?: string;
  own: boolean;
  startedAt?: string;
}

async function until<T>(read: () => T | undefined | false | null | Promise<T | undefined | false | null>, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = await read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Timed out waiting for ${label}`);
}

export async function portManagerScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.portManager!;
  const preview = await previewLocalPlugin(fixture.packagePath);
  if (preview.manifest.id !== PLUGIN_ID) throw new Error(`preview reported ${preview.manifest.id}`);
  await installLocalPlugin(fixture.packagePath);
  const workbench = useWorkbenchStore();
  let tabId = "";
  try {
    await setPluginEnabled(PLUGIN_ID, true);
    log.done("packaged plugin passed preflight, installed and enabled");

    const lookup = async () => (await invokePlugin(PLUGIN_ID, "lookup", JSON.stringify({ port: fixture.port }))) as { free: boolean; listeners: Holder[] };
    const found = await lookup();
    const holder = found.listeners.find((item) => item.pid === fixture.pid);
    if (found.free || !holder || !holder.own || holder.protocol !== "tcp") throw new Error(`lookup ${fixture.port}: ${JSON.stringify(found)}`);
    log.done(`lookup found the harness server on port ${fixture.port}: ${holder.process} (PID ${holder.pid})`);

    const session = await beginPluginUISession(PLUGIN_ID, "main");
    try {
      const listed = (await invokePluginUI(session.sessionId, session.nonce, "list", "{}")) as { listeners: Holder[] };
      if (!listed.listeners.some((item) => item.port === fixture.port && item.pid === fixture.pid)) throw new Error("list does not include the harness server");
      log.done(`list returned ${listed.listeners.length} listeners including the harness server`);
      const killed = (await invokePluginUI(session.sessionId, session.nonce, "kill", JSON.stringify({
        pid: holder.pid, port: holder.port, protocol: holder.protocol, startedAt: holder.startedAt ?? "", force: false,
      }))) as { exited: boolean };
      if (!killed.exited) throw new Error(`kill did not stop the server: ${JSON.stringify(killed)}`);
    } finally {
      closePluginUISession(session.sessionId);
    }
    await until(async () => (await lookup()).free, `port ${fixture.port} to be free`, config.timeoutMs);
    log.done(`kill through a UI session stopped PID ${fixture.pid}; port ${fixture.port} is free`);

    const tab = openPluginViewTab(workbench, router, PLUGIN_ID, "main", "Port Manager");
    if (!tab) throw new Error("Could not open the Port Manager tab");
    tabId = tab.id;
    await until(() => document.querySelector<HTMLIFrameElement>("iframe.plugin-view-frame__iframe"), "Port Manager page frame", config.timeoutMs);
    log.done("Port Manager page opened in a workspace tab");

    // Closing the last view releases the page and, shortly after, the plugin process.
    const state = async () => (await listPlugins()).find((plugin) => plugin.id === PLUGIN_ID)?.state;
    if ((await state()) !== "running") throw new Error(`plugin not running with its page open: ${await state()}`);
    workbench.closeTab(tabId, true);
    tabId = "";
    await until(() => !document.querySelector("iframe.plugin-view-frame__iframe"), "Port Manager page to be released", config.timeoutMs);
    await until(async () => (await state()) === "stopped", "plugin process to stop after its page closed", config.timeoutMs);
    log.done("closing the page released its frame and the plugin process went back to dormant");
  } finally {
    if (tabId) workbench.closeTab(tabId, true);
    await uninstallPlugin(PLUGIN_ID, true).catch(() => undefined);
  }
  if ((await listPlugins()).some((plugin) => plugin.id === PLUGIN_ID)) throw new Error("plugin still listed after uninstall");
  log.done("uninstalled");
}
