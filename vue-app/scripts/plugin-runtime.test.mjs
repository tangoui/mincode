import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { access, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { buildPluginRuntime } from './build-plugin-runtime.mjs';
import { HOST_EVENTS, PluginError, createAIAPI, createClipboardAPI, createConfigurationAPI, createDecorationsAPI, createEventsAPI, createHostAPI, createJdtlsAPI, createNotificationsAPI, createSecretsAPI, createShellAPI, createStatusBarAPI, createStorageAPI, createStorageFilesAPI, createWorkspaceAPI, quoteShellArgument, start } from '../../packages/plugin-sdk/node/runtime.mjs';

const runtime = new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url).href;

// endWhen closes stdin once the parsed stdout satisfies it, so tests that wait on
// plugin-side timers do not race a fixed EOF delay against process startup.
async function run(source, requests, { eofDelay = 0, endWhen, replyToHost } = {}) {
  const child = spawn(process.execPath, ['--input-type=module', '-e', `import { start } from ${JSON.stringify(runtime)};\n${source}`]);
  let stdout = '', stderr = '';
  const parsed = () => stdout.split('\n').filter(line => line.trim()).map(JSON.parse);
  let handled = 0;
  child.stdout.on('data', chunk => {
    stdout += chunk;
    if (replyToHost && stdout.endsWith('\n') && !child.stdin.writableEnded) {
      const messages = parsed();
      for (; handled < messages.length; handled++) {
        const message = messages[handled];
        if (message.method?.startsWith('host.')) {
          child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: message.id, result: replyToHost(message) }) + '\n');
        }
      }
    }
    if (endWhen && stdout.endsWith('\n') && !child.stdin.writableEnded && endWhen(parsed())) child.stdin.end();
  });
  child.stderr.on('data', chunk => { stderr += chunk; });
  const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
  let eofTimer;
  try {
    child.stdin.write(requests.map(request => JSON.stringify({ jsonrpc: '2.0', ...request })).join('\n') + '\n');
    if (endWhen) { /* stdin ends from the stdout handler */ }
    else if (eofDelay) eofTimer = setTimeout(() => child.stdin.end(), eofDelay);
    else child.stdin.end();
    const [code] = await once(child, 'close');
    assert.equal(code, 0, stderr);
    return { messages: stdout.trim().split('\n').filter(Boolean).map(JSON.parse), stderr };
  } finally {
    clearTimeout(timer);
    clearTimeout(eofTimer);
    if (child.exitCode === null) child.kill('SIGKILL');
  }
}

test('storage API validates keys, serialization, and local size limits', async () => {
  const calls = [];
  const storage = createStorageAPI(async (method, params) => { calls.push({ method, params }); return method === 'host.storage.list' ? ['one'] : true; }, { maxValueBytes: 16 });
  await storage.set('safe:key', { ok: true });
  assert.deepEqual(await storage.keys(), ['one']);
  assert.deepEqual(calls, [
    { method: 'host.storage.set', params: { key: 'safe:key', value: { ok: true } } },
    { method: 'host.storage.list', params: {} },
  ]);
  assert.throws(() => storage.get('../unsafe'), error => error instanceof PluginError && error.code === 'INVALID_ARGUMENT');
  await assert.rejects(storage.set('safe', { value: 'too large' }), error => error.code === 'PAYLOAD_TOO_LARGE');
  const cycle = {}; cycle.self = cycle;
  await assert.rejects(storage.set('safe', cycle), error => error.code === 'INVALID_ARGUMENT');
});

test('storage files API chunks writes, reads bounded frames, and validates paths/results', async () => {
  const calls = [];
  const host = async (method, params) => {
    calls.push({ method, params });
    if (method === 'host.storage.file.read') {
      const bytes = Buffer.from(params.offset === 0 ? 'abc' : 'de').toString('base64');
      return { data: bytes, offset: params.offset, nextOffset: params.offset + (params.offset === 0 ? 3 : 2), eof: params.offset !== 0 };
    }
    return true;
  };
  const files = createStorageAPI(host, { maxChunkBytes: 3, maxFileBytes: 8 }).files;
  assert.equal(typeof createStorageFilesAPI(host).read, 'function');
  assert.deepEqual(await files.read('dir/file.txt'), Buffer.from('abcde'));
  assert.deepEqual(await files.write('dir/file.txt', Buffer.from('abcdef')), { bytesWritten: 6, offset: 6 });
  assert.deepEqual(calls.slice(0, 4), [
    { method: 'host.storage.file.read', params: { path: 'dir/file.txt', offset: 0, maxBytes: 3 } },
    { method: 'host.storage.file.read', params: { path: 'dir/file.txt', offset: 3, maxBytes: 3 } },
    { method: 'host.storage.file.write', params: { path: 'dir/file.txt', offset: 0, data: 'YWJj', truncate: true } },
    { method: 'host.storage.file.write', params: { path: 'dir/file.txt', offset: 3, data: 'ZGVm', truncate: false } },
  ]);
  for (const path of ['../escape', '/absolute', 'C:\\windows', 'a/./b', 'a//b', 'a\0b']) {
    assert.throws(() => files.stat(path), error => error.code === 'INVALID_ARGUMENT');
  }
  await files.list('');
  assert.deepEqual(calls.at(-1), { method: 'host.storage.file.list', params: { path: '' } });
  await assert.rejects(files.read('file', { maxBytes: 4 }), error => error.code === 'INVALID_HOST_RESULT');
  await assert.rejects(files.write('file', '123456789'), error => error.code === 'PAYLOAD_TOO_LARGE');
  const bad = createStorageFilesAPI(async () => ({ data: '!!!', offset: 0, eof: true }));
  await assert.rejects(bad.read('file'), error => error.code === 'INVALID_HOST_RESULT');
  const unconfirmed = createStorageFilesAPI(async () => false);
  await assert.rejects(unconfirmed.write('file', 'abc'), error => error.code === 'INVALID_HOST_RESULT');
});

test('storage files distinguish missing reads from empty files and reject disappearance mid-read', async () => {
  const missing = createStorageFilesAPI(async () => null);
  assert.equal(await missing.read('missing'), null);
  await assert.rejects(missing.read('missing', { onMissing: 'error' }), error => error.code === 'FILE_NOT_FOUND');
  const explicit = createStorageFilesAPI(async () => ({ found: false }));
  assert.equal(await explicit.read('missing'), null);
  const empty = createStorageFilesAPI(async () => ({ data: '', offset: 0, eof: true }));
  assert.deepEqual(await empty.read('empty'), Buffer.alloc(0));
  const vanished = createStorageFilesAPI(async (_, { offset }) => offset ? null : { data: 'YQ==', offset: 0, eof: false }, { maxChunkBytes: 1 });
  await assert.rejects(vanished.read('file'), error => error.code === 'INVALID_HOST_RESULT');
});

