// Builds the marketplace upload set for official plugins: every requested plugin for the four
// marketplace targets, organised by plugin as dist/plugin-release-<time>/<id>/ with the .min
// packages, SHA256SUMS, cover image, README.md and 上传信息.md (the admin form fields).
//
//   pnpm --dir vue-app release:plugins [--targets darwin/arm64,linux/amd64] [<plugin-id>...]
//
// Pure Go plugins cross-compile anywhere. cgo plugins (backend/cgo) need a C toolchain per
// target: clang -arch on macOS for the other macOS architecture, MinGW-w64 for Windows, and,
// from a non-Linux host, a local golang:<go version>-bookworm container for linux/amd64
// (never pulled; GOPROXY=off, so nothing is downloaded). Missing tools fail before building.
import { execFile, spawn } from 'node:child_process';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { chmod, copyFile, mkdir, readdir, readFile, rename, rm, rmdir, stat, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { buildGoPlugins, goBuildTags, goCgo, goPluginAssets, goPlugins } from './build-go-plugins.mjs';

const exec = promisify(execFile);
const scripts = dirname(fileURLToPath(import.meta.url));
const repo = resolve(scripts, '../..');
const pluginsRoot = resolve(repo, 'official-plugins');

export const RELEASE_TARGETS = ['darwin/arm64', 'darwin/amd64', 'windows/amd64', 'linux/amd64'];
const TARGET_LABELS = {
  'darwin/arm64': 'macOS Apple Silicon',
  'darwin/amd64': 'macOS Intel',
  'windows/amd64': 'Windows x64',
  'linux/amd64': 'Linux x64',
};
const marketCategories = JSON.parse(readFileSync(resolve(scripts, 'official-plugin-market-categories.json'), 'utf8')).categories;

export function parseReleaseArgs(argv) {
  let targets = RELEASE_TARGETS;
  const ids = [];
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--targets') {
      targets = (argv[++i] ?? '').split(',').filter(Boolean);
      const unsupported = targets.find((target) => !RELEASE_TARGETS.includes(target));
      if (unsupported || !targets.length) throw new Error(`Unsupported release target "${unsupported ?? ''}"; use ${RELEASE_TARGETS.join(', ')}`);
    } else ids.push(argv[i]);
  }
  return { targets, ids };
}

/**
 * How to build a cgo plugin for `target` on `host`: extra env, the Linux container, or an error.
 * `has(command)` reports whether a command is on PATH.
 */
export function cgoToolchain(target, host, has) {
  if (target.os === host.os && target.arch === host.arch) return { env: {} };
  const clangArch = { amd64: 'x86_64', arm64: 'arm64' }[target.arch];
  if (target.os === 'darwin' && host.os === 'darwin') {
    return has('clang') ? { env: { CC: `clang -arch ${clangArch}`, CXX: `clang++ -arch ${clangArch}` } } : { error: 'needs clang (Xcode command line tools)' };
  }
  if (target.os === 'windows' && target.arch === 'amd64') {
    return has('x86_64-w64-mingw32-gcc')
      ? { env: { CC: 'x86_64-w64-mingw32-gcc', CXX: 'x86_64-w64-mingw32-g++' } }
      : { error: 'needs MinGW-w64 (x86_64-w64-mingw32-gcc on PATH, e.g. brew install mingw-w64)' };
  }
  if (target.os === 'linux' && target.arch === 'amd64') {
    return has('docker') ? { container: true } : { error: 'needs docker with a local golang:<go version>-bookworm linux/amd64 image' };
  }
  return { error: `no known C toolchain for ${target.os}/${target.arch} on ${host.os}/${host.arch}` };
}

// Host install limits for one package (backend/plugin install checks), and how close
// to the size limit a package may get before the release warns. ocr is the plugin near the limit:
// ONNX Runtime + models put darwin/amd64 at about 60.5 MiB.
export const PACKAGE_LIMITS = { bytes: 64 * 1024 * 1024, files: 2000 };
export const PACKAGE_HEADROOM_WARNING = 4 * 1024 * 1024;

