// Opt-in real WebKit test. No AI request or user project is opened.
import { nextTick } from 'vue';
import router from '@/router';
import { openPluginViewTab } from '@/domain/workspace/open-plugin-view';
import { useWorkbenchStore } from '@/stores/workbench';
import { useAiPluginPromptsStore } from '@/stores/ai-plugin-prompts';
import { useAiAssistantStore } from '@/stores/ai-assistant';
import { beginPluginUISession, invokePluginUI, closePluginUISession, installLocalPlugin, listPlugins, setPluginEnabled, uninstallPlugin, clearPluginData } from '@/services/plugin-service';
import { activeSmokeDialog } from '@/services/e2e-dialogs';
import type { SmokeConfig } from '@/domain/e2e/desktop-smoke';

const delay = () => new Promise<void>(resolve => setTimeout(resolve, 70));
async function until<T>(read: () => T | null | undefined | false, label: string): Promise<T> {
 const end = Date.now() + 20000;
 while (Date.now() < end) { const value = read(); if (value) return value; await delay(); }
 throw new Error(`custom prompt smoke timeout: ${label}`);
}
interface Prompt { id: string; title: string; description: string; category: string; body: string; enabled: boolean }
interface Snapshot { revision: number; prompts: Prompt[] }
export async function aiPromptsScenario(config: SmokeConfig, log: { done(step: string): void }) {
 const pluginId = 'ai-prompts', store = useAiPluginPromptsStore(), workbench = useWorkbenchStore();
 let tabId = '', stop = () => {};
 const confirmationTimer = setInterval(() => {
  const box = activeSmokeDialog(document);
  const button = box?.querySelector<HTMLButtonElement>('.el-message-box__btns .el-button--primary');
  if (button && !button.disabled) button.click();
 }, 100);
 const call = async <T>(action: string, payload: unknown): Promise<T> => {
  const session = await beginPluginUISession(pluginId, 'main');
  try { return await invokePluginUI(session.sessionId, session.nonce, action, JSON.stringify(payload)) as T; }
  finally { await closePluginUISession(session.sessionId); }
 };
 await installLocalPlugin(config.aiPrompts!.packagePath);
 try {
  await setPluginEnabled(pluginId, true);
  await until(() => store.enabledPrompts.some(p => p.pluginId === pluginId), 'builtins discovered');
  const installed = (await listPlugins()).find(p => p.id === pluginId);
  if (installed?.state === 'running') throw new Error('static contribution discovery started the plugin');
  log.done('static discovery exposed builtins without process activation');
  const interaction = new Promise<void>((resolve, reject) => {
   const listener = (event: MessageEvent<unknown>) => {
    const frame = document.querySelector<HTMLIFrameElement>('iframe.plugin-view-frame__iframe');
    if (!frame || event.source !== frame.contentWindow || !event.data || typeof event.data !== 'object') return;
    const data = event.data as { type?: string; kind?: string; value?: unknown };
    if (data.type !== 'mincode.e2e.ai-prompts') return;
    if (data.kind === 'step') log.done(String(data.value));
    if (data.kind === 'failed') reject(new Error(String(data.value)));
    if (data.kind === 'done') resolve();
   };
   window.addEventListener('message', listener); stop = () => window.removeEventListener('message', listener);
  });
  const tab = openPluginViewTab(workbench, router, pluginId, 'main', 'Custom prompt smoke');
  if (!tab) throw new Error('could not open custom prompts');
  tabId = tab.id;
  await interaction;
  const saved = await call<Snapshot>('list', {});
  if (saved.prompts.length !== 1 || !saved.prompts[0]?.body.startsWith('EDITED')) throw new Error('UI edits did not persist');
  const prompt = saved.prompts[0];
  await until(() => store.enabledPrompts.some(p => p.id === prompt.id && p.body.startsWith('EDITED')), 'runtime overlay refreshed');
  workbench.closeTab(tabId, true); tabId = '';
  const assistant = useAiAssistantStore();
  await assistant.hydrate(); assistant.show();
  const beforeMessages = assistant.messages.length;
  const textarea = await until(() => document.querySelector<HTMLTextAreaElement>('.ai-prompt-composer textarea, .ai-prompt-input textarea, .ai-composer textarea, .ai-assistant-drawer textarea'), 'AI composer');
  textarea.value = '/'; textarea.dispatchEvent(new Event('input', { bubbles: true }));
  await nextTick();
  const suggestion = await until(() => [...document.querySelectorAll<HTMLElement>('.ai-tool-suggest [role="option"]')].find(el => el.textContent?.includes('Native custom prompt')), 'real slash suggestion');
  suggestion.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  await until(() => textarea.value.startsWith('EDITED') && !textarea.value.includes('{{'), 'template expanded into draft');
  if (assistant.messages.length !== beforeMessages || assistant.loading) throw new Error('selecting template sent a request');
  assistant.close();
  log.done('real AI slash selection expanded variables into draft and sent nothing');
  await setPluginEnabled(pluginId, false);
  await until(() => !store.enabledPrompts.some(p => p.pluginId === pluginId), 'disabled contribution revoked');
  await setPluginEnabled(pluginId, true);
  await until(() => store.enabledPrompts.some(p => p.id === prompt.id), 'saved overlay recovered before opening UI');
  log.done('custom overlay recovered after process restart without opening management');
  const loaded = await call<Snapshot>('list', {});
  const disabled = await call<Snapshot>('save', { revision: loaded.revision, prompt: { ...prompt, enabled: false } });
  await until(() => !store.enabledPrompts.some(p => p.id === prompt.id), 'disabled custom template removed');
  await call('delete', { revision: disabled.revision, id: prompt.id });
  if ((await call<Snapshot>('list', {})).prompts.length) throw new Error('delete did not persist');
  await clearPluginData(pluginId);
  log.done('custom disable, revisioned delete and clear-data completed through native host');
 } finally {
  clearInterval(confirmationTimer); stop(); if (tabId) workbench.closeTab(tabId, true);
  await uninstallPlugin(pluginId, true);
 }
}
