import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { parse } from 'vue/compiler-sfc';

const source = readFileSync(new URL('../src/components/settings/ExternalMcpPanel.vue', import.meta.url), 'utf8');
const css = readFileSync(new URL('../src/styles/workspaces/settings/dialog.css', import.meta.url), 'utf8');

test('合并后保留临时路径警告与复制工具栏，不残留冲突标记', () => {
  assert.doesNotMatch(source, /^(?:<<<<<<<|=======|>>>>>>>)/m);
  assert.match(source, /<el-alert v-if="transient"[^>]*transientPath/);
  assert.match(source, /class="settings-external-mcp-config__toolbar"/);
});

test('配置复制按钮位于代码框内部的右上角工具栏，不遮挡代码', () => {
  const { descriptor, errors } = parse(source);
  assert.deepEqual(errors, []);
  assert.match(descriptor.template.content, /class="settings-external-mcp-config__snippet">\s*<div class="settings-external-mcp-config__toolbar">[\s\S]*?@click="copyConfig"[\s\S]*?<\/div>\s*<pre/);
  assert.match(css, /\.settings-external-mcp-config__toolbar\s*\{[^}]*justify-content:\s*flex-end/s);
  assert.equal((descriptor.template.content.match(/@click="copyConfig"/g) ?? []).length, 1);
});

test('复制成功反馈有定时恢复和卸载清理，失败仍显示错误', () => {
  assert.match(source, /copied\.value = true/);
  assert.match(source, /copied\.value = false/);
  assert.match(source, /clearTimeout\(copyFeedbackTimer\)/);
  assert.match(source, /onBeforeUnmount\([\s\S]*?clearTimeout\(copyFeedbackTimer\)/);
  assert.match(source, /ElMessage\.error\(nativeErrorMessage\(error\)\)/);
});