function stagedStorageHost({ staged = true, failAppend = 0 } = {}) {
  const calls = [];
  const files = new Map();
  const sessions = new Map();
  let appends = 0;
  const host = async (method, params) => {
    calls.push(method);
    switch (method) {
      case 'host.api.describe':
        return { methods: staged ? [{ name: 'host.storage.file.write.begin', available: true }] : [] };
      case 'host.storage.file.write.begin':
        sessions.set('a'.repeat(32), { path: params.path, chunks: [], size: 0 });
        return { session: 'a'.repeat(32), maxChunkBytes: 512 * 1024, idleTimeoutMs: 60000 };
      case 'host.storage.file.write.append': {
        const session = sessions.get(params.session);
        if (++appends === failAppend) throw new PluginError('STORAGE', 'quota');
        assert.equal(params.offset, session.size);
        const chunk = Buffer.from(params.data, 'base64');
        assert.ok(chunk.length <= 512 * 1024);
        session.chunks.push(chunk); session.size += chunk.length;
        return { size: session.size };
      }
      case 'host.storage.file.write.commit': {
        const session = sessions.get(params.session);
        sessions.delete(params.session);
        assert.equal(params.size, session.size);
        files.set(session.path, Buffer.concat(session.chunks));
        return true;
      }
      case 'host.storage.file.write.abort':
        sessions.delete(params.session);
        return true;
      case 'host.storage.file.write': {
        const current = params.truncate ? Buffer.alloc(0) : files.get(params.path) ?? Buffer.alloc(0);
        files.set(params.path, Buffer.concat([current.subarray(0, params.offset), Buffer.from(params.data, 'base64')]));
        return true;
      }
      case 'host.storage.file.read': {
        const data = files.get(params.path);
        if (!data) return null;
        const end = Math.min(params.offset + params.maxBytes, data.length);
        return { data: data.subarray(params.offset, end).toString('base64'), offset: params.offset, nextOffset: end, eof: end === data.length };
      }
      default:
        throw new Error(`unexpected ${method}`);
    }
  };
  return { host, calls, files, sessions };
}

test('storage files use staged writes and 512 KiB chunks when the host supports them', async () => {
  const { host, calls, files } = stagedStorageHost();
  const api = createStorageFilesAPI(host);
  const payload = Buffer.alloc(1_300_000, 7);
  assert.deepEqual(await api.write('big.bin', payload), { bytesWritten: payload.length, offset: payload.length });
  assert.deepEqual(calls, ['host.api.describe', 'host.storage.file.write.begin', 'host.storage.file.write.append', 'host.storage.file.write.append', 'host.storage.file.write.append', 'host.storage.file.write.commit']);
  assert.ok(files.get('big.bin').equals(payload));
  calls.length = 0;
  assert.ok((await api.read('big.bin')).equals(payload));
  // A 64 KiB probe frame, then 512 KiB frames; the feature probe is cached.
  assert.deepEqual(calls, ['host.storage.file.read', 'host.storage.file.read', 'host.storage.file.read', 'host.storage.file.read']);
  calls.length = 0;
  await api.write('small.txt', 'tiny');
  await api.write('patch.bin', payload.subarray(0, 70_000), { offset: 0, truncate: false });
  assert.deepEqual(calls, ['host.storage.file.write', 'host.storage.file.write', 'host.storage.file.write']);
  assert.deepEqual(await api.writeAtomic('atomic.bin', payload), { bytesWritten: payload.length, offset: payload.length });
  assert.ok(files.get('atomic.bin').equals(payload));
});

test('staged writes abort on failure and writeAtomic refuses old hosts', async () => {
  const failing = stagedStorageHost({ failAppend: 2 });
  failing.files.set('big.bin', Buffer.from('old'));
  await assert.rejects(createStorageFilesAPI(failing.host).writeAtomic('big.bin', Buffer.alloc(1_300_000)), error => error.code === 'STORAGE');
  assert.equal(failing.calls.at(-1), 'host.storage.file.write.abort');
  assert.equal(failing.sessions.size, 0);
  assert.equal(failing.files.get('big.bin').toString(), 'old');

  const old = stagedStorageHost({ staged: false });
  const api = createStorageFilesAPI(old.host);
  await assert.rejects(api.writeAtomic('big.bin', Buffer.alloc(100_000)), error => error.code === 'UNSUPPORTED');
  assert.equal(old.files.has('big.bin'), false);
  await api.writeAtomic('small.bin', 'fits in one call');
  assert.equal(old.files.get('small.bin').toString(), 'fits in one call');
  // Plain writes fall back to sequential 64 KiB chunks.
  old.calls.length = 0;
  await api.write('big.bin', Buffer.alloc(100_000, 1));
  assert.deepEqual(old.calls, ['host.storage.file.write', 'host.storage.file.write']);
  assert.equal(old.files.get('big.bin').length, 100_000);
  // A host without host.api.describe counts as an old host.
  const bare = createStorageFilesAPI(async method => { if (method === 'host.api.describe') throw new Error('host method not found'); return true; });
  await assert.rejects(bare.writeAtomic('x', Buffer.alloc(70_000)), error => error.code === 'UNSUPPORTED');
  assert.throws(() => createStorageFilesAPI(old.host, { maxChunkBytes: 512 * 1024 + 1 }), TypeError);
});

test('shell API quotes arguments, validates host results, and hides stderr', async () => {
  assert.equal(quoteShellArgument("a'b"), "'a'\\''b'");
  const shell = createShellAPI(async () => ({ success: true, stdout: 'ok', stderr: '', exit_code: 0 }));
  assert.equal(await shell.exec(['tool', 'argument']), 'ok');
  const missing = createShellAPI(async () => ({ success: false, stdout: '', stderr: 'secret path', exit_code: 127 }));
  await assert.rejects(missing.exec(['tool']), error => error.code === 'TOOL_NOT_FOUND' && !error.message.includes('secret path'));
  const invalid = createShellAPI(async () => ({ success: true, stdout: 'ok' }));
  await assert.rejects(invalid.run('tool'), error => error.code === 'INVALID_HOST_RESULT');
  const oversized = createShellAPI(async () => ({ success: true, stdout: '12345', stderr: '', exit_code: 0 }), { maxOutputBytes: 4 });
  await assert.rejects(oversized.run('tool'), error => error.code === 'RESULT_TOO_LARGE');
  const oversizedStderr = createShellAPI(async () => ({ success: false, stdout: '', stderr: '12345', exit_code: 1 }), { maxOutputBytes: 4 });
  await assert.rejects(oversizedStderr.run('tool'), error => error.code === 'RESULT_TOO_LARGE');
  const timedOut = createShellAPI(async () => { throw new PluginError('HOST_TIMEOUT', 'private timeout detail'); });
  await assert.rejects(timedOut.run('tool'), error => error.code === 'TIMEOUT' && error.retryable && !error.message.includes('private timeout detail'));
  const denied = createShellAPI(async () => { throw Object.assign(new Error('private permission detail'), { code: -32001 }); });
  await assert.rejects(denied.run('tool'), error => error.code === 'PERMISSION_DENIED' && !error.retryable && !error.message.includes('private permission detail'));
});

