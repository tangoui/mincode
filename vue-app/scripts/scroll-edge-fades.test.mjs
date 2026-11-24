import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { scrollEdgeFades } from '../src/domain/layout/scroll-edge-fades.ts';

test('no fades when the content fits', () => {
  assert.deepEqual(scrollEdgeFades({ scrollTop: 0, clientHeight: 600, scrollHeight: 600 }), { top: false, bottom: false });
});

test('only the bottom fades at the top of overflowing content', () => {
  assert.deepEqual(scrollEdgeFades({ scrollTop: 0, clientHeight: 400, scrollHeight: 700 }), { top: false, bottom: true });
});

test('both edges fade in the middle', () => {
  assert.deepEqual(scrollEdgeFades({ scrollTop: 120, clientHeight: 400, scrollHeight: 700 }), { top: true, bottom: true });
});

test('only the top fades at the end, tolerating sub-pixel rounding', () => {
  assert.deepEqual(scrollEdgeFades({ scrollTop: 299.6, clientHeight: 400, scrollHeight: 700 }), { top: true, bottom: false });
});

test('activity bar masks the scroll area edges that have hidden items', async () => {
  const css = await readFile(new URL('../src/styles/foundation/foundation.css', import.meta.url), 'utf8');
  for (const edge of ['top', 'bottom']) {
    const rule = css.match(new RegExp(`\\.app-activity-bar__nav-scroll\\.is-fade-${edge}[^{]*\\{([^}]*)\\}`));
    assert.ok(rule, `missing is-fade-${edge} rule`);
    assert.match(rule[1], /mask-image:\s*linear-gradient/);
  }
  const sidebar = await readFile(new URL('../src/components/layout/AppSidebar.vue', import.meta.url), 'utf8');
  assert.match(sidebar, /scrollEdgeFades/);
  assert.match(sidebar, /is-fade-top/);
  assert.match(sidebar, /is-fade-bottom/);
});
