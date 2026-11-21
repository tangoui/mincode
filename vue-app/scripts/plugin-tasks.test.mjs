import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import ts from 'typescript';
import { computed, reactive, ref, watch } from 'vue';
import { taskMessagesEn, taskMessagesZh } from '../src/plugin-ui/task-messages.ts';
import * as protocol from '../src/domain/plugin-ui/protocol.ts';

const source = path => readFile(new URL(path, import.meta.url), 'utf8');
function evaluate(text, dependencies) {
  const code = ts.transpileModule(text.replace(/^import[^;]+;\s*/gm, '').replace(/export /g, ''), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  return new Function(...Object.keys(dependencies), code)(...Object.values(dependencies));
}

test('host request parser accepts bounded clipboard and trusted confirmation requests only', () => {
  assert.deepEqual(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.clipboard.write-text', id: 'copy-1', text: 'hello' }), {
    type: 'mincode.plugin.clipboard.write-text', id: 'copy-1', text: 'hello', sensitive: false,
  });
  assert.deepEqual(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.confirm', id: 'confirm-1', intent: 'dangerous', subject: 'delete item' }), {
    type: 'mincode.plugin.confirm', id: 'confirm-1', intent: 'dangerous', subject: 'delete item',
  });
  assert.equal(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.clipboard.read-text', id: 'read-1' }), undefined);
  assert.equal(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.clipboard.write-text', id: 'copy-2', text: 'x'.repeat(protocol.PLUGIN_UI_MAX_CLIPBOARD_TEXT_BYTES + 1) }), undefined);
  assert.equal(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.confirm', id: 'confirm-2', intent: 'read', subject: 'inspect' }), undefined);
  assert.equal(protocol.parsePluginUIHostRequest({ type: 'mincode.plugin.confirm', id: 'confirm-3', intent: 'write', subject: '' }), undefined);
});

test('shared page defaults to invoke and passes task payload/results without reformatting', async () => {
  const page = await source('../src/plugin-ui/OfficialPluginPage.vue');
  const script = page.match(/<script setup>([^]*?)<\/script>/)[1];
  const calls = [];
  const api = evaluate(script + '\nreturn { run, longTask, taskRun, finishTask, taskBusy, result, completed, busy };', {
    computed, reactive, ref, watch, provide() {}, pluginLocaleKey: Symbol('locale'), onUnmounted() {}, taskMessagesEn, taskMessagesZh,
    defineProps: () => ({ config: { fields: [], actions: [{ id: 'run', label: 'Run' }] }, policy: {} }),
    createTransport: () => ({ call: async (...args) => { calls.push(args); return { rows: [1] }; } }),
    buildPayload: () => ({ input: 1 }),
  });
  // Simulate the authenticated onReady callback via the component's connected ref.
  const readyScript = script.replace('const connected = ref(false)', 'const connected = ref(true)');
  const ready = evaluate(readyScript + '\nreturn { run, longTask, taskRun, finishTask, taskBusy, taskUncertain, result, completed, busy };', {
    computed, reactive, ref, watch, provide() {}, pluginLocaleKey: Symbol('locale'), onUnmounted() {}, taskMessagesEn, taskMessagesZh,
    defineProps: () => ({ config: { fields: [], actions: [{ id: 'run', label: 'Run' }] }, policy: {} }),
    createTransport: () => ({ call: async (...args) => { calls.push(args); return { rows: [1] }; } }), buildPayload: () => ({ input: 1 }),
  });
  assert.equal(api.longTask.value, false);
  await ready.run({ id: 'run', label: 'Run' });
  assert.deepEqual(ready.result.value, { rows: [1] });
  ready.longTask.value = true;
  await ready.run({ id: 'run', label: 'Run' });
  assert.equal(calls.length, 1);
  assert.deepEqual(ready.taskRun.value.payload, { input: 1 });
  ready.finishTask(false); ready.taskBusy(false);
  assert.equal(ready.result.value, false);
  assert.equal(ready.completed.value, 'Run');
  assert.equal(ready.busy.value, false);
  ready.taskUncertain.value = true;
  const previousRun = ready.taskRun.value;
  await ready.run({ id: 'run', label: 'Run' });
  assert.equal(ready.taskRun.value, previousRun);
  ready.longTask.value = false;
  await ready.run({ id: 'run', label: 'Run' });
  assert.equal(calls.length, 1);
  assert.match(page, /@uncertain="taskUncertain = \$event"/);
  assert.match(page, /:disabled="!connected \|\| busy \|\| taskUncertain"/);
  assert.match(page, /<PluginTaskPanel[^]*?auto-start[^]*?@result="finishTask"/);
  assert.match(page, /<ElCheckbox v-model="longTask"/);
});

test('panel cancels late start after unmount and distinguishes polling uncertainty from failure', async () => {
  const panel = await source('../src/plugin-ui/PluginTaskPanel.vue');
  const script = panel.match(/<script setup lang="ts">([^]*?)<\/script>/)[1];
  async function setup(tasks) {
    let unmount;
    const events = [];
    const api = evaluate(script + '\nreturn { start, cancel, recheck, snapshot, uncertain, error };', {
      computed, ref, taskMessagesEn, taskMessagesZh,
      defineProps: () => ({ tasks, action: 'run', locale: 'en' }), defineEmits: () => (...args) => events.push(args),
      onMounted() {}, onBeforeUnmount(fn) { unmount = fn; },
      setTimeout(fn) { queueMicrotask(fn); return 1; }, clearTimeout() {},
    });
    return { ...api, unmount, events };
  }
  let resolveStart;
  const cancelled = [];
  const late = await setup({ start: () => new Promise(resolve => { resolveStart = resolve; }), cancel: async id => cancelled.push(id) });
  const pending = late.start(); late.unmount();
  resolveStart({ taskId: 'late', state: 'running', cancelRequested: false });
  await pending;
  assert.deepEqual(cancelled, ['late']);
  assert.equal(late.snapshot.value, undefined);
  const polling = await setup({ start: async () => ({ taskId: 't', state: 'running', cancelRequested: false }), get: async () => { throw new Error('Disconnected'); } });
  await polling.start();
  assert.equal(polling.uncertain.value, true);
  assert.match(polling.error.value, /may still be running/);
  const failed = await setup({ start: async () => ({ taskId: 't', state: 'failed', cancelRequested: false, error: 'Action failed' }) });
  await failed.start();
  assert.equal(failed.uncertain.value, false);
  assert.equal(failed.error.value, 'Action failed');
  const unknown = { taskId: 't', state: 'failed', cancelRequested: true, outcomeUnconfirmed: true, error: 'opaque detail' };
  for (const viaPoll of [false, true]) {
    let starts = 0;
    const panel = await setup({
      start: async () => { starts++; return viaPoll ? { ...unknown, state: 'running', outcomeUnconfirmed: false } : unknown; },
      get: async () => unknown,
    });
    await panel.start();
    assert.equal(panel.uncertain.value, true);
    assert.ok(panel.events.some(([name, value]) => name === 'uncertain' && value === true));
    assert.ok(!panel.events.some(([name]) => name === 'result'));
    await panel.start();
    assert.equal(starts, 1);
  }
  const cancellation = await setup({ cancel: async () => unknown });
  cancellation.snapshot.value = { taskId: 't', state: 'running', cancelRequested: false };
  await cancellation.cancel();
  assert.equal(cancellation.uncertain.value, true);
  assert.ok(cancellation.events.some(([name, value]) => name === 'uncertain' && value === true));

  let reads = 0, starts = 0;
  const recovered = await setup({
    start: async () => { starts++; return { taskId: 't', state: 'running', cancelRequested: false }; },
    get: async () => { if (++reads === 1) throw new Error('offline'); return { taskId: 't', state: 'succeeded', cancelRequested: false, result: false }; },
  });
  await recovered.start();
  assert.equal(recovered.uncertain.value, true);
  await recovered.recheck();
  assert.equal(recovered.uncertain.value, false);
  assert.equal(recovered.error.value, '');
  assert.ok(recovered.events.some(([name, value]) => name === 'uncertain' && value === false));
  assert.deepEqual(recovered.events.filter(([name]) => name === 'result'), [['result', false]]);
  assert.equal(starts, 1);

  for (const stale of ['running', 'failed', 'reject']) {
    let finishCancel;
    const reverse = await setup({
      get: async () => ({ taskId: 't', state: 'succeeded', cancelRequested: false, result: 7 }),
      cancel: () => new Promise((resolve, reject) => { finishCancel = () => stale === 'reject' ? reject(new Error('offline')) : resolve({ taskId: 't', state: stale, cancelRequested: true, outcomeUnconfirmed: stale === 'failed' }); }),
    });
    reverse.snapshot.value = { taskId: 't', state: 'running', cancelRequested: false };
    const cancelling = reverse.cancel();
    await reverse.recheck(); finishCancel(); await cancelling;
    assert.equal(reverse.snapshot.value.state, 'succeeded');
    assert.equal(reverse.uncertain.value, false);
    assert.deepEqual(reverse.events.filter(([name]) => name === 'result'), [['result', 7]]);
    let finishGet;
    const race = await setup({
      start: async () => ({ taskId: 't', state: 'running', cancelRequested: false }),
      get: () => new Promise((resolve, reject) => { finishGet = () => stale === 'reject' ? reject(new Error('offline')) : resolve({ taskId: 't', state: stale, cancelRequested: false, outcomeUnconfirmed: stale === 'failed' }); }),
      cancel: async () => ({ taskId: 't', state: 'succeeded', cancelRequested: true, result: 42 }),
    });
    const running = race.start();
    await new Promise(resolve => setImmediate(resolve));
    await race.cancel(); finishGet(); await running;
    assert.equal(race.snapshot.value.state, 'succeeded');
    assert.equal(race.uncertain.value, false);
    assert.deepEqual(race.events.filter(([name]) => name === 'result'), [['result', 42]]);
  }
});

test('task byte budgets accept maximum results without expanding request limits', async () => {
  const result = 'x'.repeat(protocol.PLUGIN_UI_MAX_TASK_RESULT_BYTES - 2);
  const progress = 'p'.repeat(protocol.PLUGIN_UI_MAX_TASK_PROGRESS_BYTES - 2);
  const snapshot = { taskId: 't', state: 'succeeded', cancelRequested: false, result, progress, error: 'e'.repeat(7000) };
  assert.equal(protocol.parsePluginUITaskSnapshot(snapshot).result, result);
  for (const value of [{ ...snapshot, result: result + 'x' }, { ...snapshot, progress: progress + 'p' }, { ...snapshot, extra: 'x'.repeat(8192) }]) {
    assert.throws(() => protocol.parsePluginUITaskSnapshot(value));
  }
  const frame = await source('../src/components/plugins/PluginViewFrame.vue');
  const posted = [];
  const post = evaluate(frame.match(/function safePost\([^]*?\n\}/)[0] + '\nreturn safePost;', {
    ...protocol, messagePort: { postMessage: value => posted.push(value) }, t: key => key, failClosed() { assert.fail('closed'); },
  });
  const response = { type: 'mincode.host.response', id: 'r'.repeat(128), ok: true, result: snapshot };
  post(response, true); assert.equal(posted.at(-1).ok, true);
  post(response); assert.equal(posted.at(-1).error.code, 'result-too-large');
  post({ ...response, result: 'x'.repeat(protocol.PLUGIN_UI_MAX_TASK_RESPONSE_BYTES) }, true);
  assert.equal(posted.at(-1).error.code, 'result-too-large');
  const service = evaluate(await source('../src/services/plugin-service.ts') + '\nreturn { startPluginUITask, getPluginUITask, cancelPluginUITask };', {
    ...protocol, invokeBackend: async () => snapshot,
  });
  assert.equal((await service.startPluginUITask('s', 'n', 'run')).result, result);
  assert.equal((await service.getPluginUITask('s', 'n', 't')).result, result);
  assert.equal((await service.cancelPluginUITask('s', 'n', 't')).result, result);
  await assert.rejects(service.startPluginUITask('s', 'n', 'run', 'x'.repeat(262145)));
});

test('task protocol validates timeout, size, snapshot state and identity', () => {
  const start = { type: 'mincode.plugin.task.start', id: 'r', action: 'run' };
  assert.equal(protocol.parsePluginUITaskRequest(start).timeoutMs, 300000);
  for (const timeoutMs of [0, -1, 300001, 1.5, '100', null]) assert.equal(protocol.parsePluginUITaskRequest({ ...start, timeoutMs }), undefined);
  assert.equal(protocol.parsePluginUITaskRequest({ ...start, payload: 'x'.repeat(262144) }), undefined);
  assert.equal(protocol.parsePluginUITaskRequest({ type: 'mincode.plugin.task.get', id: 'r', taskId: '' }), undefined);
  const snapshot = { taskId: 'task', state: 'running', cancelRequested: false, progress: { count: 1 } };
  assert.equal(protocol.parsePluginUITaskSnapshot(snapshot, 'task').state, 'running');
  for (const value of [{ ...snapshot, state: 'done' }, { ...snapshot, cancelRequested: 0 }, { ...snapshot, error: {} }]) assert.throws(() => protocol.parsePluginUITaskSnapshot(value));
  assert.throws(() => protocol.parsePluginUITaskSnapshot(snapshot, 'other'));
  for (const outcomeUnconfirmed of [undefined, false, true]) {
    assert.equal(protocol.parsePluginUITaskSnapshot({ ...snapshot, outcomeUnconfirmed }).outcomeUnconfirmed, outcomeUnconfirmed);
  }
  for (const outcomeUnconfirmed of [null, 0, 1, 'true', {}]) {
    assert.throws(() => protocol.parsePluginUITaskSnapshot({ ...snapshot, outcomeUnconfirmed }));
  }
});

test('service uses core task dispatch, validates responses and rejects invalid inputs before native I/O', async () => {
  const calls = [];
  const service = evaluate(await source('../src/services/plugin-service.ts') + '\nreturn { startPluginUITask, getPluginUITask, cancelPluginUITask };', {
    ...protocol,
    invokeBackend: async (name, args) => { calls.push([name, args]); return { taskId: 'task', state: 'failed', cancelRequested: false, outcomeUnconfirmed: true }; },
  });
  assert.equal((await service.startPluginUITask('s', 'n', 'run', '{}')).outcomeUnconfirmed, true);
  assert.equal((await service.getPluginUITask('s', 'n', 'task')).outcomeUnconfirmed, true);
  assert.equal((await service.cancelPluginUITask('s', 'n', 'task')).outcomeUnconfirmed, true);
  assert.deepEqual(calls.map(([name]) => name), ['plugin_ui_task_start', 'plugin_ui_task_get', 'plugin_ui_task_cancel']);
  assert.deepEqual(calls[0][1], { sessionId: 's', nonce: 'n', action: 'run', payload: '{}', timeoutMs: 300000 });
  await assert.rejects(service.startPluginUITask('s', 'n', 'run', '{}', 300001));
  await assert.rejects(service.getPluginUITask('s', 'n', 'other'), /snapshot/);
});

test('authenticated transport negotiates tasks, preserves invoke, bounds requests and cleans up', async () => {
  const listeners = new Map(), sent = [], timers = new Map();
  let sequence = 0;
  const parent = { postMessage() {} };
  const context = vm.createContext({ URLSearchParams, location: { hash: '#mincodeSession=s&nonce=n&generation=1' }, parent,
    setTimeout(fn) { timers.set(++sequence, fn); return sequence; }, clearTimeout(id) { timers.delete(id); },
    addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: name => listeners.delete(name),
  });
  // transport.js imports the self-contained theme-tokens.js helper; inline it
  // so the transport runs as a classic script inside the vm sandbox.
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const transportSource = await source('../src/plugin-ui/transport.js');
  assert.match(transportSource, themeImport);
  const themeTokensSource = (await source('../src/plugin-ui/theme-tokens.js')).replaceAll('export function', 'function');
  vm.runInContext(themeTokensSource + transportSource.replace(themeImport, '').replace('export function', 'function'), context);
  const transport = context.createTransport({ onReady() {}, onError() {}, applyContext() {} });
  await assert.rejects(transport.call('run', {}), /连接/);
  const port = { start() {}, close() {}, postMessage(data) { sent.push(data); } };
  const connect = listeners.get('message');
  const data = { type: 'mincode.host.connect', protocolVersion: 1, sessionId: 's', nonce: 'wrong', generation: 1, features: { tasks: 1 }, context: { locale: 'en' } };
  connect({ source: parent, data, ports: [port] });
  await assert.rejects(transport.tasks.start('run', {}));
  connect({ source: parent, data: { ...data, nonce: 'n' }, ports: [port] });
  const first = transport.tasks.start('run', {});
  assert.equal(sent[0].type, 'mincode.plugin.task.start');
  assert.equal(sent[0].timeoutMs, 300000);
  port.onmessage({ data: { type: 'mincode.host.response', id: sent[0].id, ok: false, error: { message: 'runtime does not support tasks' } } });
  await assert.rejects(first, /does not support/);
  const snapshotRequest = transport.tasks.get('task');
  port.onmessage({ data: { type: 'mincode.host.response', id: sent.at(-1).id, ok: true,
    result: { taskId: 'task', state: 'failed', cancelRequested: true, outcomeUnconfirmed: true } } });
  assert.equal((await snapshotRequest).outcomeUnconfirmed, true);
  const pending = Array.from({ length: 8 }, () => transport.call('short', {}));
  await assert.rejects(transport.tasks.get('task'), /Too many/);
  const settled = Promise.allSettled(pending);
  transport.close();
  assert.ok((await settled).every(item => item.status === 'rejected'));
  assert.equal(timers.size, 0);
  assert.equal(listeners.size, 0);
  await assert.rejects(transport.tasks.cancel('task'), /not connected/);
});

test('frame task start requires approval and rejects foreign tasks without native calls', async () => {
  const frame = await source('../src/components/plugins/PluginViewFrame.vue');
  const invoke = frame.match(/async function handleInvokeRequest\(data: unknown\) \{[^]*?\n\}/)[0];
  const lookup = frame.match(/async function handleTaskLookup\([^]*?\n\}/)[0];
  const session = { sessionId: 's', nonce: 'n', actions: [{ id: 'run', risk: 'write', maxPayloadBytes: 1000 }] };
  const responses = [], calls = [];
  let approve = false;
  const handlers = evaluate(`${invoke}\n${lookup}\nreturn { handleInvokeRequest };`, {
    ...protocol, currentSession: session, connected: true, messagePort: {}, ownedTasks: new Map(), pendingTaskStarts: 0,
    activeRequestIds: { value: new Set() }, t: key => key, props: { warm: false }, rejectWarm: id => responses.push([id, "not-allowed", "warm"]),
    confirmAction: async () => approve, safePost: value => responses.push(value), rejectRequest: (...args) => responses.push(args),
    describePluginActionFailure: () => 'failed',
    startPluginUITask: async (...args) => { calls.push(args); return { taskId: 'owned', state: 'running', cancelRequested: false }; },
    getPluginUITask: async () => { calls.push('get'); return { taskId: 'owned', state: 'running', cancelRequested: false }; },
  });
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'r', action: 'run' });
  assert.equal(calls.length, 0);
  approve = true;
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'r2', action: 'run' });
  assert.equal(calls.length, 1);
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.get', id: 'r3', taskId: 'foreign' });
  assert.equal(calls.length, 1);
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.get', id: 'r4', taskId: 'owned' });
  assert.equal(calls.length, 2);
  assert.match(frame, /ownedTasks = new Map\(\)/);
  assert.match(frame, /currentSession !== session \|\| !connected/);
});

