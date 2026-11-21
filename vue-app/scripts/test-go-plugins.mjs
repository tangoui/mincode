// Vet, test and tidy-check the shared Go plugin SDK and every official Go plugin
// backend, each as its own module (GOWORK=off, exactly like the release build).
//
//   node scripts/test-go-plugins.mjs                 # SDK, its golden suite + every Go plugin
//   node scripts/test-go-plugins.mjs nats-client sdk # only these ("sdk" = packages/plugin-sdk-go,
//                                                    #  "sdk-conformance" = packages/plugin-sdk-go-conformance)
//
// A backend whose go.mod requires github.com/mincodeai/plugin-sdk-go must replace it with
// the in-repo path ../../../packages/plugin-sdk-go (no published SDK versions exist).
// Backends built standalone outside the monorepo (VENDORED_SDK_CONSUMERS, e.g. the
// draw gitlink repository) instead vendor the SDK and must replace it with
// ./third_party/plugin-sdk-go; that copy must match packages/plugin-sdk-go
// (tools/plugin-sdk/sync-go-sdk.mjs).
import { execFile } from 'node:child_process';
import { access, readFile } from 'node:fs/promises';
import { availableParallelism } from 'node:os';
import { dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { goBuildTags, goCgoEnv, goPlugins, goTarget } from './build-go-plugins.mjs';
import { checkTarget, plan } from '../../tools/plugin-sdk/sync-go-sdk.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const SDK = 'sdk';
// Golden transcript suite kept out of the published SDK module to keep it small.
const SDK_CONFORMANCE = 'sdk-conformance';
const SDK_MODULE = 'github.com/mincodeai/plugin-sdk-go';
const SDK_REPLACE = '../../../packages/plugin-sdk-go';
const SDK_VENDORED_REPLACE = './third_party/plugin-sdk-go';
export const VENDORED_SDK_CONSUMERS = new Set(['draw']);
const env = { ...process.env, GOWORK: 'off' };

function run(args, cwd, extraEnv = {}) {
  return new Promise(done => {
    execFile('go', args, { cwd, env: { ...env, ...extraEnv }, timeout: 15 * 60 * 1000, maxBuffer: 64 * 1024 * 1024 }, (error, stdout, stderr) => {
      done({ ok: !error, output: `${stdout}${stderr}`.trim() || (error ? String(error.message) : '') });
    });
  });
}

const exists = path => access(path).then(() => true, () => false);

// Returns problems with the SDK require/replace pair of one go.mod. vendored:
// the backend must use its vendored copy instead of the monorepo path.
export function checkSdkReplace(gomod, { vendored = false } = {}) {
  const expected = vendored ? SDK_VENDORED_REPLACE : SDK_REPLACE;
  const lines = gomod.split('\n').map(line => line.replace(/\/\/.*$/, '').trim());
  let block = '';
  let required = false;
  const replaces = [];
  for (const line of lines) {
    if (block) {
      if (line === ')') { block = ''; continue; }
    }
    let directive = block;
    let body = line;
    const head = line.match(/^(require|replace)\s*(\()?\s*(.*)$/);
    if (!block && head) {
      if (head[2]) { block = head[1]; continue; }
      directive = head[1];
      body = head[3];
    }
    if (!directive || !body) continue;
    if (directive === 'require' && body.split(/\s+/)[0] === SDK_MODULE) required = true;
    if (directive === 'replace') {
      const [left, right] = body.split('=>').map(part => part.trim());
      if (left?.split(/\s+/)[0] === SDK_MODULE) replaces.push(right ?? '');
    }
  }
  if (!required) {
    return replaces.length ? [`replaces ${SDK_MODULE} without requiring it`] : [];
  }
  if (replaces.length !== 1) return [`requires ${SDK_MODULE} but has ${replaces.length} replace directives (need exactly "${SDK_MODULE} => ${expected}")`];
  if (replaces[0] !== expected) return [`replaces ${SDK_MODULE} with "${replaces[0]}", expected "${expected}"`];
  return [];
}

async function tidyDiffSupported() {
  const { output } = await run(['help', 'mod', 'tidy'], root);
  return /-diff\b/.test(output);
}

// Match release build markers instead of inheriting a possibly disabled CGO.
export async function pluginTestEnv(dir, sdkLike = false) {
  const cgo = sdkLike ? { CGO_ENABLED: '1' } : await goCgoEnv(dir, await goTarget(env), env);
  // go test runs a temporary executable, not the packaged plugin binary. Point
  // rendering fixtures at the same SHA-256/OFL-verified fonts as the release
  // build; missing assets must fail normally, never silently skip rendering.
  if (!sdkLike && resolve(dir) === resolve(root, 'official-plugins/business-card/backend')) {
    return { ...cgo, MINCODE_TEST_FONTS: resolve(dir, '../lib/fonts') };
  }
  return cgo;
}

async function testModule(target, tidyDiff) {
  const sdkLike = target === SDK || target === SDK_CONFORMANCE;
  const dir = target === SDK ? resolve(root, 'packages/plugin-sdk-go')
    : target === SDK_CONFORMANCE ? resolve(root, 'packages/plugin-sdk-go-conformance')
    : resolve(root, 'official-plugins', target, 'backend');
  const result = { target, dir: relative(root, dir), steps: [], problems: [], skipped: '' };
  const gomodPath = resolve(dir, 'go.mod');
  if (!(await exists(gomodPath))) {
    result.skipped = 'no go.mod (uninitialised gitlink checkout?)';
    return result;
  }
  if (!sdkLike) {
    const vendored = VENDORED_SDK_CONSUMERS.has(target);
    for (const problem of checkSdkReplace(await readFile(gomodPath, 'utf8'), { vendored })) result.problems.push(`go.mod: ${problem}`);
    if (vendored) {
      for (const problem of await checkTarget(dir, await plan())) {
        result.problems.push(`vendored SDK drift: ${problem} (run node tools/plugin-sdk/sync-go-sdk.mjs)`);
      }
    }
  }
  const tags = sdkLike ? [] : await goBuildTags(dir);
  const moduleEnv = await pluginTestEnv(dir, sdkLike);
  const steps = [
    ['vet', ['vet', ...tags, './...'], moduleEnv],
    ['test', sdkLike ? ['test', '-race', '-count=1', './...'] : ['test', '-count=1', ...tags, './...'], moduleEnv],
  ];
  if (tidyDiff) steps.push(['tidy', ['mod', 'tidy', '-diff']]);
  for (const [name, args, extra] of steps) {
    const started = Date.now();
    const { ok, output } = await run(args, dir, extra);
    result.steps.push({ name, ok, output, ms: Date.now() - started });
    if (!ok && name === 'vet') break; // go test would repeat the same compile errors
  }
  return result;
}

async function pool(items, limit, fn) {
  const results = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const i = next++;
      results[i] = await fn(items[i]);
    }
  }));
  return results;
}