test('structured action and host callback errors preserve safe metadata', async () => {
  const action = await run(`import { pluginError } from ${JSON.stringify(runtime)}; start(() => ({ fail: () => { throw pluginError('BROKEN', 'Safe failure', { data: { kind: 'business', tool: 'runner', secret: 'hidden' }, retryable: true }); } }));`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'fail' } },
  ]);
  assert.deepEqual(action.messages[0].error, { code: -32000, message: 'Safe failure', data: { kind: 'business', tool: 'runner', retryable: true } });
  const callback = await run(`start(host => ({ inspect: async () => { try { await host('host.test', {}); } catch (error) { return { code: error.code, data: error.data }; } } }));`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'inspect' } },
    { id: 'host-1', error: { code: -32001, message: 'denied', data: { kind: 'permission', retryable: false } } },
  ], { eofDelay: 100 });
  assert.deepEqual(callback.messages.find(message => message.id === 1).result, { code: -32001, data: { kind: 'permission', retryable: false } });
});

test('configuration API reads host values and validates keys/results', async () => {
  const calls = [];
  const api = createConfigurationAPI(async (method, params) => { calls.push([method, params]); return { values: { endpoint: 'https://example.test', token: 's3cret' } }; });
  assert.deepEqual(await api.get(), { endpoint: 'https://example.test', token: 's3cret' });
  await api.get(['endpoint']);
  assert.deepEqual(calls, [['host.configuration.get', {}], ['host.configuration.get', { keys: ['endpoint'] }]]);
  await assert.rejects(api.get('endpoint'), { code: 'INVALID_ARGUMENT' });
  await assert.rejects(createConfigurationAPI(async () => ({})).get(), { code: 'INVALID_HOST_RESULT' });
});

test('configuration change notifications require a handler and negotiation', async () => {
  const withHandler = await run(`start(() => ({}), { onConfigurationChanged: keys => process.stderr.write('changed:' + keys.join(',') + '\\n') });`, [
    { id: 0, method: 'plugin.configuration.changed', params: { keys: ['early'] } },
    { id: 1, method: 'plugin.initialize', params: { features: { configuration: 1 } } },
    { id: 2, method: 'plugin.configuration.changed', params: { keys: ['endpoint', 'token'] } },
  ], { eofDelay: 100 });
  assert.equal(withHandler.messages.find(m => m.id === 0).error.code, -32601);
  assert.deepEqual(withHandler.messages.find(m => m.id === 1).result, { runtimeProtocolVersion: 1, features: { configuration: 1 } });
  assert.deepEqual(withHandler.messages.find(m => m.id === 2).result, {});
  assert.match(withHandler.stderr, /changed:endpoint,token/);
  const legacy = await run(`start(() => ({}));`, [
    { id: 1, method: 'plugin.initialize', params: { features: { configuration: 1 } } },
    { id: 2, method: 'plugin.configuration.changed', params: { keys: [] } },
  ]);
  assert.deepEqual(legacy.messages.find(m => m.id === 1).result, { runtimeProtocolVersion: 1 });
  assert.equal(legacy.messages.find(m => m.id === 2).error.code, -32601);
});

test('tasks require negotiation and preserve short invoke', async () => {
  const { messages } = await run(`start(() => ({ echo: p => p }));`, [
    { id: 1, method: 'plugin.task.start', params: { taskId: 'one', action: 'echo' } },
    { id: 2, method: 'plugin.invoke', params: { action: 'echo', payload: '{}' } },
  ]);
  assert.equal(messages.find(m => m.id === 1).error.code, -32601);
  assert.deepEqual(messages.find(m => m.id === 2).result, {});
});

test('task handshake, progress and cooperative cancellation', async () => {
  const { messages } = await run(`start(() => ({ work: async (p, ctx) => {
    ctx.reportProgress({ step: 1 });
    await new Promise(resolve => setTimeout(resolve, 30));
    ctx.signal.throwIfAborted();
  } }));`, [
    { id: 1, method: 'plugin.initialize', params: { features: { tasks: 1 } } },
    { id: 2, method: 'plugin.task.start', params: { taskId: 'one', action: 'work' } },
    { id: 3, method: 'plugin.task.cancel', params: { taskId: 'one' } },
  ], { eofDelay: 200 });
  assert.equal(messages.find(m => m.id === 1).result.features.tasks, 1);
  assert.equal(messages.find(m => m.id === 2).result.state, 'running');
  assert.equal(messages.find(m => m.id === 3).result.cancelRequested, true);
  assert.equal(messages.find(m => m.id === 3).result.state, 'running');
});

for (const failure of ['size', 'write']) {
  test(`failed task acknowledgement rolls back task, timer and busy gate (${failure})`, async () => {
    const taskId = 't'.repeat(64);
    const { messages, stderr } = await run(`
      ${failure === 'write' ? `const write = process.stdout.write.bind(process.stdout);
      process.stdout.write = text => {
        if (JSON.parse(text).id === 2 && JSON.parse(text).result) throw new Error('ack write failed');
        return write(text);
      };` : ''}
      start(() => ({ work: () => { process.stderr.write('started\\n'); }, inspect: () => ({
        timers: process.getActiveResourcesInfo().filter(name => name === 'Timeout').length,
      }) }), { maxResultBytes: ${failure === 'size' ? 140 : 900000}, oversizedMessage: 'ack too large' });`, [
      { id: 1, method: 'plugin.initialize', params: { features: { tasks: 1 } } },
      { id: 2, method: 'plugin.task.start', params: { taskId, action: 'work' } },
      { id: 3, method: 'plugin.task.status', params: { taskId } },
      { id: 4, method: 'plugin.invoke', params: { action: 'inspect' } },
    ], { eofDelay: 100 });
    assert.match(messages.find(m => m.id === 2).error.message, /ack/);
    assert.equal(messages.find(m => m.id === 3).error.code, -32602);
    assert.deepEqual(messages.find(m => m.id === 4).result, { timers: 0 });
    assert.equal(stderr, '', 'unacknowledged action must not run');
  });
}

