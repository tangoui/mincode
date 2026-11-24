import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { access, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { promisify } from 'node:util';

const require = createRequire(import.meta.url);
const browser = process.env.CHROME_BIN || '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge';
const available = await access(browser).then(() => true, () => false);

test('keeps tool metadata, approval buttons and plan actions inside the timeline at supported widths', {
  skip: !available && 'set CHROME_BIN to a Chromium browser',
}, async () => {
  const styles = await Promise.all([
    readFile(require.resolve('element-plus/dist/index.css'), 'utf8'),
    readFile(new URL('../src/styles/foundation/foundation.css', import.meta.url), 'utf8'),
    readFile(new URL('../src/styles/shared/markdown-preview.css', import.meta.url), 'utf8'),
    readFile(new URL('../src/styles/workspaces/ai/drawer.css', import.meta.url), 'utf8'),
    readFile(new URL('../src/styles/workspaces/ai/timeline.css', import.meta.url), 'utf8'),
  ]);
  const directory = await mkdtemp(join(tmpdir(), 'mincode-ai-layout-'));
  try {
    const file = join(directory, 'layout.html');
    await writeFile(file, `<!doctype html><meta charset="utf-8"><style>${styles.join('\n')}
.ai-message-thread { --app-text: rgb(24, 25, 26); }
</style>
<div class="ai-message-thread">
  <article class="ai-timeline-tool"><button class="ai-timeline__row">
    <svg class="ai-timeline__icon"></svg>
    <span class="ai-timeline__title"><strong>Edit file</strong><code title="src/very/deep/path/to/acceptance/fixture.ts">src/very/deep/path/to/acceptance/fixture.ts</code></span>
    <span class="ai-timeline__meta"><span class="ai-timeline__diff"><span>+120</span><span>−30</span></span><span>15.2s</span><span role="status">Awaiting approval</span><svg class="ai-timeline__icon"></svg></span>
  </button></article>
  <section class="ai-timeline-summary"><button class="ai-timeline__row"><strong class="ai-timeline__title">Read 2 files</strong></button></section>
  <section class="ai-timeline-approval"><div class="ai-timeline__contents"><div class="ai-timeline__approval-actions">
    <button class="el-button el-button--small"><span>Always allow this call with the same parameters</span></button>
    <button class="el-button el-button--small"><span>Reject</span></button>
  </div></div></section>
  <section class="ai-timeline-plan"><div class="ai-timeline__action-row">
    <button class="ai-timeline__row"><svg class="ai-timeline__icon"></svg><strong class="ai-timeline__title">Plan updated 2/3</strong><svg class="ai-timeline__icon"></svg></button>
    <button class="ai-timeline__icon-button"><svg></svg></button>
  </div></section>
  <div class="ai-timeline__contents"><div class="ai-timeline__result">
    <div class="ai-agent-event-detail is-terminal is-failed is-inline">
      <button type="button" class="ai-timeline__output-toggle" aria-expanded="false">显示全部</button>
      <span class="ai-agent-event-detail__language" aria-hidden="true">terminal</span>
      <div class="ai-agent-event-detail__viewport is-collapsed is-tail" style="max-block-size: 12lh">
        <div class="ai-agent-event-detail__body"><pre><code>FAIL integration: expected status 200, received 503.</code></pre></div>
      </div>
    </div>
    <button type="button" class="ai-timeline__icon-button" aria-label="Copy output"><svg></svg></button>
  </div></div>
</div><pre id="result"></pre><script>
const failures = [];
const thread = document.querySelector('.ai-message-thread');
const markdown = document.createElement('div');
markdown.className = 'ai-agent-event-detail is-markdown is-inline';
markdown.innerHTML = '<article class="project-markdown-preview">' +
  '<p>Long Markdown result</p>'.repeat(20) +
  '<pre><code>' + 'longCodeToken'.repeat(100) + '</code></pre></article>';
thread.querySelector('.ai-timeline__result').append(markdown);
for (const width of [304, 800]) {
  thread.style.width = width + 'px';
  if (markdown.clientHeight <= 220) failures.push(width + ': Markdown result did not grow with its content');
  for (const element of [markdown, ...markdown.querySelectorAll('*')]) {
    const style = getComputedStyle(element);
    if ([style.overflowX, style.overflowY].some(value => value === 'auto' || value === 'scroll')) {
      failures.push(width + ': nested scrolling in ' + element.tagName);
    }
    if (element.getBoundingClientRect().right > markdown.getBoundingClientRect().right + 1) {
      failures.push(width + ': Markdown block extends past the result');
    }
  }
}
markdown.remove();
const viewport = thread.querySelector('.ai-agent-event-detail__viewport');
const body = viewport.firstElementChild;
const outputText = body.querySelector('code');
const shortText = outputText.textContent;
outputText.textContent = ('Single line of wrapped output with a final status. ').repeat(60);
for (const tail of [false, true]) {
  viewport.classList.toggle('is-tail', tail);
  const clipped = viewport.getBoundingClientRect();
  const full = body.getBoundingClientRect();
  if (clipped.height !== 180 || full.height <= clipped.height) failures.push('wrapped line should be clipped at 12 visual lines');
  if (Math.abs(tail ? clipped.bottom - full.bottom : clipped.top - full.top) > 1) failures.push('preview clips the wrong end');
  viewport.scrollTop = 100;
  if (viewport.scrollTop !== 0 || getComputedStyle(viewport).overflowY !== 'clip') failures.push('preview is internally scrollable');
}
viewport.classList.remove('is-collapsed');
viewport.style.maxBlockSize = '';
if (body.offsetHeight !== viewport.offsetHeight) failures.push('expanded output does not grow with content');
outputText.textContent = shortText;
for (const width of [88, 304, 800]) {
  thread.style.width = width + 'px';
  const limit = thread.getBoundingClientRect().right;
  for (const item of thread.querySelectorAll('svg, [role=status], .ai-timeline__diff, button')) {
    if (item.getBoundingClientRect().right > limit + 1 || item.scrollWidth > item.clientWidth + 1) {
      failures.push(width + ': overflow in ' + (item.textContent || item.tagName));
    }
  }
  const meta = [...thread.querySelector('.ai-timeline__meta').children];
  for (const [index, item] of meta.entries()) {
    const a = item.getBoundingClientRect();
    for (const other of meta.slice(index + 1)) {
      const b = other.getBoundingClientRect();
      if (a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom) failures.push(width + ': overlapping metadata');
    }
  }
  if (width === 800) {
    for (const selector of ['.ai-timeline-tool strong', '.ai-timeline-summary strong']) {
      if (getComputedStyle(thread.querySelector(selector)).fontWeight !== '400') failures.push('expected regular weight: ' + selector);
    }
    if (getComputedStyle(thread.querySelector('.ai-timeline-tool code')).color !== 'rgb(24, 25, 26)') failures.push('expected primary text color for the tool target');
    const rows = [...thread.querySelectorAll('.ai-timeline__row, .ai-timeline__icon-button')];
    for (const row of rows) {
      if (row.getBoundingClientRect().height !== 24) failures.push('expected a 24px header: ' + row.textContent.trim());
    }
    const tool = thread.querySelector('.ai-timeline-tool');
    if (tool.nextElementSibling.getBoundingClientRect().top !== tool.getBoundingClientRect().bottom) failures.push('adjacent timeline items have a gap');
  }
  for (const theme of ['light', 'dark']) {
    document.documentElement.dataset.theme = theme;
    const output = thread.querySelector('.ai-agent-event-detail');
    const toggle = output.querySelector('.ai-timeline__output-toggle');
    const language = output.querySelector('.ai-agent-event-detail__language');
    const buttonBounds = toggle.getBoundingClientRect();
    const languageBounds = language.getBoundingClientRect();
    const codeBounds = output.querySelector('pre').getBoundingClientRect();
    const overlap = buttonBounds.left < languageBounds.right && languageBounds.left < buttonBounds.right
      && buttonBounds.top < languageBounds.bottom && languageBounds.top < buttonBounds.bottom;
    if (overlap) failures.push(width + ' ' + theme + ': output toggle overlaps language label');
    if (Math.max(buttonBounds.bottom, languageBounds.bottom) > codeBounds.top) {
      failures.push(width + ' ' + theme + ': output metadata overlaps the code block');
    }
    if (languageBounds.right > output.getBoundingClientRect().right + 1) {
      failures.push(width + ' ' + theme + ': output language label overflows its column');
    }
  }
  const path = thread.querySelector('code');
  if (width === 304 && (path.clientWidth === 0 || path.scrollWidth <= path.clientWidth)) failures.push(width + ': path should be visible and truncated');
}
document.querySelector('#result').textContent = JSON.stringify(failures);
</script>`);
    const { stdout } = await promisify(execFile)(browser, [
      '--headless', '--no-sandbox', '--disable-gpu', '--no-first-run', `--user-data-dir=${join(directory, 'profile')}`,
      '--virtual-time-budget=1000', '--dump-dom', `file://${file}`,
    ], { timeout: 90_000, maxBuffer: 8 * 1024 * 1024 });
    const result = stdout.match(/<pre id="result">([^<]*)<\/pre>/);
    assert.ok(result, 'browser did not execute layout checks');
    assert.deepEqual(JSON.parse(result[1]), []);
  } finally {
    await rm(directory, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  }
});
