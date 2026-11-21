import test from 'node:test';
import assert from 'node:assert/strict';
import { AI_TASK_TIMEOUT_MS, TASK_PUSH_POLL_MS, clipUtf8, isTaskUnsupported, runAIAction, runTask, runTaskOrCall, utf8Length } from '../src/plugin-ui/tasks.mjs';
import { parsePluginUITaskChangedEvent, pluginUITaskPushMessage } from '../src/domain/plugin-ui/task-push.ts';

function fakeTasks(states, { startError } = {}) {
  const calls = { start: [], get: 0, cancel: [] };
  return {
    calls,
    async start(action, payload, timeoutMs) {
      calls.start.push({ action, payload, timeoutMs });
      if (startError) throw startError;
      return { taskId: 't1', ...states.shift() };
    },
    async get() { calls.get++; return { taskId: 't1', ...states.shift() }; },
    async cancel(id) { calls.cancel.push(id); return { taskId: id, state: 'cancelled' }; },
  };
}
const sleep = async () => {};

test('runTask polls until success and reports progress', async () => {
  const tasks = fakeTasks([{ state: 'running', progress: { done: 1 } }, { state: 'running' }, { state: 'succeeded', result: { ok: 1 } }]);
  const seen = [], started = [], control = {};
  const onStart = snapshot => started.push([snapshot.taskId, typeof control.cancel]);
  const result = await runTask(tasks, 'job', { a: 1 }, { sleep, control, onStart, onProgress: value => seen.push(value) });
  assert.deepEqual(result, { ok: 1 });
  assert.deepEqual(started, [['t1', 'function']]);
  assert.deepEqual(seen, [{ done: 1 }]);
  assert.equal(tasks.calls.get, 2);
  assert.equal(control.cancel, null);
});

test('runTask maps failed, cancelled, stale and overdue tasks', async () => {
  await assert.rejects(runTask(fakeTasks([{ state: 'failed', error: 'AI 调用过于频繁' }]), 'x', {}, { sleep }), /AI 调用过于频繁/);
  await assert.rejects(runTask(fakeTasks([{ state: 'cancelled', result: { done: 1 } }]), 'x', {}, { sleep }), error => error.cancelled === true && error.result.done === 1);
  const stale = fakeTasks([{ state: 'running' }]);
  await assert.rejects(runTask(stale, 'x', {}, { sleep, alive: () => false }), error => error.cancelled === true);
  assert.deepEqual(stale.calls.cancel, ['t1']);
  let clock = 0;
  const overdue = fakeTasks([{ state: 'running' }, { state: 'running' }]);
  await assert.rejects(runTask(overdue, 'x', {}, { sleep, timeoutMs: 1000, now: () => (clock += 20000) }), error => error.uncertain === true);
  assert.deepEqual(overdue.calls.cancel, ['t1']);
});

test('runTask flags unknown outcomes as uncertain and keeps host error details', async () => {
  const noId = { start: async () => ({ state: 'running' }), get: async () => assert.fail(), cancel: async () => {} };
  await assert.rejects(runTask(noId, 'x', {}, { sleep }), error => error.uncertain === true);
  const lost = fakeTasks([{ state: 'running' }]);
  lost.get = async () => { throw new Error('port closed'); };
  await assert.rejects(runTask(lost, 'x', {}, { sleep }), error => error.uncertain === true && /port closed/.test(error.message));
  await assert.rejects(runTask(fakeTasks([{ state: 'failed', error: 'x', outcomeUnconfirmed: true }]), 'x', {}, { sleep }), error => error.uncertain === true);
  await assert.rejects(runTask(fakeTasks([{ state: 'weird' }]), 'x', {}, { sleep }), error => error.uncertain === true);
  await assert.rejects(runTask(fakeTasks([{ state: 'failed', error: 'denied', errorCode: 'E_AUTH' }]), 'x', {}, { sleep }), error => error.code === 'E_AUTH' && error.snapshot.state === 'failed' && !error.uncertain);
});

test('runTask reports every snapshot and stops when the page goes away mid-poll', async () => {
  const states = [];
  await runTask(fakeTasks([{ state: 'running', cancelRequested: true }, { state: 'succeeded', result: 1 }]), 'x', {}, { sleep, onSnapshot: s => states.push([s.state, s.cancelRequested === true]) });
  assert.deepEqual(states, [['running', true], ['succeeded', false]]);
  let alive = true;
  const tasks = fakeTasks([{ state: 'running' }, { state: 'running' }]);
  await assert.rejects(runTask(tasks, 'x', {}, { sleep: async () => { alive = false; }, alive: () => alive }), error => error.cancelled === true);
  assert.equal(tasks.calls.get, 0);
  assert.deepEqual(tasks.calls.cancel, ['t1']);
  let clock = 0;
  await assert.rejects(runTask(fakeTasks([{ state: 'running' }, { state: 'running' }]), 'x', {}, { sleep, deadlineMs: 5, now: () => (clock += 10) }), error => error.uncertain === true);
});

test('runTaskOrCall falls back to a direct call only without the task protocol', async () => {
  const called = [];
  const transport = { tasks: fakeTasks([], { startError: new Error('宿主不支持任务协议') }), call: async (action, payload) => { called.push(action); return payload; } };
  assert.deepEqual(await runTaskOrCall(transport, 'ask', { q: 1 }), { q: 1 });
  assert.deepEqual(called, ['ask']);
  const broken = { tasks: fakeTasks([], { startError: new Error('busy') }), call: async () => assert.fail('must not fall back') };
  await assert.rejects(runTaskOrCall(broken, 'ask', {}), /busy/);
  assert.equal(isTaskUnsupported(new Error('Host does not support the task protocol')), true);
});