test('runtime generation is identical, checked, and rejects drift', async () => {
  const outputPackages = await mkdtemp(resolve(tmpdir(), 'mincode-runtime-test-'));
  try {
    const outputTemplates = resolve(outputPackages, 'templates');
    const options = { outputPackages, outputTemplates };
    const official = new URL('../../official-plugins/', import.meta.url);
    const expectedNames = [];
    for (const entry of await readdir(official, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      const content = await readFile(new URL(`${entry.name}/plugin.json`, official), 'utf8').catch(error => {
        // 与生成器一致，忽略仅剩本地构建产物的已移除包目录。
        if (error.code === 'ENOENT') return null;
        throw error;
      });
      if (content === null) continue;
      const manifest = JSON.parse(content);
      if (manifest.runtime?.type === 'process' && manifest.runtime.entry?.endsWith('.mjs')) expectedNames.push(entry.name);
    }
    const templates = ['contributions-process-showcase', 'host-api-showcase'];
    const unmanaged = resolve(outputPackages, 'third-party/lib');
    await mkdir(unmanaged, { recursive: true });
    await writeFile(resolve(unmanaged, 'runtime.mjs'), 'third-party runtime');
    const goRuntime = resolve(outputPackages, 'etcd-manager/lib');
    await mkdir(goRuntime, { recursive: true });
    await writeFile(resolve(goRuntime, 'runtime.mjs'), 'untouched legacy file');
    assert.deepEqual(await buildPluginRuntime(['etcd-manager'], options), []);
    const names = await buildPluginRuntime([], options);
    assert.equal(await readFile(resolve(goRuntime, 'runtime.mjs'), 'utf8'), 'untouched legacy file');
    assert.deepEqual(names, [...expectedNames.sort(), ...templates]);
    const expected = await readFile(new URL(runtime));
    for (const name of names) {
      const root = templates.includes(name) ? outputTemplates : outputPackages;
      assert.deepEqual(await readFile(resolve(root, name, 'lib/runtime.mjs')), expected);
    }
    await buildPluginRuntime([], { ...options, check: true });
    assert.equal(await readFile(resolve(unmanaged, 'runtime.mjs'), 'utf8'), 'third-party runtime');
    // No official package uses the Node runtime any more; the templates still cover drift.
    for (const name of [...expectedNames.slice(0, 1), ...templates]) {
      const root = templates.includes(name) ? outputTemplates : outputPackages;
      await writeFile(resolve(root, name, 'lib/runtime.mjs'), 'drift');
      await assert.rejects(buildPluginRuntime([], { ...options, check: true }), /Stale runtime/);
      assert.deepEqual(await buildPluginRuntime([name, name], options), [name]);
    }
    await rm(resolve(outputTemplates, templates[0], 'lib/runtime.mjs'));
    await assert.rejects(buildPluginRuntime([], { ...options, check: true }), /Stale runtime/);
    await buildPluginRuntime([templates[0]], options);
    await assert.rejects(buildPluginRuntime([expectedNames[0] ?? templates[0], '../escape'], options), /Unknown process plugin/);
    await assert.rejects(buildPluginRuntime(['third-party'], options), /Unknown process plugin/);
    const selected = resolve(outputPackages, 'selected');
    await buildPluginRuntime([expectedNames[0] ?? 'etcd-manager'], { outputPackages: selected, outputTemplates: resolve(selected, 'templates') });
    await assert.rejects(access(resolve(selected, 'templates')));
  } finally {
    await rm(outputPackages, { recursive: true, force: true });
  }
});

test('legacy adapter and result limit remain configurable', async () => {
  const { messages } = await run(`start(api => ({ value: () => api.marker, big: () => 'x'.repeat(500) }), {
    adaptHost: () => ({ marker: 'adapted' }), maxResultBytes: 200, oversizedMessage: 'too large', cleanup: false,
  });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'value', payload: '{}' } },
  ]);
  assert.equal(messages[0].result, 'adapted');
  const result = await run(`start(() => ({ big: () => 'x'.repeat(500) }), { maxResultBytes: 200, oversizedMessage: 'too large' });`, [
    { id: 2, method: 'plugin.invoke', params: { action: 'big' } },
  ]);
  assert.equal(result.messages[0].error.message, 'too large');
});

test('result formatting and null payload opt-in preserve official defaults', async () => {
  const requests = [{ id: 1, method: 'plugin.invoke', params: { action: 'echo', payload: 'null' } }];
  const defaults = await run(`start(() => ({ echo: payload => payload }));`, requests);
  assert.match(defaults.messages[0].error.message, /参数必须是对象/);
  const wrapped = await run(`start(() => ({ echo: payload => payload }), {
    allowNullPayload: true, formatResult: result => ({ result }),
  });`, requests);
  assert.deepEqual(wrapped.messages[0].result, { result: {} });
});

test('strict wire payloads and malformed parsed payloads return invalid-params errors', async () => {
  for (const payload of [{}, [], null, false, 42, '{', '[]', 'false', '42', '"text"']) {
    const { messages } = await run(`start(() => ({ echo: payload => payload }), {
      strictPayloadString: true, allowNullPayload: true,
    });`, [{ id: 1, method: 'plugin.invoke', params: { action: 'echo', payload } }]);
    assert.equal(messages[0].error?.code, -32602, JSON.stringify(payload));
  }
  for (const payload of [undefined, '', '{}', 'null']) {
    const { messages } = await run(`start(() => ({ echo: payload => payload }), {
      strictPayloadString: true, allowNullPayload: true,
    });`, [{ id: 1, method: 'plugin.invoke', params: { action: 'echo', payload } }]);
    assert.deepEqual(messages[0].result, {});
  }
  const legacy = await run(`start(() => ({ echo: payload => payload }));`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'echo', payload: { value: 1 } } },
  ]);
  assert.deepEqual(legacy.messages[0].result, { value: 1 });
});

test('unknown methods and actions return method-not-found errors', async () => {
  const { messages } = await run(`start(() => ({}));`, [
    { id: 1, method: 'unknown' },
    ...['missing', 'toString', '__proto__', 'dispose'].map((action, index) => ({
      id: index + 2, method: 'plugin.invoke', params: { action },
    })),
  ]);
  assert.equal(messages.length, 5);
  for (const message of messages) assert.equal(message.error.code, -32601);
});

test('timeout and busy error messages can be overridden independently of protocol codes', async () => {
  const { messages } = await run(`start(host => ({ call: () => host('host.test', {}) }), {
    hostTimeoutMs: 20, errors: { timeout: 'Host request timed out', busy: 'An action is already running' },
  });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'call' } },
    { id: 2, method: 'plugin.invoke', params: { action: 'call' } },
  ], { endWhen: messages => [1, 2].every(id => messages.some(message => message.id === id)) });
  assert.deepEqual(messages.find(message => message.id === 1).error, { code: -32000, message: 'Host request timed out', data: { kind: 'HOST_TIMEOUT', retryable: true } });
  assert.deepEqual(messages.find(message => message.id === 2).error, { code: -32029, message: 'An action is already running' });
});

