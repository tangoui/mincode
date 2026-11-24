import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const css = await readFile(new URL('../src/styles/workspaces/ai/timeline.css', import.meta.url), 'utf8');
const rule = (selector) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = css.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  return match?.[1] ?? '';
};

test('full-parameters toggle hides the native disclosure triangle', () => {
  assert.match(rule('.ai-timeline__parameters > summary'), /list-style:\s*none/);
  assert.match(rule('.ai-timeline__parameters > summary::-webkit-details-marker'), /display:\s*none/);
});

test('full-parameters toggle draws the timeline chevron and turns it when open', () => {
  assert.match(rule('.ai-timeline__parameters > summary::before'), /content:\s*""/);
  assert.match(rule('.ai-timeline__parameters[open] > summary::before'), /rotate\(45deg\)/);
});

test('full-parameters toggle matches timeline row hover and focus styles', () => {
  assert.match(rule('.ai-timeline__parameters > summary:hover'), /color:\s*var\(--el-text-color-primary\)/);
  assert.match(rule('.ai-timeline__parameters > summary:focus-visible'), /outline:\s*1px solid var\(--el-color-primary\)/);
});