async function main(args) {
  const known = [SDK, SDK_CONFORMANCE, ...goPlugins];
  const unknown = args.filter(arg => !known.includes(arg));
  if (unknown.length) {
    console.error(`Unknown target(s): ${unknown.join(', ')}\nUsage: test-go-plugins.mjs [${SDK}|${SDK_CONFORMANCE}|<go-plugin>...]; known Go plugins: ${goPlugins.join(', ')}`);
    return 2;
  }
  const targets = args.length ? [...new Set(args)] : known;
  const explicit = args.length > 0;
  const tidyDiff = await tidyDiffSupported();
  if (!tidyDiff) console.log('go mod tidy -diff is not supported by this Go toolchain; skipping tidy checks');
  const jobs = Math.max(1, Math.min(4, availableParallelism() - 1));
  const started = Date.now();
  const results = await pool(targets, jobs, async target => {
    const r = await testModule(target, tidyDiff);
    const failed = r.problems.length || r.steps.some(step => !step.ok);
    const label = r.skipped ? `skip (${r.skipped})` : failed ? 'FAIL' : 'ok';
    console.log(`${label.padEnd(4)} ${target}${r.steps.length ? ` (${r.steps.map(s => `${s.name} ${s.ok ? 'ok' : 'FAIL'}`).join(', ')})` : ''}`);
    return r;
  });

  let failures = 0;
  for (const r of results) {
    const failedSteps = r.steps.filter(step => !step.ok);
    if (r.skipped && explicit) failures++;
    if (!r.problems.length && !failedSteps.length) continue;
    failures++;
    console.log(`\n=== ${r.target} (${r.dir}) ===`);
    for (const problem of r.problems) console.log(`guard: ${problem}`);
    for (const step of failedSteps) {
      const lines = step.output.split('\n');
      const shown = lines.length > 60 ? [...lines.slice(0, 60), `... (${lines.length - 60} more lines)`] : lines;
      console.log(`--- go ${step.name} ---\n${shown.join('\n')}`);
    }
  }
  const skipped = results.filter(r => r.skipped).map(r => r.target);
  const passed = results.length - failures - (explicit ? 0 : skipped.length);
  console.log(`\nGo modules: ${passed} passed, ${failures} failed${skipped.length ? `, ${skipped.length} skipped (${skipped.join(', ')})` : ''} in ${((Date.now() - started) / 1000).toFixed(1)}s`);
  if (failures) {
    console.log(`Failed: ${results.filter(r => (r.skipped && explicit) || r.problems.length || r.steps.some(s => !s.ok)).map(r => r.target).join(', ')}`);
  }
  return failures ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main(process.argv.slice(2));
}