for (const mode of ['EOF', 'shutdown']) {
  for (const outcome of ['resolve', 'reject']) {
    test(`${mode} suppresses late action ${outcome} and result formatting`, async () => {
      const requests = [{ id: 1, method: 'plugin.invoke', params: { action: 'slow' } }];
      if (mode === 'shutdown') requests.push({ id: 2, method: 'plugin.shutdown' });
      const { messages, stderr } = await run(`start(() => ({
        slow: async () => {
          await new Promise(resolve => setTimeout(resolve, 100));
          ${outcome === 'reject' ? "throw new Error('late failure');" : "return 'late result';"}
        },
      }), { formatResult: result => { process.stderr.write('formatted'); return result; } });`, requests);
      assert.equal(messages.some(message => message.id === 1), false);
      assert.equal(stderr, '');
      if (mode === 'shutdown') assert.deepEqual(messages, [{ jsonrpc: '2.0', id: 2, result: {} }]);
      else assert.deepEqual(messages, []);
    });
  }
}

test('optional synchronous initialization validation gates invoke and adds handshake fields', async () => {
  const { messages } = await run(`start(() => ({ echo: () => 'ok' }), {
    validateInitialize: params => {
      if ((params?.runtimeProtocolVersion ?? params?.apiVersion) !== 1) {
        throw Object.assign(new Error('Unsupported version'), { code: -32602 });
      }
      return { manifestApiVersion: 2 };
    },
  });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'echo' } },
    { id: 2, method: 'plugin.initialize', params: { runtimeProtocolVersion: 2 } },
    { id: 3, method: 'plugin.invoke', params: { action: 'echo' } },
    { id: 4, method: 'plugin.initialize', params: { apiVersion: 1 } },
    { id: 5, method: 'plugin.invoke', params: { action: 'echo' } },
  ]);
  const byId = id => messages.find(message => message.id === id);
  assert.match(byId(1).error.message, /not initialized/);
  assert.equal(byId(2).error.code, -32602);
  assert.match(byId(3).error.message, /not initialized/);
  assert.deepEqual(byId(4).result, { runtimeProtocolVersion: 1, manifestApiVersion: 2 });
  assert.equal(byId(5).result, 'ok');
});

test('host timeout is configurable and send failures clear pending timers', async () => {
  const timed = await run(`start(host => ({ call: () => host('host.test', {}) }), { hostTimeoutMs: 20 });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'call' } },
  ], { endWhen: messages => messages.some(message => message.id === 1) });
  assert.match(timed.messages.find(message => message.id === 1).error.message, /宿主请求超时/);
  const failed = await run(`start(host => ({ call: async () => {
    try { await host('host.test', { text: 'x'.repeat(1000) }); }
    catch (error) {
      return { message: error.message, timers: process.getActiveResourcesInfo().filter(name => name === 'Timeout').length };
    }
  } }), { maxResultBytes: 200 });`, [{ id: 1, method: 'plugin.invoke', params: { action: 'call' } }], { eofDelay: 300 });
  assert.equal(failed.messages.length, 1);
  assert.match(failed.messages[0].result.message, /结果过大/);
  assert.equal(failed.messages[0].result.timers, 0);
});

test('EOF cancels pending host calls and blocks calls from cleanup', async () => {
  const { messages, stderr } = await run(`start(host => ({
    call: () => host('host.test', {}),
    dispose: async () => {
      try { await host('host.cleanup', {}); }
      catch { process.stderr.write('cleanup stopped\\n'); }
    },
  }));`, [{ id: 1, method: 'plugin.invoke', params: { action: 'call' } }]);
  assert.equal(messages.filter(message => message.method).length, 1);
  assert.equal(messages.some(message => message.id === 1), false, 'stopped action must not send an error response');
  assert.equal(stderr, 'cleanup stopped\n');
});

test('throwing cleanup still closes transport and runs once', async () => {
  const { messages, stderr } = await run(`start(() => ({ dispose: () => {
    process.stderr.write('disposed\\n'); throw new Error('cleanup failure');
  } }));`, [{ id: 1, method: 'plugin.shutdown' }], { eofDelay: 300 });
  assert.equal(messages[0].error.message, 'cleanup failure');
  assert.equal(stderr, 'disposed\nPlugin cleanup failed\n');
});

test('shutdown plus EOF disposes once and dispose is not callable', async () => {
  const { messages, stderr } = await run(`start(() => ({ dispose: () => process.stderr.write('disposed\\n') }));`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'dispose' } },
    { id: 2, method: 'plugin.shutdown' },
  ]);
  assert.equal(messages.find(message => message.id === 1).error.message, '不支持的操作');
  assert.deepEqual(messages.find(message => message.id === 2).result, {});
  assert.equal(stderr, 'disposed\n');
});

test('status bar adapter validates ids and fields before calling the host', async () => {
  const calls = [];
  const statusBar = createStatusBarAPI(async (method, params) => { calls.push({ method, params }); return true; });
  assert.equal(await statusBar.update('progress', { text: '42%', busy: true, tooltip: undefined }), true);
  await statusBar.reset('progress');
  await statusBar.reset();
  assert.deepEqual(calls, [
    { method: 'host.statusBar.update', params: { id: 'progress', text: '42%', busy: true } },
    { method: 'host.statusBar.reset', params: { id: 'progress' } },
    { method: 'host.statusBar.reset', params: {} },
  ]);
  assert.throws(() => statusBar.update('Bad Id', { text: 'x' }), error => error instanceof PluginError && error.code === 'INVALID_ARGUMENT');
  assert.throws(() => statusBar.update('progress', { color: 'red' }), /Unknown status bar field color/);
  assert.throws(() => statusBar.update('progress', []), /must be an object/);
  assert.throws(() => statusBar.reset(''), /declared item id/);
});

test('decorations.refresh forwards and tolerates old hosts', async () => {
  const calls = [];
  let answer = () => ({ scheduled: true });
  const decorations = createDecorationsAPI(async (method, params) => { calls.push({ method, params }); return answer(); });
  assert.deepEqual(await decorations.refresh(), { scheduled: true });
  assert.deepEqual(calls, [{ method: 'host.decorations.refresh', params: {} }]);
  answer = () => ({ scheduled: false });
  assert.deepEqual(await decorations.refresh(), { scheduled: false });
  // Hosts before 0.3.0 (or a plugin without fileDecorations) answer -32601.
  answer = () => { throw Object.assign(new Error('host method not found'), { code: -32601 }); };
  assert.deepEqual(await decorations.refresh(), { scheduled: false });
  answer = () => { throw Object.assign(new Error('workspace.read capability not granted'), { code: -32001 }); };
  await assert.rejects(decorations.refresh(), /workspace.read capability not granted/);
  answer = () => [];
  await assert.rejects(decorations.refresh(), error => error instanceof PluginError && error.code === 'INVALID_HOST_RESULT');
});

