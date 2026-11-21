import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';

// JSON-RPC 2.0 conformance of the Node plugin runtime (packages/plugin-sdk/node).
const runtime = new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url).href;

async function exchange(lines, source = `start(() => ({ echo: async payload => payload }));`) {
  const child = spawn(process.execPath, ['--input-type=module', '-e', `import { start } from ${JSON.stringify(runtime)};\n${source}`]);
  let stdout = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
  child.stdin.end(lines.join('\n') + '\n');
  await once(child, 'close');
  clearTimeout(timer);
  return stdout.trim().split('\n').filter(Boolean).map(JSON.parse);
}

const init = JSON.stringify({ jsonrpc: '2.0', id: 0, method: 'plugin.initialize', params: {} });

test('unparsable input gets -32700 and invalid requests -32600, echoing a valid id; method-less messages are responses', async () => {
  const replies = await exchange([
    '{"jsonrpc":"2.0","id":1,"method":',
    '"hello"',
    '{"jsonrpc":"1.0","id":2,"method":"plugin.ping"}',
    '{"jsonrpc":"2.0","id":"three","method":5}',
    '{"jsonrpc":"2.0","id":{"x":1},"method":"plugin.ping"}',
    '{"jsonrpc":"2.0","id":6,"method":"plugin.ping","params":3}',
    '[]',
  ]);
  assert.deepEqual(replies.map(reply => [reply.id, reply.error?.code]), [
    [null, -32700], [null, -32600], [2, -32600], ['three', -32600], [null, -32600], [6, -32600], [null, -32600],
  ]);
  for (const reply of replies) assert.equal(reply.jsonrpc, '2.0');
});

test('a batch is answered with one array of its request replies; notifications and invalid entries follow §6', async () => {
  const replies = await exchange([
    init,
    JSON.stringify([
      { jsonrpc: '2.0', id: 'a', method: 'plugin.invoke', params: { action: 'echo', payload: '{"v":1}' } },
      { jsonrpc: '2.0', method: 'plugin.ping' },
      1,
      { jsonrpc: '2.0', id: 'b', method: 'plugin.nope' },
    ]),
    JSON.stringify([{ jsonrpc: '2.0', method: 'plugin.ping' }, { jsonrpc: '2.0', method: 'plugin.ping' }]),
  ]);
  assert.equal(replies.length, 2, 'initialize reply plus exactly one batch reply');
  const batch = replies[1];
  assert.ok(Array.isArray(batch));
  const byId = new Map(batch.map(reply => [reply.id, reply]));
  assert.equal(batch.length, 3);
  assert.deepEqual(byId.get('a').result, { v: 1 });
  assert.equal(byId.get('b').error.code, -32601);
  assert.equal(byId.get(null).error.code, -32600);
  for (const reply of batch) assert.equal(reply.jsonrpc, '2.0');
});

test('the fake host (node/testing.mjs) answers a batch from the plugin with one array', async () => {
  const { startPlugin, createFakeHost } = await import('../../packages/plugin-sdk/node/testing.mjs');
  const { mkdtemp, writeFile } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const dir = await mkdtemp(path.join(tmpdir(), 'jsonrpc-batch-'));
  const entry = path.join(dir, 'plugin.mjs');
  // A hand-written plugin: on invoke it sends the host a batch and returns the reply it got.
  await writeFile(entry, `
import readline from 'node:readline';
const send = message => process.stdout.write(JSON.stringify(message) + '\\n');
let invokeId;
readline.createInterface({ input: process.stdin }).on('line', line => {
  const message = JSON.parse(line);
  if (Array.isArray(message)) { send({ jsonrpc: '2.0', id: invokeId, result: message }); return; }
  if (message.method === 'plugin.initialize') send({ jsonrpc: '2.0', id: message.id, result: { runtimeProtocolVersion: 1 } });
  else if (message.method === 'plugin.invoke') {
    invokeId = message.id;
    send([
      { jsonrpc: '2.0', id: 'h1', method: 'host.storage.get', params: { key: 'a' } },
      { jsonrpc: '2.0', method: 'host.log', params: { level: 'info', message: 'x' } },
    ]);
  } else if (message.method === 'plugin.shutdown') { send({ jsonrpc: '2.0', id: message.id, result: {} }); process.exit(0); }
});
`);
  const plugin = await startPlugin(entry, { host: createFakeHost({ capabilities: ['storage'], storage: { a: 1 } }) });
  try {
    const replies = await plugin.invoke('go');
    assert.equal(replies.length, 1, 'one reply: the notification adds none');
    assert.equal(replies[0].id, 'h1');
    assert.equal(replies[0].jsonrpc, '2.0');
    assert.deepEqual(plugin.protocolViolations, []);
  } finally {
    await plugin.stop();
  }
});