test('terminal snapshots free active slots while ownership stays bounded and expires', async () => {
  const frame = await source('../src/components/plugins/PluginViewFrame.vue');
  const invoke = frame.match(/async function handleInvokeRequest\(data: unknown\) \{[^]*?\n\}/)[0];
  const lookup = frame.match(/async function handleTaskLookup\([^]*?\n\}/)[0];
  const tasks = new Map(), responses = [];
  let sequence = 0;
  const handlers = evaluate(`${invoke}\n${lookup}\nreturn { handleInvokeRequest };`, {
    ...protocol, ownedTasks: tasks, pendingTaskStarts: 0, connected: true, messagePort: {},
    currentSession: { sessionId: 's', nonce: 'n', actions: [{ id: 'run', maxPayloadBytes: 1000 }] },
    activeRequestIds: { value: new Set() }, t: key => key, props: { warm: false }, rejectWarm: id => responses.push([id, "not-allowed", "warm"]), confirmAction: async () => true,
    safePost: value => responses.push(value), rejectRequest: (...args) => responses.push(args), describePluginActionFailure: () => 'failed',
    startPluginUITask: async () => ({ taskId: String(++sequence), state: 'running', cancelRequested: false }),
    getPluginUITask: async (_s, _n, taskId) => ({ taskId, state: 'succeeded', cancelRequested: false }),
  });
  for (let i = 1; i <= 64; i++) {
    await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: `s${i}`, action: 'run' });
    await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.get', id: `g${i}`, taskId: String(i) });
  }
  assert.equal(sequence, 64);
  assert.equal(tasks.size, 64);
  // Every get delivered a terminal snapshot to the page.
  assert.ok([...tasks.values()].every(task => task.terminal && task.delivered));
  const expires = tasks.get('1').expires;
  protocol.rememberPluginUITask(tasks, { taskId: '1', state: 'running' });
  assert.equal(tasks.get('1').terminal, true);
  assert.equal(tasks.get('1').expires, expires);
  // Undelivered terminal results still hold their slots.
  for (const task of tasks.values()) task.delivered = false;
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'overflow', action: 'run' });
  assert.equal(responses.at(-1)[1], 'busy');
  assert.equal(sequence, 64);
  tasks.get('1').expires = Date.now() - 1;
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'after-expiry', action: 'run' });
  assert.equal(sequence, 65);
  assert.equal(tasks.size, 64);
  // Delivered results free their slots for new starts, oldest finished first.
  tasks.get('3').delivered = true;
  tasks.get('2').delivered = true;
  tasks.get('2').expires -= 1000;
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'after-delivery', action: 'run' });
  assert.equal(sequence, 66);
  assert.equal(tasks.size, 64);
  assert.equal(tasks.has('2'), false);
  assert.equal(tasks.has('3'), true);
});