test('jdtls.executeCommand forwards declared commands and reports old hosts', async () => {
  const calls = [];
  let answer = () => ({ classpaths: ['/a'] });
  const jdtls = createJdtlsAPI(async (method, params, options) => { calls.push({ method, params, options }); return answer(); });
  assert.deepEqual(await jdtls.executeCommand({ root: '/work/app', command: 'vscode.java.resolveClasspath', arguments: ['com.example.Main', null] }), { classpaths: ['/a'] });
  assert.deepEqual(calls, [{ method: 'host.jdtls.executeCommand', params: { root: '/work/app', command: 'vscode.java.resolveClasspath', arguments: ['com.example.Main', null] }, options: { timeoutMs: 65_000 } }]);
  // No arguments sends an empty list; an empty result is null.
  answer = () => undefined;
  assert.equal(await jdtls.executeCommand({ root: '/work/app', command: 'vscode.java.resolveJavaExecutable' }), null);
  assert.deepEqual(calls[1].params, { root: '/work/app', command: 'vscode.java.resolveJavaExecutable', arguments: [] });
  // Hosts before 0.3.0 (or a plugin without jdtlsBundles) answer -32601.
  answer = () => { throw Object.assign(new Error('host method not found'), { code: -32601 }); };
  await assert.rejects(jdtls.executeCommand({ root: '/work/app', command: 'x' }), error => error instanceof PluginError && error.code === 'UNSUPPORTED');
  answer = () => { throw Object.assign(new Error('command is not declared by this plugin'), { code: -32001 }); };
  await assert.rejects(jdtls.executeCommand({ root: '/work/app', command: 'x' }), error => error.code === -32001 && /not declared/.test(error.message));
  // Invalid requests reject (never throw synchronously) without calling the host.
  for (const bad of [undefined, {}, { root: '', command: 'x' }, { root: 'relative', command: 'x' }, { root: '/work/app', command: '' }, { root: '/work/app', command: 'x', arguments: {} }]) {
    let pending;
    assert.doesNotThrow(() => { pending = jdtls.executeCommand(bad); });
    await assert.rejects(pending, error => error instanceof PluginError && error.code === 'INVALID_ARGUMENT');
  }
  assert.equal(calls.length, 4);
});

test('host events subscribe after initialize and dispatch after the ack', async () => {
  // Deliberately start beyond the former 100 ms EOF window, as under load.
  const { messages, stderr } = await run(`await new Promise(resolve => setTimeout(resolve, 150)); start(() => ({}), { events: ['workspace.changed', 'editor.fileSaved'], onEvent: (event, data) => process.stderr.write('event:' + event + ':' + JSON.stringify(data) + '\\n') });`, [
    { id: 0, method: 'plugin.event', params: { event: 'workspace.changed', data: {} } },
    { id: 1, method: 'plugin.initialize', params: {} },
    { id: 2, method: 'plugin.event', params: { event: 'editor.fileSaved', data: { path: 'src/a.ts' } } },
    { id: 3, method: 'plugin.event', params: { event: 'editor.unknown', data: {} } },
  ], { endWhen: messages => [0, 1, 2, 3].every(id => messages.some(message => message.id === id && !message.method)), replyToHost: message => message.method === 'host.api.describe'
    ? { events: [{ name: 'workspace.changed', available: true }, { name: 'editor.fileSaved', available: true }] }
    : { events: message.params.events } });
  assert.equal(messages.find(m => m.id === 0).error.code, -32601);
  const describe = messages.find(m => m.method === 'host.api.describe');
  assert.ok(describe);
  const subscribe = messages.find(m => m.method === 'host.events.subscribe');
  assert.deepEqual(subscribe.params, { events: ['workspace.changed', 'editor.fileSaved'] });
  assert.deepEqual(messages.find(m => m.id === 2).result, {});
  assert.equal(messages.find(m => m.id === 3).error.code, -32602);
  assert.match(stderr, /event:editor\.fileSaved:\{"path":"src\/a\.ts"\}/);
  const without = await run(`start(() => ({}));`, [
    { id: 1, method: 'plugin.initialize', params: {} },
    { id: 2, method: 'plugin.event', params: { event: 'workspace.changed', data: {} } },
  ]);
  assert.equal(without.messages.find(m => m.id === 2).error.code, -32601);
  assert.ok(!without.messages.some(m => m.method === 'host.events.subscribe'));
});

test('start rejects invalid event options before touching stdio', () => {
  assert.throws(() => start(() => ({}), { events: ['workspace.changed'] }), TypeError);
  assert.throws(() => start(() => ({}), { events: ['editor.nope'], onEvent() {} }), error => error.code === 'INVALID_ARGUMENT');
  assert.throws(() => start(() => ({}), { onEvent: 'no' }), TypeError);
  assert.throws(() => start(() => ({}), { events: Array(17).fill('workspace.changed'), onEvent() {} }), error => error.code === 'INVALID_ARGUMENT');
  assert.deepEqual([...HOST_EVENTS], ['workspace.changed', 'workspace.rootChanged', 'editor.activeFileChanged', 'editor.fileSaved', 'editor.selectionChanged', 'git.headChanged', 'ai.activity', 'ai.tasks', 'workspace.fileCreated', 'workspace.fileDeleted', 'workspace.fileRenamed', 'workspace.rescanRequired', 'git.statusChanged', 'app.lockChanged', 'app.suspend', 'app.resume', 'tests.runEnded']);
});

