// Lazy native smoke scenario; only the disposable package includes a UI driver.
import router from '@/router';
import { openPluginViewTab } from '@/domain/workspace/open-plugin-view';
import { useWorkbenchStore } from '@/stores/workbench';
import { beginPluginUISession, invokePluginUI, closePluginUISession, installLocalPlugin, invokePlugin, setPluginEnabled, uninstallPlugin } from '@/services/plugin-service';
import type { SmokeConfig } from '@/domain/e2e/desktop-smoke';

export async function formatterScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.formatter!;
  const pluginId = 'wechat-formatter';
  await installLocalPlugin(fixture.packagePath);
  await setPluginEnabled(pluginId, true);
  const workbench = useWorkbenchStore();
  let stop = () => {};
  let tabId = '';
  try {
    const session = await beginPluginUISession(pluginId, 'main');
    try {
      const imported = await invokePluginUI(session.sessionId, session.nonce, 'importAsset', JSON.stringify({ path: fixture.imagePath })) as { asset: { id: string } };
      await invokePluginUI(session.sessionId, session.nonce, 'save', JSON.stringify({ title: 'Formatter smoke', markdown: '# Smoke\n\n![Local](wxf-asset:' + imported.asset.id + ')\n\n[Example](https://example.com)\n\nparagraph' }));
      log.done('host-authorized import and article save through real plugin process');
    } finally { await closePluginUISession(session.sessionId); }
    const interaction = new Promise<void>((resolve, reject) => {
      const listener = (event: MessageEvent<unknown>) => {
        const frame = document.querySelector<HTMLIFrameElement>('iframe.plugin-view-frame__iframe');
        if (!frame || event.source !== frame.contentWindow || !event.data || typeof event.data !== 'object') return;
        const data = event.data as { type?: string; kind?: string; value?: unknown };
        if (data.type !== 'mincode.e2e.formatter') return;
        if (data.kind === 'step') log.done(String(data.value));
        if (data.kind === 'failed') reject(Error(String(data.value)));
        if (data.kind === 'done') resolve();
      };
      window.addEventListener('message', listener);
      stop = () => window.removeEventListener('message', listener);
    });
    const tab = openPluginViewTab(workbench, router, pluginId, 'main', 'Formatter smoke');
    if (!tab) throw Error('Could not open formatter tab');
    tabId = tab.id;
    await interaction;
    workbench.closeTab(tab.id, true); tabId = '';
    await setPluginEnabled(pluginId, false);
    await setPluginEnabled(pluginId, true);
    const read = await beginPluginUISession(pluginId, 'main');
    try {
      const assets = await invokePluginUI(read.sessionId, read.nonce, 'assets', '{}') as { assets: unknown[] };
      if (assets.assets.length !== 1) throw Error('Local asset lost on restart');
      const articles = await invokePlugin(pluginId, 'articles', '{}') as { articles: Array<{ title: string }> };
      if (!articles.articles.some(a => a.title === '技术教程')) throw Error('Template article did not persist');
      log.done('local asset and template article survived actual process restart');
    } finally { await closePluginUISession(read.sessionId); }
  } finally {
    stop(); if (tabId) workbench.closeTab(tabId, true);
    await uninstallPlugin(pluginId, true);
  }
}