test('delivered-task eviction keeps running and unread tasks', () => {
  const now = Date.now();
  const tasks = new Map([
    ['running', { terminal: false, expires: now + 1, delivered: true }],
    ['unread', { terminal: true, expires: now + 2 }],
    ['newer', { terminal: true, expires: now + 30, delivered: true }],
    ['older', { terminal: true, expires: now + 20, delivered: true }],
  ]);
  protocol.evictDeliveredPluginUITasks(tasks, 0, 4);
  assert.deepEqual([...tasks.keys()], ['running', 'unread', 'newer']);
  protocol.evictDeliveredPluginUITasks(tasks, 1, 4);
  assert.deepEqual([...tasks.keys()], ['running', 'unread']);
  protocol.evictDeliveredPluginUITasks(tasks, 4, 4);
  assert.deepEqual([...tasks.keys()], ['running', 'unread']);
  protocol.markPluginUITaskDelivered(tasks, 'running');
  protocol.markPluginUITaskDelivered(tasks, 'unread');
  assert.equal(tasks.get('unread').delivered, true);
  assert.equal(tasks.get('running').terminal, false);
});

test('task approval cannot start after session revocation and start reservations are bounded', async () => {
  const frame = await source('../src/components/plugins/PluginViewFrame.vue');
  const invoke = frame.match(/async function handleInvokeRequest\(data: unknown\) \{[^]*?\n\}/)[0];
  const approvals = [], calls = [], responses = [];
  const handlers = evaluate(`${invoke}\nreturn { handleInvokeRequest, revoke() { currentSession = undefined; connected = false; } };`, {
    ...protocol, currentSession: { sessionId: 's', nonce: 'n', actions: [{ id: 'run', risk: 'write', maxPayloadBytes: 1000 }] },
    connected: true, messagePort: {}, ownedTasks: new Map(), pendingTaskStarts: 0,
    activeRequestIds: { value: new Set() }, t: key => key, props: { warm: false }, rejectWarm: id => responses.push([id, "not-allowed", "warm"]),
    confirmAction: () => new Promise(resolve => approvals.push(resolve)),
    safePost: value => responses.push(value), rejectRequest: (...args) => responses.push(args),
    startPluginUITask: async () => calls.push('start'), describePluginActionFailure: () => 'failed',
  });
  const pending = Array.from({ length: 8 }, (_, index) => handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: `r${index}`, action: 'run' }));
  await handlers.handleInvokeRequest({ type: 'mincode.plugin.task.start', id: 'overflow', action: 'run' });
  assert.equal(responses.at(-1)[1], 'busy');
  assert.equal(approvals.length, 8);
  handlers.revoke();
  approvals.forEach(resolve => resolve(true));
  await Promise.all(pending);
  assert.equal(calls.length, 0);
});

