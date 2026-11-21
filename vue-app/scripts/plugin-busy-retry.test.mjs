import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const sdk = await readFile(new URL('../../packages/plugin-sdk/node/runtime.mjs', import.meta.url), 'utf8');
const transport = await readFile(new URL('../src/plugin-ui/transport.js', import.meta.url), 'utf8');
const protocol = await readFile(new URL('../src/domain/plugin-ui/protocol.ts', import.meta.url), 'utf8');
const viewFrame = await readFile(new URL('../src/components/plugins/PluginViewFrame.vue', import.meta.url), 'utf8');
const etcdMain = await readFile(new URL('../../official-plugins/etcd-manager/backend/main.go', import.meta.url), 'utf8');
const kafkaMain = await readFile(new URL('../../official-plugins/kafka-inspector/backend/main.go', import.meta.url), 'utf8');
const goSdkOptions = await readFile(new URL('../../packages/plugin-sdk-go/rpc/options.go', import.meta.url), 'utf8');
const kafkaWorkbench = await readFile(new URL('../../official-plugins/kafka-inspector/ui-src/KafkaWorkbench.vue', import.meta.url), 'utf8');

test('plugin runtimes reject busy invocations with the reserved -32029 code', () => {
  assert.match(protocol, /PLUGIN_PROCESS_BUSY_ERROR_CODE = -32029/);
  assert.match(sdk, /errors\.busy \?\? '已有任务运行，请稍后重试'\), \{ code: -32029 \}\)/);
  assert.match(goSdkOptions, /CodeBusy\s+= -32029/);
  assert.match(etcdMain, /o\.Busy = rpc\.NewError\(rpc\.CodeBusy, "已有任务运行，请稍后重试"\)/);
});

test('host view frame maps the reserved busy code to the shared "busy" response code', () => {
  assert.match(viewFrame, /PLUGIN_PROCESS_BUSY_ERROR_CODE \? "busy" : "invoke-failed"/);
});

test('shared transport retries busy rejections with bounded backoff for invoke calls', () => {
  assert.match(transport, /cause\?\.code !== 'busy' \|\| attempt >= busyRetryDelaysMs\.length \|\| closed/);
  assert.match(transport, /waitRetry\(delay\)\.then\(send\)\.catch\(retry\)/);
  const delays = transport.match(/busyRetryDelaysMs = \[([\d,\s]+)\]/)?.[1].split(',').map(Number);
  assert.ok(delays?.length >= 3 && delays.every(value => value > 0 && value <= 10_000), 'bounded retry delays');
  // Short serial actions release the gate within tens of milliseconds: retry quickly first,
  // while the total wait stays close to the former [500 … 8000] schedule (15.5 s).
  assert.ok(delays[0] <= 100, 'first retry within 100 ms');
  const total = delays.reduce((sum, value) => sum + value, 0);
  assert.ok(total >= 12_000 && total <= 20_000, `total busy wait ${total} ms`);
  // close() rejects calls waiting for their next retry instead of letting them fire later.
  assert.match(transport, /for \(const entry of retryWaits\) \{ clearTimeout\(entry\.timer\);/);
  // Path pickers bypass the busy retry: they go straight to the host dialog.
  assert.match(transport, /if \(kind\) return request\(\{\s*type: 'mincode\.plugin\.pick-path', kind,/);
});

test('Kafka Go backend rejects overload with -32029 and relies on the shared transport retry', () => {
  assert.match(kafkaMain, /o\.Busy = rpc\.NewError\(rpc\.CodeBusy, "\[KAFKA_BUSY\]/);
  assert.doesNotMatch(kafkaWorkbench, /callPlugin|busyRetryDelaysMs/);
});

test('official Go plugins declare only manifest read actions as concurrent', async () => {
  const { readdir } = await import('node:fs/promises');
  const root = new URL('../../official-plugins/', import.meta.url);
  const declared = [];
  for (const entry of await readdir(root, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const backend = new URL(`${entry.name}/backend/`, root);
    let files;
    try { files = (await readdir(backend)).filter(name => name.endsWith('.go') && !name.endsWith('_test.go')); } catch { continue; }
    const text = (await Promise.all(files.map(file => readFile(new URL(file, backend), 'utf8')))).join('\n');
    const match = text.match(/var concurrentActions = \[\]string\{([^}]*)\}/);
    if (!match) continue;
    const names = [...match[1].matchAll(/"([^"]+)"/g)].map(m => m[1]);
    const manifest = JSON.parse(await readFile(new URL(`${entry.name}/plugin.json`, root), 'utf8'));
    const actions = manifest.contributes?.actions ?? manifest.actions ?? [];
    for (const name of names) {
      const action = actions.find(candidate => candidate.id === name);
      assert.ok(action, `${entry.name}: concurrent action ${name} is not declared in plugin.json`);
      assert.equal(action.risk, 'read', `${entry.name}: concurrent action ${name} must have risk "read"`);
    }
    assert.match(text, /o\.ConcurrentActions = concurrentActions/, `${entry.name}: concurrentActions must be wired into rpc.Options`);
    declared.push(entry.name);
  }
  assert.ok(declared.length >= 1, 'at least one official plugin opts in');
});
