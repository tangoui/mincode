import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

for (const name of ['devtools', 'hbuilder-simulator', 'openapi-contract', 'grpc-debug', 'kafka-inspector', 'knowledge-base', 'network-diagnostics', 'todo']) {
  test(`${name}: browser bundle must not require Node environment globals`, async () => {
    const source = await readFile(new URL(`../../official-plugins/${name}/ui/app.js`, import.meta.url), 'utf8');
    assert.doesNotMatch(source, /\bprocess\s*\.\s*env\s*\.\s*NODE_ENV\b/, 'Node process is unavailable in the sandbox; initialization would fail before handshake');
    assert.ok(source.includes('mincode.plugin.ready'));
    assert.ok(source.includes('mincode.host.probe'));
    assert.ok(source.includes('mincode.plugin.probe'));
  });
}
