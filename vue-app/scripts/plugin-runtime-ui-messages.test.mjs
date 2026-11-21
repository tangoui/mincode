import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';

const runtime = new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url).href;

async function runJSONL(source, requests) {
  const child = spawn(process.execPath, ['--input-type=module', '-e', `import { start } from ${JSON.stringify(runtime)};\n${source}`]);
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stderr.on('data', chunk => { stderr += chunk; });
  const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
  child.stdin.write(requests.map(request => JSON.stringify({ jsonrpc: '2.0', ...request })).join('\n') + '\n');
  // Let queued view messages run before stdin closes.
  setTimeout(() => child.stdin.end(), 300);
  const [code] = await once(child, 'close');
  clearTimeout(timer);
  assert.equal(code, 0, stderr);
  return { messages: stdout.trim().split('\n').filter(Boolean).map(JSON.parse), stderr };
}

test('view messages reach onUIMessage in order and postUIMessage notifies the views', async () => {
  const { messages } = await runJSONL(`const runtime = start(() => ({}), {
    onUIMessage: async (message, { viewId, host, postUIMessage }) => {
      if (typeof host !== 'function') throw new Error('host missing');
      if (message.seq === 1) await new Promise(resolve => setTimeout(resolve, 30));
      postUIMessage({ echo: message.seq }, { viewId });
    },
  });
  if (typeof runtime.postUIMessage !== 'function') throw new Error('postUIMessage not returned');`, [
    { id: 1, method: 'plugin.initialize', params: {} },
    { id: 2, method: 'plugin.ui.message', params: { viewId: 'main', message: { seq: 1 } } },
    { id: 3, method: 'plugin.ui.message', params: { viewId: 'main', message: { seq: 2 } } },
    { id: 4, method: 'plugin.ui.message', params: { viewId: 'main' } },
  ]);
  const byId = Object.fromEntries(messages.filter(message => message.id !== undefined).map(message => [message.id, message]));
  assert.deepEqual(byId[2].result, {});
  assert.deepEqual(byId[3].result, {});
  assert.equal(byId[4].error.code, -32602);
  const posted = messages.filter(message => message.method === 'host.ui.postMessage');
  assert.deepEqual(posted.map(message => message.params), [
    { message: { echo: 1 }, viewId: 'main' },
    { message: { echo: 2 }, viewId: 'main' },
  ], 'a slow handler does not let later messages overtake it');
  assert.ok(posted.every(message => message.id === undefined), 'posted messages are notifications');
});

test('view messages without onUIMessage are unknown, and postUIMessage validates', async () => {
  const { messages } = await runJSONL(`const { postUIMessage } = start(() => ({}));
  const errors = [];
  for (const [message, options] of [[undefined], [null], [() => 1], ['x'.repeat(256 * 1024)], [1, { viewId: '../x' }]]) {
    try { postUIMessage(message, options); errors.push('accepted'); } catch (error) { errors.push(error.code); }
  }
  postUIMessage('broadcast');
  process.stderr.write('');
  start.errors = errors;
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', method: 'test.errors', params: errors }) + '\\n');`, [
    { id: 1, method: 'plugin.initialize', params: {} },
    { id: 2, method: 'plugin.ui.message', params: { viewId: 'main', message: 1 } },
  ]);
  assert.equal(messages.find(message => message.id === 2).error.code, -32601);
  const errors = messages.find(message => message.method === 'test.errors').params;
  assert.deepEqual(errors, ['INVALID_UI_MESSAGE', 'INVALID_UI_MESSAGE', 'INVALID_UI_MESSAGE', 'UI_MESSAGE_TOO_LARGE', 'INVALID_UI_MESSAGE']);
  assert.deepEqual(messages.find(message => message.method === 'host.ui.postMessage').params, { message: 'broadcast' });
});
