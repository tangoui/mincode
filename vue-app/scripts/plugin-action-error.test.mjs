import { test } from 'node:test';
import assert from 'node:assert/strict';
import { describePluginActionFailure as describe } from '../src/domain/plugin-ui/action-error.mjs';

test('allowlisted protocol failures map to fixed safe messages', () => {
  for (const [raw, expected] of [
    ['context deadline exceeded secret=abc', '操作超时'],
    ['request timed out after 30s', '操作超时'],
    ['plugin process not running for session', '插件已停止或更新'],
    ['plugin UI session stale', '插件已停止或更新'],
    ['generation changed during invoke', '插件已停止或更新'],
    ['capability not granted: fs.write', '操作未获授权'],
    ['operation not allowed by policy', '操作未获授权'],
    ['permission denied for private-host', '操作未获授权'],
    ['payload too large: 3MB secret=abc', '请求数据超过限制'],
    ['request exceeds limit', '请求数据超过限制'],
    ['result too large to return', '操作结果超过限制'],
    ['response exceeds limit', '操作结果超过限制'],
  ]) {
    const result = describe(new Error(raw), '操作失败');
    assert.ok(result.includes(expected), `${raw} -> ${result}`);
    assert.ok(!result.includes('abc'));
    assert.ok(!result.includes('private-host'));
  }
});

test('non-protocol errors fall back without forwarding raw host data', () => {
  assert.equal(describe(new Error('tool: command not found secret=abc'), '操作失败'), '操作失败');
  assert.equal(describe(new Error('SSL handshake failed on private-host'), '操作失败'), '操作失败');
  assert.equal(describe(new Error('connection refused private-host'), '操作失败'), '操作失败');
  assert.equal(describe('getaddrinfo ENOTFOUND private-host', '操作失败'), '操作失败');
});

test('string causes are classified the same as Error causes', () => {
  assert.match(describe('capability not granted: fs.write', '操作失败'), /操作未获授权/);
});

test('missing or non-string causes return the fallback', () => {
  assert.equal(describe(null, '操作失败'), '操作失败');
  assert.equal(describe(undefined, '操作失败'), '操作失败');
  assert.equal(describe(new Error(), '操作失败'), '操作失败');
  assert.equal(describe(42, '操作失败'), '操作失败');
});
