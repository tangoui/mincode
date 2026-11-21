// Opt-in real WebKit test for a framework-neutral (no Vue) plugin page.
import router from '@/router';
import { openPluginViewTab } from '@/domain/workspace/open-plugin-view';
import { useWorkbenchStore } from '@/stores/workbench';
import { useVueI18n } from '@/i18n/messages';
import { applyUiTheme, type UiTheme } from '@/services/ui-theme';
import { installLocalPlugin, listPlugins, setPluginEnabled, uninstallPlugin } from '@/services/plugin-service';
import type { SmokeConfig } from '@/domain/e2e/desktop-smoke';

interface PageState { theme?: string; title?: string; background?: string }
const pluginId = 'ts-go-plugin';

export async function tsPluginScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const workbench = useWorkbenchStore();
  const i18n = useVueI18n();
  const initialLocale = i18n.locale.value;
  const initialTheme = (document.documentElement.dataset.theme ?? 'light') as UiTheme;
  let tabId = '';
  const waiters = new Map<string, (state: PageState) => void>();
  let ready!: (state: PageState) => void;
  let failed!: (error: Error) => void;
  const started = new Promise<PageState>((resolve, reject) => { ready = resolve; failed = reject; });
  const frame = () => document.querySelector<HTMLIFrameElement>('iframe.plugin-view-frame__iframe');
  const listener = (event: MessageEvent<unknown>) => {
    if (!frame() || event.source !== frame()!.contentWindow || !event.data || typeof event.data !== 'object') return;
    const data = event.data as { type?: string; kind?: string; value?: unknown };
    if (data.type !== 'mincode.e2e.ts-plugin') return;
    if (data.kind === 'step') log.done(String(data.value));
    if (data.kind === 'ready') ready(data.value as PageState);
    if (data.kind === 'failed') failed(new Error(String(data.value)));
    if (data.kind === 'probe') {
      const value = data.value as PageState & { id: string };
      waiters.get(value.id)?.(value);
    }
  };
  // The page reports what it rendered; poll until the host change arrives through mincode.host.context.
  async function pageState(expect: (state: PageState) => boolean, label: string) {
    const end = Date.now() + 15000;
    let last: PageState = {};
    while (Date.now() < end) {
      const id = Math.random().toString(36).slice(2);
      last = await new Promise<PageState>(resolve => {
        waiters.set(id, resolve);
        frame()?.contentWindow?.postMessage({ type: 'mincode.e2e.ts-plugin.probe', id }, '*');
        setTimeout(() => resolve(last), 500);
      });
      waiters.delete(id);
      if (expect(last)) return last;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error(`page did not follow host ${label}: ${JSON.stringify(last)}`);
  }
  window.addEventListener('message', listener);
  await installLocalPlugin(config.tsPlugin!.packagePath);
  try {
    if (!(await listPlugins()).some(plugin => plugin.id === pluginId)) throw new Error('installed plugin is not listed');
    await setPluginEnabled(pluginId, true);
    log.done('installed and enabled the built TypeScript + Go package');
    i18n.setLocale('zh');
    applyUiTheme('light');
    const tab = openPluginViewTab(workbench, router, pluginId, 'main', 'TypeScript smoke');
    if (!tab) throw new Error('could not open the plugin view');
    tabId = tab.id;
    const first = await started;
    if (first.theme !== 'light' || !first.title?.includes('插件模板')) throw new Error(`unexpected initial page ${JSON.stringify(first)}`);
    applyUiTheme('dark');
    i18n.setLocale('en');
    const dark = await pageState(state => state.theme === 'dark' && state.title === 'TypeScript + Go Plugin Template', 'dark theme and English');
    if (dark.background === first.background) throw new Error('theme.css variables did not repaint the page');
    log.done('host theme and locale updates repainted the page without reload');
    workbench.closeTab(tabId, true); tabId = '';
    await setPluginEnabled(pluginId, false);
    log.done('closed view and disabled');
  } finally {
    window.removeEventListener('message', listener);
    if (tabId) workbench.closeTab(tabId, true);
    i18n.setLocale(initialLocale);
    applyUiTheme(initialTheme);
    await uninstallPlugin(pluginId, true).catch(() => undefined);
  }
  if ((await listPlugins()).some(plugin => plugin.id === pluginId)) throw new Error('plugin still listed after uninstall');
  log.done('uninstall');
}