const mib = (bytes) => `${(bytes / 1024 / 1024).toFixed(2)} MiB`;

/**
 * Checks one package's uncompressed size and entry count (files plus folders, see countFiles)
 * against the host install limits.
 */
export function packageBudget({ file, unpackedBytes, files }) {
  if (!Number.isSafeInteger(unpackedBytes) || !Number.isSafeInteger(files)) return { error: `${file}: unknown uncompressed size or file count` };
  if (unpackedBytes > PACKAGE_LIMITS.bytes) return { error: `${file}: ${mib(unpackedBytes)} uncompressed exceeds the ${mib(PACKAGE_LIMITS.bytes)} install limit` };
  if (files > PACKAGE_LIMITS.files) return { error: `${file}: ${files} ZIP entries (files and folders) exceed the ${PACKAGE_LIMITS.files} install limit` };
  const headroom = PACKAGE_LIMITS.bytes - unpackedBytes;
  if (headroom < PACKAGE_HEADROOM_WARNING) return { warning: `${file}: ${mib(unpackedBytes)} uncompressed, only ${mib(headroom)} below the install limit` };
  return {};
}

/**
 * Entries a package ZIP of `directory` may hold: every file and every folder below it. The host
 * counts directory records toward the same limit (validatePluginZIPEntries in
 * backend/plugin/install_zip.go), so folders count even when they hold files.
 */
export async function countFiles(directory) {
  let count = 0;
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    count += 1;
    if (entry.isDirectory()) count += await countFiles(resolve(directory, entry.name));
  }
  return count;
}

export function releaseNotes(changelog, version) {
  const escaped = version.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = changelog.match(new RegExp(`^## ${escaped}\\b[^\\n]*\\n([\\s\\S]*?)(?=^## |(?![\\s\\S]))`, 'm'));
  return match ? match[1].trim() : '';
}

/** Marketplace category for a listing: the curated finer category, else the manifest's. */
export function marketCategory(id, manifest) {
  return marketCategories[id] ?? manifest.category ?? 'other';
}

export function uploadInfo({ id, manifest, category, categoryLabel, cover, readme, notes, packages }) {
  const tags = manifest.tags ?? [];
  const manifestNote = category !== manifest.category ? `；插件包内仍为 \`${manifest.category}\`` : '';
  return `# ${manifest.name}

按后台“插件编辑”表单顺序列出，可直接复制。

| 字段 | 填写内容 |
|---|---|
| 插件 ID | \`${id}\` |
| 名称 | ${manifest.name} |
| 版本 | \`${manifest.version}\` |
| 主页 | ${manifest.homepage || '（无）'} |
| 图标 | ${manifest.icon ? `\`${manifest.icon}\`（在图标下拉框中搜索）` : '（未设置）'} |
| 分类 | ${categoryLabel}（\`${category}\`）${manifestNote} |
| 标签 | ${tags.length ? tags.join(', ') : '（无）'} |
| 封面图 | ${cover ? `上传本目录的 \`${cover}\`` : '（插件未提供封面）'} |
| 详情说明 | ${readme ? '本目录的 `README.md`' : '（无）'} |

## 简介