test('dormant views remain launchable and shared task UI has bounded polling and cleanup', async () => {
  const manager = await source('../src/views/PluginManagerWorkspaceView.vue');
  assert.match(manager, /!\['running', 'stopped'\]\.includes\(plugin.state\)/);
  assert.match(manager, /plugins.dormantHelp/);
  const panel = await source('../src/plugin-ui/PluginTaskPanel.vue');
  assert.match(panel, /Date.now\(\) >= deadline/);
  assert.match(panel, /onBeforeUnmount/);
  assert.match(panel, /clearTimeout\(timer\); wake\?\.\(\)/);
  assert.doesNotMatch(panel, /<(button|input|select)\b/);
});

test('task panel finishes on a pushed terminal snapshot without waiting for its poll', async () => {
  const panel = await source('../src/plugin-ui/PluginTaskPanel.vue');
  const script = panel.match(/<script setup lang="ts">([^]*?)<\/script>/)[1];
  let listener, unsubscribed = false, reads = 0;
  const delays = [];
  const tasks = {
    start: async () => ({ taskId: 't', state: 'running', cancelRequested: false }),
    get: async () => { reads++; return { taskId: 't', state: 'running', cancelRequested: false }; },
    cancel: async () => {},
    pushSupported: () => true,
    onChanged(fn) { listener = fn; return () => { unsubscribed = true; }; },
  };
  const events = [];
  const api = evaluate(script + '\nreturn { start, snapshot };', {
    computed, ref, taskMessagesEn, taskMessagesZh,
    defineProps: () => ({ tasks, action: 'run', locale: 'en' }), defineEmits: () => (...args) => events.push(args),
    onMounted() {}, onBeforeUnmount() {},
    // The fallback poll timer never fires here: only the push can end the wait.
    setTimeout(_fn, ms) { delays.push(ms); return 1; }, clearTimeout() {},
  });
  const running = api.start();
  for (let i = 0; i < 10 && !listener; i++) await Promise.resolve();
  listener({ taskId: 'other', state: 'succeeded', cancelRequested: false, result: 'wrong' });
  listener({ taskId: 't', state: 'succeeded', cancelRequested: false, result: 42 });
  await running;
  assert.deepEqual(events.find(([name]) => name === 'result'), ['result', 42]);
  assert.equal(reads, 0);
  assert.deepEqual(delays, [2000], 'pushing hosts poll every 2 s as a fallback');
  assert.equal(unsubscribed, true);
});