test('workspace API reads in chunks, maps host error codes, and validates writes', async () => {
  const content = Buffer.from('x'.repeat(600 * 1024));
  const calls = [];
  const workspace = createWorkspaceAPI(async (method, params) => {
    calls.push({ method, params });
    if (method === 'host.workspace.read') {
      const end = Math.min(params.offset + params.maxBytes, content.length);
      return { path: params.path, data: content.subarray(params.offset, end).toString('base64'), offset: params.offset, nextOffset: end, eof: end === content.length, size: content.length, modifiedAt: 1 };
    }
    if (method === 'host.workspace.write') {
      if (params.expectedSha256 === '') throw Object.assign(new Error('file already exists'), { code: -32002, data: { code: 'conflict' } });
      return { path: params.path, size: 2, sha256: 'ab', created: true };
    }
    if (method === 'host.workspace.root') return { root: null };
    throw new Error('unexpected ' + method);
  });
  assert.equal((await workspace.read('big.txt')).length, content.length);
  assert.equal(calls.filter(call => call.method === 'host.workspace.read').length, 2);
  await assert.rejects(workspace.read('big.txt', { maxBytes: 1024 }), error => error.code === 'RESULT_TOO_LARGE');
  assert.equal(await workspace.root(), null);
  await assert.rejects(workspace.write('a.txt', 'hi', { createOnly: true }), error => error instanceof PluginError && error.code === 'CONFLICT');
  await workspace.write('dir/b.txt', 'hi', { createParents: true });
  assert.deepEqual(calls.at(-1).params, { path: 'dir/b.txt', data: Buffer.from('hi').toString('base64'), createParents: true });
  for (const path of ['/etc/passwd', '../up', 'C:/x', 'a/../../b']) {
    assert.throws(() => workspace.stat(path), error => error.code === 'INVALID_ARGUMENT', path);
  }
  assert.throws(() => workspace.write('big.bin', Buffer.alloc(512 * 1024 + 1)), error => error.code === 'PAYLOAD_TOO_LARGE');
  assert.throws(() => workspace.write('a.txt', 'x', { expectedSha256: 'nope' }), error => error.code === 'INVALID_ARGUMENT');
});

test('secrets, clipboard, notifications, AI, host and events adapters send validated requests', async () => {
  const calls = [];
  const host = async (method, params) => { calls.push({ method, params }); return method === 'host.secrets.get' ? null : true; };
  assert.equal(await createSecretsAPI(host).get('token'), null);
  assert.throws(() => createSecretsAPI(host).set('token', 42), error => error.code === 'INVALID_ARGUMENT');
  await createClipboardAPI(host).write('copied');
  assert.throws(() => createClipboardAPI(host).read({ since: -1 }), error => error.code === 'INVALID_ARGUMENT');
  await createNotificationsAPI(host).notify({ title: 'Done', level: 'success' });
  assert.throws(() => createNotificationsAPI(host).notify({ title: 'x'.repeat(81) }), error => error.code === 'INVALID_ARGUMENT');
  assert.throws(() => createAIAPI(host).start({ prompt: 'hi' }), error => error.code === 'INVALID_ARGUMENT');
  await createAIAPI(host).approve('turn-1', 'approval-1', 1);
  await createHostAPI(host).log('warn', 'careful');
  assert.throws(() => createHostAPI(host).log('loud', 'x'), error => error.code === 'INVALID_ARGUMENT');
  await createEventsAPI(host).subscribe(['workspace.changed', 'workspace.changed']);
  assert.deepEqual(calls.map(call => call.method), ['host.secrets.get', 'host.clipboard.write', 'host.notify', 'host.ai.turn.approve', 'host.log', 'host.events.subscribe']);
  assert.deepEqual(calls[2].params, { title: 'Done', level: 'success', system: false });
  assert.deepEqual(calls[3].params, { id: 'turn-1', approvalId: 'approval-1', approved: true });
  assert.deepEqual(calls[5].params, { events: ['workspace.changed'] });
});

test('AI turns forward history "none" and reject other history values', async () => {
  const calls = [];
  const host = async (method, params) => { calls.push({ method, params }); return { id: 'aiturn-1', status: 'pending', done: false, text: '' }; };
  const ai = createAIAPI(host);
  await ai.start({ conversationKey: 'k', prompt: 'hi', allowedPlugins: [], history: 'none' });
  await ai.start({ conversationKey: 'k', prompt: 'hi', allowedPlugins: [] });
  assert.deepEqual(calls.map(call => call.params.history), ['none', undefined]);
  assert.equal('history' in calls[1].params, false);
  for (const history of ['all', 'None', 1, null]) {
    assert.throws(() => ai.start({ conversationKey: 'k', prompt: 'hi', allowedPlugins: [], history }), error => error.code === 'INVALID_ARGUMENT', String(history));
  }
  assert.equal(calls.length, 2);
});

test('notify forwards actions', async () => {
  const calls = [];
  const host = async (method, params) => { calls.push({ method, params }); return { delivered: true }; };
  const actions = [{ id: 'r', title: 'Retry', command: 'c' }];
  await createNotificationsAPI(host).notify({ title: 't', actions });
  assert.deepEqual(calls[0].params, { title: 't', level: 'info', system: false, actions });
  await createNotificationsAPI(host).notify({ title: 't' });
  assert.equal(Object.hasOwn(calls[1].params, 'actions'), false);
  const { createFakeHost } = await import('../../packages/plugin-sdk/node/testing.mjs');
  const fake = createFakeHost({ capabilities: ['notifications'] });
  assert.deepEqual(await fake.handle('host.notify', { title: 't', actions }), { delivered: true, actions: 1 });
  assert.deepEqual(await fake.handle('host.notify', { title: 't' }), { delivered: true, actions: 0 });
  assert.deepEqual(fake.state.notifications.map(item => item.actions), [actions, undefined]);
});

test('testing harness drives a real plugin through the fake host', async () => {
  const { startPlugin, createFakeHost } = await import('../../packages/plugin-sdk/node/testing.mjs');
  const directory = await mkdtemp(resolve(tmpdir(), 'mincode-harness-'));
  const workspace = await mkdtemp(resolve(tmpdir(), 'mincode-workspace-'));
  try {
    await writeFile(resolve(workspace, 'notes.md'), 'hello');
    await writeFile(resolve(directory, 'plugin.mjs'), `import { start, createStorageAPI, createWorkspaceAPI } from ${JSON.stringify(runtime)};
start(host => ({
  remember: async ({ value }) => { await createStorageAPI(host).set('last', value); return { saved: value }; },
  bigFile: async () => { const files = createStorageAPI(host).files; const data = Buffer.alloc(1_200_000, 9); await files.writeAtomic('big/blob.bin', data); return (await files.read('big/blob.bin')).equals(data); },
  shout: async () => { const workspace = createWorkspaceAPI(host); const text = await workspace.readText('notes.md'); await workspace.write('out/notes.md', text.toUpperCase(), { createParents: true }); return text.length; },
  secret: () => createWorkspaceAPI(host).readText('.env'),
}), { events: ['editor.fileSaved'], onEvent: (event, data) => process.stderr.write('saw ' + event + ' ' + data.path + '\\n') });`);
    const host = createFakeHost({ capabilities: ['storage', 'workspace.read', 'workspace.write'], workspaceRoot: workspace });
    const plugin = await startPlugin(resolve(directory, 'plugin.mjs'), { host });
    try {
      assert.deepEqual(await plugin.invoke('remember', { value: 42 }), { saved: 42 });
      assert.equal(host.state.storage.get('last'), 42);
      assert.equal(await plugin.invoke('bigFile'), true);
      assert.equal(host.state.files.get('big/blob.bin').length, 1_200_000);
      assert.equal(await plugin.invoke('shout'), 5);
      assert.equal(await readFile(resolve(workspace, 'out/notes.md'), 'utf8'), 'HELLO');
      await assert.rejects(plugin.invoke('secret'), error => /not accessible/.test(error.message));
      for (let attempt = 0; attempt < 50 && !host.state.subscriptions.has('editor.fileSaved'); attempt++) await new Promise(done => setTimeout(done, 10));
      assert.ok(host.state.subscriptions.has('editor.fileSaved'));
      assert.deepEqual(await plugin.sendEvent('editor.fileSaved', { path: 'notes.md' }), {});
      await assert.rejects(plugin.invoke('missing'), error => error.code === -32601);
      assert.deepEqual(plugin.protocolViolations, []);
    } finally {
      assert.equal((await plugin.stop()).code, 0);
    }
    assert.match(plugin.stderr, /saw editor\.fileSaved notes\.md/);
  } finally {
    await rm(directory, { recursive: true, force: true });
    await rm(workspace, { recursive: true, force: true });
  }
});

