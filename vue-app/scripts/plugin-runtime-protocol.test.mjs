import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createFrameReader, encodeBinaryFrame } from '../../packages/plugin-sdk/node/runtime.mjs';

const runtime = new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url).href;

function spawnPlugin(source) {
  const child = spawn(process.execPath, ['--input-type=module', '-e', `import { start } from ${JSON.stringify(runtime)};\n${source}`]);
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
  child.on('close', () => clearTimeout(timer));
  return { child, stderr: () => stderr };
}

async function runJSONL(source, requests) {
  const { child, stderr } = spawnPlugin(source);
  let stdout = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stdin.end(requests.map(request => JSON.stringify({ jsonrpc: '2.0', ...request })).join('\n') + '\n');
  const [code] = await once(child, 'close');
  assert.equal(code, 0, stderr());
  return stdout.trim().split('\n').filter(Boolean).map(JSON.parse);
}

const jsonFrame = message => {
  const payload = Buffer.from(JSON.stringify({ jsonrpc: '2.0', ...message }));
  const header = Buffer.alloc(5);
  header.writeUInt32BE(payload.length, 1);
  return Buffer.concat([header, payload]);
};

test('initialize reports the declared plugin identity and plain results stay unchanged', async () => {
  const [identified] = await runJSONL(`start(() => ({}), { plugin: { id: 'vendor.example', version: '1.2.0' } });`,
    [{ id: 1, method: 'plugin.initialize', params: {} }]);
  assert.deepEqual(identified.result, { runtimeProtocolVersion: 1, plugin: { id: 'vendor.example', version: '1.2.0' } });
  const [plain] = await runJSONL(`start(() => ({}));`, [{ id: 1, method: 'plugin.initialize', params: {} }]);
  assert.deepEqual(plain.result, { runtimeProtocolVersion: 1 });
});

test('plugin.call routes to registered methods only', async () => {
  const messages = await runJSONL(`start(() => ({ read: () => 'action' }), { methods: {
    'files/list': async (params, context) => ({ params, hasHost: typeof context.host === 'function' }),
  } });`, [
    { id: 1, method: 'plugin.initialize', params: {} },
    { id: 2, method: 'plugin.call', params: { method: 'files/list', params: { prefix: 'a' } } },
    { id: 3, method: 'plugin.call', params: { method: 'files/list' } },
    { id: 4, method: 'plugin.call', params: { method: 'read' } },
    { id: 5, method: 'plugin.call', params: { method: 'toString' } },
    { id: 6, method: 'plugin.call', params: {} },
  ]);
  const byId = Object.fromEntries(messages.map(message => [message.id, message]));
  assert.deepEqual(byId[2].result, { params: { prefix: 'a' }, hasHost: true });
  assert.deepEqual(byId[3].result, { params: null, hasHost: true });
  assert.equal(byId[4].error.code, -32601, 'actions are not callable as methods');
  assert.equal(byId[5].error.code, -32601, 'inherited properties are not methods');
  assert.equal(byId[6].error.code, -32602);
});

test('framed transport carries JSON-RPC and binary frames in both directions', async () => {
  const { child, stderr } = spawnPlugin(`start(() => ({}), {
    transport: 'framed',
    plugin: { id: 'vendor.framed' },
    onBinary: (channel, data, { sendBinary }) => sendBinary('echo/' + channel, Buffer.concat([Buffer.from('echo:'), data])),
    methods: { ping: (params, { sendBinary }) => { sendBinary('push', new Uint8Array([7, 8])); return 'pong'; } },
  });`);
  const frames = [];
  const waiters = [];
  child.stdout.on('data', createFrameReader((kind, payload) => {
    frames.push({ kind, payload: Buffer.from(payload) });
    waiters.splice(0).forEach(wake => wake());
  }, error => assert.fail(error)));
  const nextFrame = async () => {
    while (!frames.length) await new Promise(resolve => waiters.push(resolve));
    return frames.shift();
  };
  const binaryOf = frame => {
    const length = frame.payload.readUInt16BE(0);
    return { channel: frame.payload.subarray(2, 2 + length).toString(), data: frame.payload.subarray(2 + length) };
  };

  child.stdin.write(jsonFrame({ id: 1, method: 'plugin.initialize', params: {} }));
  const initialized = await nextFrame();
  assert.equal(initialized.kind, 0);
  assert.deepEqual(JSON.parse(initialized.payload).result.plugin, { id: 'vendor.framed' });

  child.stdin.write(encodeBinaryFrame('pty/1', Buffer.from([0, 10, 255])));
  const echo = await nextFrame();
  assert.equal(echo.kind, 1);
  assert.deepEqual(binaryOf(echo), { channel: 'echo/pty/1', data: Buffer.from([...Buffer.from('echo:'), 0, 10, 255]) });

  child.stdin.write(jsonFrame({ id: 2, method: 'plugin.call', params: { method: 'ping' } }));
  const pushed = await nextFrame();
  assert.deepEqual(binaryOf(pushed), { channel: 'push', data: Buffer.from([7, 8]) });
  const reply = await nextFrame();
  assert.equal(JSON.parse(reply.payload).result, 'pong');

  child.stdin.end();
  const [code] = await once(child, 'close');
  assert.equal(code, 0, stderr());
});

test('frame codec rejects invalid channels, oversized payloads and corrupt frames', () => {
  assert.throws(() => encodeBinaryFrame('../x y', Buffer.alloc(0)), /channel/);
  assert.throws(() => encodeBinaryFrame('ok', Buffer.alloc(8 * 1024 * 1024 + 1)), /exceeds/);
  const errors = [];
  const reader = createFrameReader(() => assert.fail('no frame expected'), error => errors.push(error.message));
  reader(Buffer.from([9, 0, 0, 0, 1, 0]));
  assert.equal(errors.length, 1);

  const received = [];
  const split = createFrameReader((kind, payload) => received.push([kind, payload.toString()]), error => assert.fail(error));
  const frame = jsonFrame({ id: 1, result: true });
  split(frame.subarray(0, 3));
  split(frame.subarray(3));
  assert.deepEqual(received, [[0, '{"jsonrpc":"2.0","id":1,"result":true}']]);
});
