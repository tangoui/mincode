import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { nextTick } from 'vue';
import { ElMessageBox } from 'element-plus';
import AiPrompts from '../../../official-plugins/ai-prompts/ui-src/AiPrompts.vue';

const bridge = vi.hoisted(() => ({ options: null as null | { onReady(): void; onError(message: string): void; applyContext(context: { locale: string }): void }, call: vi.fn(), close: vi.fn() }));
vi.mock('@mincode/plugin-ui/transport.js', () => ({ createTransport: (options: typeof bridge.options) => { bridge.options = options; return { call: bridge.call, close: bridge.close }; } }));
const empty = () => ({ version: 1, revision: 0, capacity: 17, prompts: [], entries: [], builtins: [{ id: 'review', kind: 'prompt', title: 'Review', description: 'Check code', body: 'Check {{selection}}' }, { id: 'guide', kind: 'skill', title: 'Guide', description: 'Style', body: 'Small functions' }] });
async function click(wrapper: ReturnType<typeof mount>, text: string) {
 const button = wrapper.findAll('button').find(b => b.text() === text);
 expect(button, text).toBeTruthy();
 await button!.trigger('click'); await flushPromises();
}
describe('AI prompts management', () => {
 beforeEach(() => { bridge.call.mockReset(); bridge.close.mockReset(); bridge.call.mockResolvedValue(empty()); });
 it('creates a custom prompt through the host and preserves rejected edits', async () => {
  const wrapper = mount(AiPrompts, { attachTo: document.body });
  bridge.options!.applyContext({ locale: 'en' }); bridge.options!.onReady(); await flushPromises();
  await click(wrapper, 'New prompt');
  const inputs = wrapper.findAll('input');
  await inputs.find(i => i.attributes('maxlength') === '64')!.setValue('My review');
  await wrapper.find('textarea').setValue('Explain {{selection}}');
  bridge.call.mockResolvedValueOnce({ ...empty(), revision: 1, prompts: [{ id: 'custom-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', title: 'My review', description: '', category: '', body: 'Explain {{selection}}', enabled: true }] });
  await click(wrapper, 'Save');
  expect(bridge.call).toHaveBeenLastCalledWith('save', { revision: 0, prompt: { id: '', title: 'My review', description: '', category: '', body: 'Explain {{selection}}', enabled: true } });
  expect(wrapper.text()).toContain('My review');
  await wrapper.find('textarea').setValue('Unsaved new text');
  bridge.call.mockRejectedValueOnce(new Error('revision conflict: reload before saving'));
  await click(wrapper, 'Save');
  expect(wrapper.text()).toContain('Another window updated');
  expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('Unsaved new text');
  wrapper.unmount();
  expect(bridge.close).toHaveBeenCalled();
 });
 it('shows unsupported host without pretending an operation succeeded', async () => {
  bridge.call.mockRejectedValueOnce(new Error('[UNSUPPORTED] update MinCode'));
  const wrapper = mount(AiPrompts);
  bridge.options!.applyContext({ locale: 'en' }); bridge.options!.onReady(); await flushPromises();
  expect(wrapper.text()).toContain('does not support custom prompt management');
  expect(wrapper.findAll('button').find(b => b.text() === 'New prompt')!.attributes('disabled')).toBeDefined();
  wrapper.unmount();
 });
 it('disconnect makes in-flight results stale and prevents further writes', async () => {
  let finish: (value: ReturnType<typeof empty>) => void = () => {};
  bridge.call.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const wrapper = mount(AiPrompts);
  bridge.options!.applyContext({ locale: 'en' }); bridge.options!.onReady(); await nextTick();
  bridge.options!.onError('gone'); finish(empty()); await flushPromises();
  expect(wrapper.text()).toContain('Host disconnected');
  expect(wrapper.findAll('button').find(b => b.text() === 'New prompt')!.attributes('disabled')).toBeDefined();
  wrapper.unmount();
 });
});

describe('review regressions', () => {
 beforeEach(() => { bridge.call.mockReset(); bridge.call.mockResolvedValue(empty()); });
 it('new and copy switch tabs before making a dirty draft', async () => {
  const confirm = vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue('close');
  for (const copy of [false, true]) {
   const wrapper = mount(AiPrompts, { attachTo: document.body });
   bridge.options!.applyContext({ locale: 'en' }); bridge.options!.onReady(); await flushPromises();
   if (copy) { await click(wrapper, 'Review'); await click(wrapper, 'Copy as custom'); }
   else await click(wrapper, 'New prompt');
   expect(confirm).not.toHaveBeenCalled();
   expect(wrapper.find('[role="tab"][aria-selected="true"]').text()).toBe('My prompts');
   expect(wrapper.find('textarea').exists()).toBe(true);
   if (copy) expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('Check {{selection}}');
   wrapper.unmount();
  }
 });
 it('refresh failure preserves an editable and savable loaded draft', async () => {
  vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue('cancel');
  const wrapper = mount(AiPrompts, { attachTo: document.body });
  bridge.options!.applyContext({ locale: 'en' }); bridge.options!.onReady(); await flushPromises();
  await wrapper.findAll('[role="tab"]').find(t => t.text() === 'My prompts')!.trigger('click'); await flushPromises();
  await click(wrapper, 'New prompt');
  await wrapper.find('input[maxlength="64"]').setValue('Retained');
  await wrapper.find('textarea').setValue('Do not lose me');
  bridge.call.mockRejectedValueOnce(new Error('temporary list failure'));
  await click(wrapper, 'Reload');
  expect(wrapper.find('textarea').attributes('disabled')).toBeUndefined();
  bridge.call.mockResolvedValueOnce({ ...empty(), revision: 1, prompts: [{ id: 'custom-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', title: 'Retained', description: '', category: '', body: 'Do not lose me', enabled: true }] });
  await click(wrapper, 'Save');
  expect(bridge.call).toHaveBeenLastCalledWith('save', expect.objectContaining({ revision: 0, prompt: expect.objectContaining({ body: 'Do not lose me' }) }));
  wrapper.unmount();
 });
});