test('a forwarded terminal task push is acknowledged so its slot is released', async () => {
  const frame = await source('../src/components/plugins/PluginViewFrame.vue');
  const handler = frame.match(/function handleTaskChanged\(value: unknown\) \{[^]*?\n\}/)[0];
  const taskPush = await import('../src/domain/plugin-ui/task-push.ts');
  const posted = [], acks = [];
  let failPost = false;
  const ownedTasks = new Map([['t1', { terminal: false, expires: Date.now() + 60_000 }], ['t2', { terminal: false, expires: Date.now() + 60_000 }]]);
  const run = evaluate(`${handler}\nreturn handleTaskChanged;`, {
    ...protocol, ...taskPush, ownedTasks, connected: true, props: { pluginId: 'p' },
    currentSession: { sessionId: 's', nonce: 'n', generation: 1 },
    messagePort: { postMessage(message) { if (failPost) throw new Error('closed'); posted.push(message); } },
    acknowledgePluginUITask: async (...args) => { acks.push(args); },
  });
  const push = (taskId, state) => ({ pluginId: 'p', sessionId: 's', generation: 1, taskId, snapshot: { taskId, state, cancelRequested: false } });
  run(push('t1', 'running'));
  assert.equal(posted.length, 1);
  assert.equal(acks.length, 0);
  run(push('t1', 'succeeded'));
  assert.equal(posted.length, 2);
  assert.deepEqual(acks, [['s', 'n', 't1']]);
  assert.equal(ownedTasks.get('t1').delivered, true);
  failPost = true;
  run(push('t2', 'succeeded'));
  assert.equal(acks.length, 1, 'a push that never reached the page is not acknowledged');
  assert.notEqual(ownedTasks.get('t2').delivered, true);
});