\`\`\`text
${manifest.description ?? ''}
\`\`\`

## 标签（逗号分隔）

\`\`\`text
${tags.join(',')}
\`\`\`

## 发布说明（${manifest.version}）

\`\`\`markdown
${notes || '（CHANGELOG 中没有该版本的说明）'}
\`\`\`

## 安装包

| 平台 | 文件 | 大小（字节） | SHA-256 |
|---|---|---|---|
${packages.map((p) => `| ${p.label} | \`${p.file}\` | ${p.bytes.toLocaleString('en-US')} | \`${p.sha256.slice(0, 16)}…\` |`).join('\n')}
`;
}

async function hasCommand(name) {
  try {
    await exec('/usr/bin/env', ['which', name]);
    return true;
  } catch {
    return false;
  }
}

function run(command, args, options) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { stdio: 'inherit', ...options });
    child.once('error', reject);
    child.once('exit', (code) => (code === 0 ? resolvePromise() : reject(new Error(`${command} ${args[0] ?? ''} exited with ${code}`))));
  });
}

async function sha256(path) {
  return createHash('sha256').update(await readFile(path)).digest('hex');
}

function categoryLabels() {
  const zh = readFileSync(resolve(repo, 'vue-app/src/i18n/locales/zh.ts'), 'utf8');
  return Object.fromEntries([...zh.matchAll(/"plugins\.category\.([\w-]+)": "([^"]+)"/g)].map((m) => [m[1], m[2]]));
}

async function containerBuild(id, image) {
  const backend = resolve(pluginsRoot, id, 'backend');
  const manifest = JSON.parse(await readFile(resolve(pluginsRoot, id, 'plugin.json'), 'utf8'));
  const entry = resolve(pluginsRoot, id, manifest.runtime.entrypoints.linux);
  const temp = resolve(dirname(entry), '.release-linux-build');
  const { stdout } = await exec('go', ['env', 'GOMODCACHE']);
  console.log(`Building ${id} for linux/amd64 (cgo, container ${image})`);
  await mkdir(dirname(entry), { recursive: true });
  // Same flags as build-go-plugins.mjs; the module cache is mounted and GOPROXY=off keeps it offline.
  await run('docker', ['run', '--rm', '--pull', 'never', '--platform', 'linux/amd64',
    '-v', `${repo}:/src`, '-v', `${stdout.trim()}:/go/pkg/mod`, '-w', `/src/official-plugins/${id}/backend`,
    '-e', 'GOWORK=off', '-e', 'CGO_ENABLED=1', '-e', 'GOPROXY=off', image,
    'go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', ...(await goBuildTags(backend)),
    '-o', `/src/official-plugins/${id}/${manifest.runtime.entrypoints.linux.replace(/[^/]+$/, '.release-linux-build')}`, '.']);
  await chmod(temp, 0o755);
  await rename(temp, entry);
  // Same per-target assets as build-go-plugins.mjs (they are prepared on the host).
  const assets = await goPluginAssets(id);
  if (assets) await assets.prepareAssets({ GOOS: 'linux', GOARCH: 'amd64' }, resolve(pluginsRoot, id));
}

async function main(argv) {
  const { targets, ids: requested } = parseReleaseArgs(argv);
  const available = readdirSync(pluginsRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && existsSync(resolve(pluginsRoot, entry.name, 'plugin.json'))).map((entry) => entry.name).sort();
  const unknown = requested.find((id) => !available.includes(id));
  if (unknown) throw new Error(`Unknown official plugin: ${unknown}`);
  const ids = requested.length ? [...new Set(requested)].sort() : available;
  const goIds = ids.filter((id) => goPlugins.includes(id));
  const cgoIds = [];
  for (const id of goIds) if (await goCgo(resolve(pluginsRoot, id, 'backend'))) cgoIds.push(id);
  const pureIds = goIds.filter((id) => !cgoIds.includes(id));

  // Preflight every target before building anything.
  const { stdout: hostJson } = await exec('go', ['env', '-json', 'GOHOSTOS', 'GOHOSTARCH', 'GOVERSION']);
  const goEnv = JSON.parse(hostJson);
  const host = { os: goEnv.GOHOSTOS, arch: goEnv.GOHOSTARCH };
  const image = process.env.MINCODE_PLUGIN_LINUX_IMAGE || `golang:${goEnv.GOVERSION.replace(/^go/, '')}-bookworm`;
  const commands = Object.fromEntries(await Promise.all(['clang', 'x86_64-w64-mingw32-gcc', 'docker'].map(async (name) => [name, await hasCommand(name)])));
  const toolchains = {};
  for (const target of targets) {
    const [os, arch] = target.split('/');
    for (const id of cgoIds) {
      const supported = (await goCgo(resolve(pluginsRoot, id, 'backend'))).targets;
      if (supported.length && !supported.includes(target)) throw new Error(`${id} does not support ${target} (backend/cgo)`);
    }
    toolchains[target] = cgoIds.length ? cgoToolchain({ os, arch }, host, (name) => commands[name]) : { env: {} };
    if (toolchains[target].error) throw new Error(`${cgoIds.join(', ')} for ${target}: ${toolchains[target].error}`);
    if (toolchains[target].container) {
      await run('docker', ['run', '--rm', '--pull', 'never', '--platform', 'linux/amd64', image, 'true'], { stdio: 'ignore' })
        .catch(() => { throw new Error(`linux/amd64 cgo builds need the local image ${image} (set MINCODE_PLUGIN_LINUX_IMAGE to use another)`); });
    }
  }

  const now = new Date();
  const stamp = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}-${String(now.getHours()).padStart(2, '0')}${String(now.getMinutes()).padStart(2, '0')}`;
  const out = resolve(repo, 'dist', `plugin-release-${stamp}`);
  if (existsSync(out)) throw new Error(`${out} already exists`);
  await mkdir(out, { recursive: true });

  try {
    await buildRelease({ out, ids, requested, targets, pureIds, cgoIds, toolchains, image });
  } catch (error) {
    // Leave no empty release directory behind; a partial one is kept for its logs and packages.
    if (!readdirSync(out).length) await rmdir(out);
    throw error;
  }
}