test('concurrentActions bypass the serial gate while undeclared actions stay serial', async () => {
  const { messages } = await run(`start(() => ({
    write: () => new Promise(resolve => setTimeout(() => resolve('w'), 80)),
    read: () => new Promise(resolve => setTimeout(() => resolve('r'), 20)),
  }), { concurrentActions: ['read'], maxConcurrent: 2 });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'write' } },
    { id: 2, method: 'plugin.invoke', params: { action: 'write' } },
    { id: 3, method: 'plugin.invoke', params: { action: 'read' } },
    { id: 4, method: 'plugin.invoke', params: { action: 'read' } },
    { id: 5, method: 'plugin.invoke', params: { action: 'read' } },
  ], { endWhen: messages => [1, 2, 3, 4, 5].every(id => messages.some(message => message.id === id)) });
  const byId = id => messages.find(message => message.id === id);
  assert.equal(byId(1).result, 'w');
  assert.equal(byId(2).error.code, -32029, 'undeclared actions keep the serial gate');
  assert.equal(byId(3).result, 'r');
  assert.equal(byId(4).result, 'r');
  assert.equal(byId(5).error.code, -32029, 'maxConcurrent bounds concurrent actions');
  // Concurrent reads finish while the write still runs.
  assert.ok(messages.indexOf(byId(3)) < messages.indexOf(byId(1)));
});

test('taskGate separate keeps invokes available while a task runs; shared stays compatible', async () => {
  for (const taskGate of ['shared', 'separate']) {
    const { messages } = await run(`start(() => ({
      work: () => new Promise(resolve => setTimeout(() => resolve('done'), 60)),
      quick: () => 'q',
    }), { taskGate: '${taskGate}' });`, [
      { id: 1, method: 'plugin.initialize', params: { features: { tasks: 1 } } },
      { id: 2, method: 'plugin.task.start', params: { taskId: 'one', action: 'work' } },
      { id: 3, method: 'plugin.invoke', params: { action: 'quick' } },
    ], { endWhen: messages => messages.some(message => message.id === 3) });
    const reply = messages.find(message => message.id === 3);
    if (taskGate === 'shared') assert.equal(reply.error.code, -32029);
    else assert.equal(reply.result, 'q');
  }
});

test('busyWaitMs queues invokes for the gate instead of failing at once', async () => {
  const { messages } = await run(`let running = 0, peak = 0;
  start(() => ({
    work: async () => { peak = Math.max(peak, ++running); await new Promise(resolve => setTimeout(resolve, 20)); running--; return peak; },
  }), { busyWaitMs: 2000, maxQueued: 2 });`, [
    { id: 1, method: 'plugin.invoke', params: { action: 'work' } },
    { id: 2, method: 'plugin.invoke', params: { action: 'work' } },
    { id: 3, method: 'plugin.invoke', params: { action: 'work' } },
    { id: 4, method: 'plugin.invoke', params: { action: 'work' } },
  ], { endWhen: messages => [1, 2, 3, 4].every(id => messages.some(message => message.id === id)) });
  const byId = id => messages.find(message => message.id === id);
  for (const id of [1, 2, 3]) assert.equal(byId(id).result, 1, `invoke ${id} ran alone`);
  assert.equal(byId(4).error.code, -32029, 'maxQueued bounds waiting invokes');
});

test('taskEvents announce progress (throttled) and completion only when negotiated', async () => {
  const source = `start(() => ({ work: async (p, ctx) => {
    for (let i = 0; i < 20; i++) ctx.reportProgress({ i });
    await new Promise(resolve => setTimeout(resolve, 150));
    return 'ok';
  } }), { taskEventIntervalMs: 50 });`;
  const finished = messages => messages.filter(message => message.method === 'plugin.task.changed').length >= 3;
  const negotiated = await run(source, [
    { id: 1, method: 'plugin.initialize', params: { features: { tasks: 1, taskEvents: 1 } } },
    { id: 2, method: 'plugin.task.start', params: { taskId: 'one', action: 'work' } },
  ], { endWhen: finished });
  assert.deepEqual(negotiated.messages.find(message => message.id === 1).result.features, { tasks: 1, taskEvents: 1 });
  const notifications = negotiated.messages.filter(message => message.method === 'plugin.task.changed');
  // One immediate and one trailing progress notification for the burst, then completion.
  assert.equal(notifications.length, 3);
  assert.ok(notifications.every(message => message.id === undefined && message.params.taskId === 'one'));

  const legacy = await run(source, [
    { id: 1, method: 'plugin.initialize', params: { features: { tasks: 1 } } },
    { id: 2, method: 'plugin.task.start', params: { taskId: 'one', action: 'work' } },
  ], { eofDelay: 300 });
  assert.deepEqual(legacy.messages.find(message => message.id === 1).result.features, { tasks: 1 });
  assert.equal(legacy.messages.some(message => message.method === 'plugin.task.changed'), false);
});

test('start rejects invalid concurrency options before touching stdio', () => {
  assert.throws(() => start(() => ({}), { maxInflight: 0 }), /maxInflight/);
  assert.throws(() => start(() => ({}), { concurrentActions: 'read' }), /concurrentActions/);
  assert.throws(() => start(() => ({}), { taskGate: 'none' }), /taskGate/);
  assert.throws(() => start(() => ({}), { busyWaitMs: -1 }), /busyWaitMs/);
  assert.throws(() => start(() => ({}), { taskEvents: 'yes' }), /taskEvents/);
});