test('runAIAction uses the AI task timeout', async () => {
  const tasks = fakeTasks([{ state: 'succeeded', result: 'ok' }]);
  assert.equal(await runAIAction({ tasks, call: async () => assert.fail() }, 'summarize', { text: 'x' }), 'ok');
  assert.equal(tasks.calls.start[0].timeoutMs, AI_TASK_TIMEOUT_MS);
});

test('clipUtf8 keeps whole characters within the byte budget', () => {
  assert.equal(clipUtf8('abc', 10), 'abc');
  assert.equal(clipUtf8('中文字', 7), '中文');
  assert.equal(clipUtf8('a😀b', 3), 'a');
  assert.ok(utf8Length(clipUtf8('😀'.repeat(50), 101)) <= 101);
});

function pushTasks({ finishAfterMs }) {
  const listeners = new Set();
  const calls = { get: 0 };
  return {
    calls,
    listeners,
    pushSupported: () => true,
    onChanged(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    async start() {
      setTimeout(() => { for (const fn of [...listeners]) fn({ taskId: 't1', state: 'succeeded', cancelRequested: false, result: 'done' }); }, finishAfterMs);
      return { taskId: 't1', state: 'running', cancelRequested: false };
    },
    async get() { calls.get++; return { taskId: 't1', state: 'running', cancelRequested: false }; },
    async cancel(id) { return { taskId: id, state: 'cancelled' }; },
  };
}

test('runTask finishes as soon as the host pushes the terminal snapshot', async () => {
  const tasks = pushTasks({ finishAfterMs: 30 });
  const started = Date.now();
  assert.equal(await runTask(tasks, 'job', {}), 'done');
  const elapsed = Date.now() - started;
  // A polling-only page would wait for its next poll (TASK_POLL_MS = 400 ms).
  assert.ok(elapsed < 200, `push latency ${elapsed} ms`);
  assert.equal(tasks.calls.get, 0, 'no poll needed once the push arrived');
  assert.equal(tasks.listeners.size, 0, 'listener removed after the task finished');
});

test('runTask keeps polling as a fallback and ignores pushes for other tasks', async () => {
  const listeners = new Set();
  let reads = 0;
  const tasks = {
    pushSupported: () => true,
    onChanged(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    async start() { return { taskId: 't1', state: 'running', cancelRequested: false }; },
    async get() {
      for (const fn of [...listeners]) fn({ taskId: 'other', state: 'succeeded', cancelRequested: false });
      return { taskId: 't1', state: ++reads < 2 ? 'running' : 'succeeded', cancelRequested: false, result: reads };
    },
    async cancel() {},
  };
  const waits = [];
  assert.equal(await runTask(tasks, 'job', {}, { sleep: async ms => { waits.push(ms); } }), 2);
  assert.deepEqual(waits, [TASK_PUSH_POLL_MS, TASK_PUSH_POLL_MS], 'pushing hosts poll every TASK_PUSH_POLL_MS');
  assert.equal(listeners.size, 0);
});

test('task pushes reach only the owning session page for tasks it started', () => {
  const snapshot = { taskId: 't1', state: 'succeeded', cancelRequested: false, result: 1 };
  const event = parsePluginUITaskChangedEvent({ pluginId: 'p', sessionId: 's1', generation: 2, taskId: 't1', snapshot });
  assert.ok(event);
  assert.equal(parsePluginUITaskChangedEvent({ pluginId: 'p', sessionId: 's1', generation: 2, taskId: 't1', snapshot: { ...snapshot, taskId: 't2' } }), undefined);
  assert.equal(parsePluginUITaskChangedEvent({ pluginId: 'p', sessionId: 's1', generation: 0, taskId: 't1', snapshot }), undefined);
  const target = { pluginId: 'p', sessionId: 's1', generation: 2 };
  const now = 1000;
  const owned = () => new Map([['t1', { terminal: false, expires: now + 10000 }]]);
  for (const other of [{ ...target, pluginId: 'q' }, { ...target, sessionId: 's2' }, { ...target, generation: 3 }]) {
    assert.equal(pluginUITaskPushMessage(event, other, owned(), now), undefined);
  }
  assert.equal(pluginUITaskPushMessage(event, target, new Map(), now), undefined, 'not started by this page');
  assert.equal(pluginUITaskPushMessage(event, target, new Map([['t1', { terminal: false, expires: now }]]), now), undefined, 'ownership expired');
  const tasks = owned();
  assert.deepEqual(pluginUITaskPushMessage(event, target, tasks, now), { type: 'mincode.host.task.changed', taskId: 't1', snapshot: event.snapshot });
  assert.equal(tasks.get('t1').terminal, true, 'terminal push frees the task slot like task.get');
});

test('task push parsing rejects other plugins and sessions before validating the snapshot', () => {
  const target = { pluginId: 'p', sessionId: 's1' };
  // An oversized snapshot for another session is never inspected.
  const foreign = { pluginId: 'p', sessionId: 's2', generation: 2, taskId: 't1', snapshot: { taskId: 't1', state: 'running', result: 'x'.repeat(4 * 1024 * 1024) } };
  assert.equal(parsePluginUITaskChangedEvent(foreign, target), undefined);
  assert.equal(parsePluginUITaskChangedEvent({ ...foreign, pluginId: 'q', sessionId: 's1' }, target), undefined);
  const own = { pluginId: 'p', sessionId: 's1', generation: 2, taskId: 't1', snapshot: { taskId: 't1', state: 'running', cancelRequested: false } };
  assert.equal(parsePluginUITaskChangedEvent(own, target)?.taskId, 't1');
});
