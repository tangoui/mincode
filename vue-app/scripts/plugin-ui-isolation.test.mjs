import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc';

const packages = new URL('../../official-plugins/', import.meta.url);
const names = ['devtools', 'grpc-debug', 'hbuilder-simulator', 'kafka-inspector', 'knowledge-base', 'network-diagnostics', 'openapi-contract', 'todo'];
for (const name of names) {
  test(`${name}: package-owned UI compiles without missing template bindings`, async () => {
    const base = new URL(`${name}/ui-src/`, packages);
    for (const file of await readdir(base)) {
      if (!file.endsWith('.vue')) continue;
      const source = await readFile(new URL(file, base), 'utf8');
      const { descriptor, errors } = parse(source);
      assert.deepEqual(errors, []);
      const script = compileScript(descriptor, { id: `${name}-${file}` });
      const template = compileTemplate({ source: descriptor.template.content, filename: file, id: `${name}-${file}`, compilerOptions: { bindingMetadata: script.bindings } });
      assert.deepEqual(template.errors, []);
      // Unresolved _ctx accesses indicate state accidentally lost while splitting pages.
      const unresolved = [...template.code.matchAll(/_ctx\.([A-Za-z_$][\w$]*)/g)].map(match => match[1]);
      assert.deepEqual([...new Set(unresolved)], [], `${file}: unresolved template bindings`);
    }
  });
}

test('shared UI does not select or import business plugins', async () => {
  const base = new URL('../src/plugin-ui/', import.meta.url);
  for (const file of await readdir(base)) {
    const source = await readFile(new URL(file, base), 'utf8');
    for (const name of names) assert.ok(!source.includes(name), `${file} contains business plugin ${name}`);
    assert.doesNotMatch(source, /config\.id|Workbench\.vue/);
  }
});

test('each browser bundle excludes other plugin business pages', async () => {
  const markers = {
    devtools: 'JSON 转代码',
    'grpc-debug': '读取描述不会请求远端服务',
    'hbuilder-simulator': 'uni-app 与小程序助手',
    'kafka-inspector': '仅搜索本次已读取结果',
    'knowledge-base': 'KnowledgeWorkbench',
    'network-diagnostics': '诊断报告（复制前请检查目标信息）',
    'openapi-contract': '生成 cURL（不执行）',
    todo: '清晰安排，专注完成',
  };
  for (const name of names) {
    const bundle = await readFile(new URL(`${name}/ui/app.js`, packages), 'utf8');
    assert.ok(bundle.includes(markers[name]), `${name}: own business UI missing`);
    for (const other of names.filter(other => other !== name)) {
      assert.ok(!bundle.includes(markers[other]), `${name} embeds ${other}`);
    }
    assert.doesNotMatch(bundle, /@mincode\/plugin-ui|process\.env\.NODE_ENV/);
  }
});
