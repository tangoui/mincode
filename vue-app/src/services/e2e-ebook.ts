// Real WebKit smoke, lazy-loaded only behind the native -tags e2e gate.
// The instrumented driver exists only in the disposable test package.
import router from '@/router';
import { openPluginViewTab } from '@/domain/workspace/open-plugin-view';
import { useWorkbenchStore } from '@/stores/workbench';
import { flushPluginView } from '@/services/plugin-view-save';
import { beginPluginUISession, callPluginUIMethod, closePluginUISession, installLocalPlugin, invokePlugin, setPluginEnabled, uninstallPlugin } from '@/services/plugin-service';
import type { SmokeConfig } from '@/domain/e2e/desktop-smoke';

export async function ebookScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.ebook!;
  const pluginId = 'ebook-reader';
  await installLocalPlugin(fixture.packagePath);
  await setPluginEnabled(pluginId, true);
  const imported = await invokePlugin(pluginId, 'library.import', JSON.stringify({ paths: fixture.paths })) as { added: Array<{ id: string; format: string }> };
  if (imported.added.length !== fixture.paths.length) throw Error(`imported ${imported.added.length} of ${fixture.paths.length} book formats`);
  const textId = imported.added.find(book => book.format === 'txt')!.id;
  log.done('installed real package and imported CHM/EPUB/TXT through native plugin process');
  const workbench = useWorkbenchStore();
  let stop: () => void = () => {};
  const interaction = new Promise<{ steps: string[]; minBlock: number }>((resolve, reject) => {
    const listener = (event: MessageEvent<unknown>) => {
      const frame = document.querySelector<HTMLIFrameElement>('iframe.plugin-view-frame__iframe');
      if (!frame || event.source !== frame.contentWindow || !event.data || typeof event.data !== 'object') return;
      const data = event.data as { type?: string; kind?: string; value?: unknown };
      if (data.type !== 'mincode.e2e.ebook') return;
      if (data.kind === 'step') log.done(String(data.value));
      if (data.kind === 'failed') reject(Error(String(data.value)));
      if (data.kind === 'ready-to-close') resolve(data.value as { steps: string[]; minBlock: number });
    };
    window.addEventListener('message', listener);
    stop = () => window.removeEventListener('message', listener);
  });
  try {
    const tab = openPluginViewTab(workbench, router, pluginId, 'main', 'Ebook smoke');
    if (!tab) throw Error('could not open ebook tab');
    const result = await interaction;
    await flushPluginView(tab.id);
    // Read the stored state through another real, host-validated session.
    const session = await beginPluginUISession(pluginId, 'main');
    try {
      const opened = await callPluginUIMethod(session.sessionId, session.nonce, 'book/open', { id: textId }) as { state: { position: { block: number }; bookmarks: unknown[] } };
      if (opened.state.position.block < result.minBlock || opened.state.bookmarks.length !== 1) throw Error('save handshake failed to persist current position/bookmark');
      log.done(`awaited final live-bridge save; stored paragraph ${opened.state.position.block} and bookmark verified`);
      workbench.closeTab(tab.id, true);
      await setPluginEnabled(pluginId, false);
      await setPluginEnabled(pluginId, true);
      log.done('closed real tab and restarted plugin process without deleting source files');
    } finally { await closePluginUISession(session.sessionId); }
    const reopened = await beginPluginUISession(pluginId, 'main');
    try {
      const opened = await callPluginUIMethod(reopened.sessionId, reopened.nonce, 'book/open', { id: textId }) as { state: { position: { block: number }; bookmarks: unknown[] } };
      if (opened.state.position.block < result.minBlock || opened.state.bookmarks.length !== 1) throw Error('reading state lost after plugin restart');
      log.done('position and bookmark survived actual process restart');
    } finally { await closePluginUISession(reopened.sessionId); }
  } finally { stop(); await uninstallPlugin(pluginId, true); }
}