// Each target is built into the plugin's own bin/, so the loop ends with the
// last target's binary there (linux/amd64), and installing the plugin from its
// source folder on this machine fails. Rebuild for the host afterwards, even
// when a target failed; a failure here is reported without hiding the first one.
async function restoreHostBinaries(ids) {
  if (!ids.length) return;
  const env = { ...process.env };
  delete env.GOOS;
  delete env.GOARCH;
  try {
    await buildGoPlugins(ids, { env });
  } catch (error) {
    console.warn(`Could not rebuild bin/ for this machine; run pnpm --dir vue-app build:plugin-go ${ids.join(' ')}: ${error?.message ?? error}`);
  }
}

async function buildRelease({ out, ids, requested, targets, pureIds, cgoIds, toolchains, image }) {
  // Kept after a failure so the per-platform packages and catalogs can be inspected.
  const staging = resolve(out, '.platforms');
  // Declarative plugins have neither UI nor backend; an empty list would mean "build every UI".
  const builtIds = ids.filter((id) => goPlugins.includes(id) || existsSync(resolve(pluginsRoot, id, 'ui-src', 'entry.js')));
  if (!requested.length || builtIds.length) {
    const { buildPluginUI } = await import('./build-plugin-ui.mjs');
    await buildPluginUI(requested.length ? builtIds : []);
  }
  try {
    for (const target of targets) {
      const [GOOS, GOARCH] = target.split('/');
      const env = { ...process.env, GOOS, GOARCH };
      await buildGoPlugins(pureIds, { env });
      const toolchain = toolchains[target];
      for (const id of cgoIds) {
        if (toolchain.container) await containerBuild(id, image);
        else await buildGoPlugins([id], { env: { ...env, ...toolchain.env } });
      }
      await run(process.execPath, [resolve(scripts, 'package-official-plugins.mjs'), '--no-build', ...(requested.length ? ids : [])], {
        env: { ...env, PLUGIN_PACKAGE_OUT: resolve(staging, `${GOOS}-${GOARCH}`) },
      });
    }
  } finally {
    await restoreHostBinaries([...pureIds, ...cgoIds]);
  }

  const labels = categoryLabels();
  const catalogs = Object.fromEntries(await Promise.all(targets.map(async (target) => {
    const tag = target.replace('/', '-');
    const catalog = JSON.parse(await readFile(resolve(staging, tag, `catalog-${tag}.json`), 'utf8'));
    return [target, Object.fromEntries(catalog.packages.map((item) => [item.id, item]))];
  })));
  // Budget: every package must stay installable (size and file count), checked before anything is copied.
  const problems = [];
  for (const target of targets) {
    const tag = target.replace('/', '-');
    for (const id of ids) {
      const item = catalogs[target][id];
      if (!item) continue;
      const files = await countFiles(resolve(staging, tag, item.directory));
      const budget = packageBudget({ file: item.file, unpackedBytes: item.unpackedBytes, files });
      console.log(`${item.file}: ${mib(item.unpackedBytes)} uncompressed, ${files} ZIP entries${budget.warning ? ' (near the limit)' : ''}`);
      if (budget.warning) console.warn(`Warning: ${budget.warning}`);
      if (budget.error) problems.push(budget.error);
    }
  }
  if (problems.length) throw new Error(`Packages over the install budget:\n${problems.join('\n')}`);
  const byPlugin = out;
  const rows = [];
  let total = 0;
  for (const id of ids) {
    const source = resolve(pluginsRoot, id);
    const manifest = JSON.parse(await readFile(resolve(source, 'plugin.json'), 'utf8'));
    const dir = resolve(byPlugin, id);
    await mkdir(dir, { recursive: true });
    const packages = [];
    for (const target of targets) {
      const item = catalogs[target][id];
      if (!item) throw new Error(`${id} has no ${target} package`);
      const tag = target.replace('/', '-');
      await copyFile(resolve(staging, tag, item.file), resolve(dir, item.file));
      const digest = await sha256(resolve(dir, item.file));
      if (digest !== item.sha256) throw new Error(`${item.file} changed while copying`);
      packages.push({ label: TARGET_LABELS[target], file: item.file, bytes: (await stat(resolve(dir, item.file))).size, sha256: digest });
    }
    total += packages.length;
    await writeFile(resolve(dir, 'SHA256SUMS'), packages.map((p) => `${p.sha256}  ${p.file}`).join('\n') + '\n');
    let cover = '';
    if (manifest.cover && existsSync(resolve(source, manifest.cover))) {
      cover = `cover${extname(manifest.cover)}`;
      await copyFile(resolve(source, manifest.cover), resolve(dir, cover));
    } else if (manifest.cover) console.warn(`${id}: plugin.json names ${manifest.cover}, which does not exist`);
    const readme = existsSync(resolve(source, 'README.md'));
    if (readme) await copyFile(resolve(source, 'README.md'), resolve(dir, 'README.md'));
    const changelog = existsSync(resolve(source, 'CHANGELOG.md')) ? await readFile(resolve(source, 'CHANGELOG.md'), 'utf8') : '';
    const category = marketCategory(id, manifest);
    await writeFile(resolve(dir, '上传信息.md'), uploadInfo({
      id, manifest, category, categoryLabel: labels[category] ?? category, cover, readme,
      notes: releaseNotes(changelog, manifest.version), packages,
    }));
    rows.push(`| \`${id}\` | ${manifest.name} | ${manifest.version} | ${labels[category] ?? category} | ${cover ? '有' : '无'} | ${packages.length} |`);
  }
  await writeFile(resolve(byPlugin, '上传总览.md'), `# 官方插件上传总览

共 ${ids.length} 个插件、${total} 个安装包（${targets.join('、')}）。每个插件目录内含各平台的 \`.min\`、\`SHA256SUMS\`、封面（如有）、\`README.md\` 和 \`上传信息.md\`。
市场分类来自 \`vue-app/scripts/official-plugin-market-categories.json\`；插件包内 plugin.json 的分类保持原值，以兼容旧版桌面端。

| 插件 ID | 名称 | 版本 | 分类 | 封面 | 平台包 |
|---|---|---|---|---|---|
${rows.join('\n')}
`);
  // Per-platform output was only an intermediate step; the release is organised by plugin.
  await rm(staging, { recursive: true, force: true });
  console.log(`${ids.length} plugins, ${total} packages → ${byPlugin}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
