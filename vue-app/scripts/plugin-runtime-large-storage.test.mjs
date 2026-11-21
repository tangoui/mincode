import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { LARGE_STORAGE_MESSAGE_BYTES, createFrameReader } from '../../packages/plugin-sdk/node/runtime.mjs';

const runtime = new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url).href;

// The action reads a stored string and writes it back doubled.
function spawnPlugin(options) {
  const source = `import { start } from ${JSON.stringify(runtime)};
start(host => ({ grow: async () => {
  const value = await host('host.storage.get', { key: 'k' });
  await host('host.storage.set', { key: 'k', value: value + value });
  return value.length;
} }), ${options});`;
  const child = spawn(process.execPath, ['--input-type=module', '-e', source]);
  const timer = setTimeout(() => child.kill('SIGKILL'), 10_000);
  child.on('close', () => clearTimeout(timer));
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity })[Symbol.asyncIterator]();
  const send = message => child.stdin.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n');
  const next = async () => JSON.parse((await lines.next()).value);
  return { child, send, next };
}

async function grow(options, offer, size = 1500 * 1024) {
  const plugin = spawnPlugin(options);
  plugin.send({ id: 1, method: 'plugin.initialize', params: { features: offer } });
  const initialized = await plugin.next();
  plugin.send({ id: 2, method: 'plugin.invoke', params: { action: 'grow', payload: '{}' } });
  const get = await plugin.next();
  assert.equal(get.method, 'host.storage.get');
  plugin.send({ id: get.id, result: 'x'.repeat(size) });
  let message = await plugin.next();
  let stored;
  if (message.method === 'host.storage.set') {
    stored = message.params.value.length;
    plugin.send({ id: message.id, result: true });
    message = await plugin.next();
  }
  plugin.child.stdin.end();
  return { initialized, stored, reply: message };
}

test('largeStorage lets a plugin write a value above 1 MiB once both sides agree', async () => {
  const { initialized, stored, reply } = await grow('{ largeStorage: true }', { largeStorage: 1 });
  assert.deepEqual(initialized.result.features, { largeStorage: 1 });
  assert.equal(stored, 3000 * 1024);
  assert.equal(reply.id, 2);
  assert.equal(reply.result.result ?? reply.result, 1500 * 1024);
});

test('without the opt-in or the offer a large host.storage.set is refused', async () => {
  // The stored value fits in one message; doubled it does not.
  for (const [options, offer] of [['{}', { largeStorage: 1 }], ['{ largeStorage: true }', {}]]) {
    const { initialized, stored, reply } = await grow(options, offer, 600 * 1024);
    assert.equal(initialized.result.features, undefined);
    assert.equal(stored, undefined, 'the oversized request was sent');
    assert.equal(reply.id, 2);
    assert.ok(reply.error, 'the action must fail');
  }
});

test('the frame reader applies a raised JSON limit', () => {
  const payload = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id: 1, result: 'x'.repeat(2 << 20) }));
  const frame = Buffer.concat([Buffer.from([0, ...new Uint8Array(new Uint32Array([0]).buffer)]), payload]);
  frame.writeUInt32BE(payload.length, 1);
  const errors = [];
  createFrameReader(() => assert.fail('frame above 1 MiB accepted'), error => errors.push(error))(frame);
  assert.equal(errors.length, 1);
  let received = 0;
  createFrameReader((kind, data) => { received = data.length; }, error => assert.fail(error), { maxJsonBytes: () => LARGE_STORAGE_MESSAGE_BYTES })(frame);
  assert.equal(received, payload.length);
});
