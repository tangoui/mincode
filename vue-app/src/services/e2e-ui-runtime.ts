// Opt-in real WebView test of the plugin page CSP (host 0.3.0): every
// page may use blob: workers and play blob: media, only ui.wasm pages may
// compile WebAssembly, and nothing may fetch blob: or remote URLs. The probe
// page (tools/e2e/plugin-ui-runtime-probe.js) reports what the WebView allowed.
import router from '@/router';
import { openPluginViewTab } from '@/domain/workspace/open-plugin-view';
import { useWorkbenchStore } from '@/stores/workbench';
import { installLocalPlugin, setPluginEnabled, uninstallPlugin } from '@/services/plugin-service';
import type { SmokeConfig } from '@/domain/e2e/desktop-smoke';

type Results = Record<string, string>;

const allowed = (value: string | undefined) => value?.startsWith('ok:') === true;

// What the host CSP must allow ("ok") or block for each probe.
function expectations(wasm: boolean): Record<string, boolean> {
  return {
    blobWorker: true,
    assetWorker: false,
    workerFetchRemote: false,
    workerWasm: wasm,
    audioBlob: true,
    wasm,
    fetchBlob: false,
    fetchRemote: false,
  };
}

export async function uiRuntimeScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const workbench = useWorkbenchStore();
  const frame = () => document.querySelector<HTMLIFrameElement>('iframe.plugin-view-frame__iframe');
  for (const [label, fixture] of [['ui', config.uiRuntime!.plain], ['ui + ui.wasm', config.uiRuntime!.wasm]] as const) {
    let tabId = '';
    let listener: ((event: MessageEvent<unknown>) => void) | undefined;
    try {
      const reported = new Promise<Results>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`${label}: probe page did not report within 30 s`)), 30000);
        listener = (event: MessageEvent<unknown>) => {
          if (!frame() || event.source !== frame()!.contentWindow) return;
          const data = event.data as { type?: string; results?: Results } | null;
          if (data?.type !== 'mincode.e2e.ui-runtime' || !data.results) return;
          clearTimeout(timer);
          resolve(data.results);
        };
        window.addEventListener('message', listener);
      });
      await installLocalPlugin(fixture.packagePath);
      await setPluginEnabled(fixture.pluginId, true);
      const tab = openPluginViewTab(workbench, router, fixture.pluginId, 'main', `UI runtime (${label})`);
      if (!tab) throw new Error(`${label}: could not open the plugin view`);
      tabId = tab.id;
      const results = await reported;
      const wrong = Object.entries(expectations(fixture === config.uiRuntime!.wasm))
        .filter(([probe, ok]) => allowed(results[probe]) !== ok)
        .map(([probe, ok]) => `${probe} expected ${ok ? 'allowed' : 'blocked'}, got ${results[probe] ?? 'nothing'}`);
      if (wrong.length) throw new Error(`${label}: ${wrong.join('; ')} | ${JSON.stringify(results)}`);
      log.done(`${label}: ${Object.entries(results).map(([probe, value]) => `${probe}=${value}`).join(', ')}`);
    } finally {
      if (listener) window.removeEventListener('message', listener);
      if (tabId) workbench.closeTab(tabId, true);
      await setPluginEnabled(fixture.pluginId, false).catch(() => undefined);
      await uninstallPlugin(fixture.pluginId, true).catch(() => undefined);
    }
  }
}
