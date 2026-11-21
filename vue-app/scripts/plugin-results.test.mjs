import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc';
import { formatResponse } from '../src/plugin-ui/result-model.mjs';

test('gRPC responses pretty print JSON without corrupting descriptors or empty output', () => {
  assert.equal(formatResponse('{"ok":true}'), '{\n  "ok": true\n}');
  assert.equal(formatResponse('service Example { }'), 'service Example { }');
  assert.equal(formatResponse(''), '调用完成，响应为空');
});
for (const name of ['OfficialPluginPage', 'PluginResults']) {
  test(`${name} compiles with template bindings`, async () => {
    const source = await readFile(new URL(`../src/plugin-ui/${name}.vue`, import.meta.url), 'utf8');
    const { descriptor, errors } = parse(source);
    assert.deepEqual(errors, []);
    const script = compileScript(descriptor, { id: name });
    const template = compileTemplate({ source: descriptor.template.content, filename: `${name}.vue`, id: name, compilerOptions: { bindingMetadata: script.bindings } });
    assert.deepEqual(template.errors, []);
  });
}
