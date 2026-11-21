// Uploads a release set from `pnpm release:plugins` to the marketplace through the admin API, the same
// steps as 插件管理: create the listing and version draft (a new plugin also gets its cover, category,
// tags, README and a free license), upload every platform package, approve the admin draft, check the
// server's SHA-256 values against SHA256SUMS and publish. Runs again safely: a draft or approved version
// left by an interrupted run is continued. A version that is already on the market is skipped or, when
// chosen (asked per plugin, or --overwrite / --skip-existing), has its changed packages replaced: the
// server only replaces packages of a withdrawn version, so it is withdrawn, replaced and put back.
// Versions are immutable otherwise; a new listing for the same plugin needs a new version in plugin.json.
//
//   pnpm --dir vue-app upload:plugins [--dir dist/plugin-release-<time>] [--server URL] [--sync-info]
//                                     [--overwrite | --skip-existing] [--no-publish] [--dry-run] [--yes]
//                                     [<plugin-id>...]
//
// Sign-in: MINCODE_ADMIN_TOKEN (the batoken of a super administrator, from the browser's admin requests),
// or --user <name> with MINCODE_ADMIN_PASSWORD (only when the admin login captcha is off).
// --sync-info first updates an existing plugin's name, description, icon, homepage, category, tags and
// README to the package's; without it a new version keeps the listing as it is online.
import { createHash, randomBytes } from 'node:crypto';
import { existsSync, readdirSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { basename, dirname, extname, resolve } from 'node:path';
import { createInterface } from 'node:readline/promises';
import { fileURLToPath } from 'node:url';
import { inflateRawSync } from 'node:zlib';
import { RELEASE_TARGETS, marketCategory, releaseNotes } from './release-official-plugins.mjs';

const scripts = dirname(fileURLToPath(import.meta.url));
const repo = resolve(scripts, '../..');
const DEFAULT_SERVER = 'https://api.mincodeai.cn';
const API = '/admin/mincode/marketplacePlugin/';
const COVER_TYPES = { '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp', '.gif': 'image/gif' };

export function parseUploadArgs(argv) {
  const options = { dir: '', server: DEFAULT_SERVER, user: '', ids: [], syncInfo: false, existing: 'ask', publish: true, dryRun: false, yes: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const value = () => {
      const next = argv[++i];
      if (!next || next.startsWith('--')) throw new Error(`${arg} needs a value`);
      return next;
    };
    if (arg === '--dir') options.dir = value();
    else if (arg === '--server') options.server = value().replace(/\/+$/, '');
    else if (arg === '--user') options.user = value();
    else if (arg === '--sync-info') options.syncInfo = true;
    else if (arg === '--overwrite') options.existing = 'overwrite';
    else if (arg === '--skip-existing') options.existing = 'skip';
    else if (arg === '--no-publish') options.publish = false;
    else if (arg === '--dry-run') options.dryRun = true;
    else if (arg === '--yes' || arg === '-y') options.yes = true;
    else if (arg.startsWith('-')) throw new Error(`Unknown option ${arg}`);
    else options.ids.push(arg);
  }
  if (!/^https?:\/\/[^/]+$/.test(options.server)) throw new Error(`Invalid --server ${options.server}`);
  return options;
}

/** The newest dist/plugin-release-<time>/ (names sort by time). */
export function latestReleaseDir(dist) {
  const names = existsSync(dist) ? readdirSync(dist).filter((name) => /^plugin-release-\d{8}-\d{4}$/.test(name)).sort() : [];
  return names.length ? resolve(dist, names.at(-1)) : '';
}

/** Marketplace target of a release package file name, `<id>-<version>-<os>-<arch>.min`. */
export function packageTarget(file, id, version) {
  const prefix = `${id}-${version}-`;
  if (!file.startsWith(prefix) || !file.endsWith('.min')) return '';
  const target = file.slice(prefix.length, -'.min'.length).replace('-', '/');
  return RELEASE_TARGETS.includes(target) ? target : '';
}

/** One file of a ZIP archive (stored or deflated), or null when it is absent. */
export function readZipEntry(zip, name) {
  let end = zip.length - 22;
  while (end >= 0 && zip.readUInt32LE(end) !== 0x06054b50) end--;
  if (end < 0) throw new Error('Not a ZIP archive');
  let offset = zip.readUInt32LE(end + 16);
  for (let count = zip.readUInt16LE(end + 10); count > 0; count--) {
    if (zip.readUInt32LE(offset) !== 0x02014b50) throw new Error('Corrupt ZIP central directory');
    const method = zip.readUInt16LE(offset + 10);
    const packed = zip.readUInt32LE(offset + 20);
    const nameLength = zip.readUInt16LE(offset + 28);
    const local = zip.readUInt32LE(offset + 42);
    const entry = zip.toString('utf8', offset + 46, offset + 46 + nameLength);
    offset += 46 + nameLength + zip.readUInt16LE(offset + 30) + zip.readUInt16LE(offset + 32);
    if (entry !== name) continue;
    const start = local + 30 + zip.readUInt16LE(local + 26) + zip.readUInt16LE(local + 28);
    const data = zip.subarray(start, start + packed);
    if (method === 0) return Buffer.from(data);
    if (method === 8) return inflateRawSync(data);
    throw new Error(`Unsupported ZIP method ${method} for ${name}`);
  }
  return null;
}

/**
 * What to do with an existing listing for `version`: a version already on the market (published, or
 * withdrawn/blocked after publication) is skipped or overwritten, a draft this script (or an administrator)
 * created is continued, an approved one is published, otherwise a new draft is created. A listing owned
 * by a member keeps its owner (插件管理 can draft for them; 转移归属 there makes it official).
 */
export function releaseAction(detail, version) {
  if (!detail) return { step: 'new-plugin' };
  const release = detail.releases.find((row) => row.version === version);
  if (!release) return { step: 'new-version' };
  if (release.status === 'published' || (release.published_at && ['withdrawn', 'blocked'].includes(release.status))) return { step: 'exists', release };
  if (release.status === 'approved' && release.review_status === 'approved') return { step: 'publish', release };
  if (release.status === 'draft' && release.review_status === 'not_submitted' && release.created_by_admin) return { step: 'upload', release };
  return { step: 'error', reason: `版本 ${version} 当前状态为 ${release.status}/${release.review_status}，请在插件管理中处理` };
}

/** Packages still to upload; an online package that differs from the local one is an error. */
export function missingPackages(release, packages) {
  const online = new Map((release?.artifacts ?? []).map((row) => [`${row.os}/${row.arch}`, row]));
  for (const [target, row] of online) {
    const local = packages.find((item) => item.target === target);
    if (!local) throw new Error(`线上有本地没有的 ${target} 包，请在插件管理中删除后重试`);
    if (row.sha256 !== local.sha256) throw new Error(`线上 ${target} 包与本地不同（${row.sha256.slice(0, 12)}…），请在插件管理中删除或替换后重试`);
  }
  return packages.filter((item) => !online.has(item.target));
}

/**
 * Replacing the packages of a version already on the market: the packages that differ, how many are the same,
 * local targets the version never had (a published version takes no new targets) and online-only targets (kept).
 */
export function overwritePlan(release, packages) {
  const online = new Map(release.artifacts.map((row) => [`${row.os}/${row.arch}`, row]));
  const replace = [], missing = [];
  let same = 0;
  for (const item of packages) {
    const artifact = online.get(item.target);
    if (!artifact) missing.push(item.target);
    else if (artifact.sha256 === item.sha256) same++;
    else replace.push({ artifact, item });
  }
  const extra = [...online.keys()].filter((target) => !packages.some((item) => item.target === target));
  return { replace, same, missing, extra };
}

/** BuildAdmin save name for a direct OSS upload (web/src/components/mixins/baUpload.ts getSaveName). */
export function ossSaveName(template, file, sha1, now = new Date()) {
  const suffix = file.slice(file.lastIndexOf('.') + 1);
  const pad = (value) => String(value).padStart(2, '0');
  const values = {
    '{topic}': 'default', '{year}': now.getFullYear(), '{mon}': pad(now.getMonth() + 1), '{day}': pad(now.getDate()),
    '{hour}': now.getHours(), '{min}': now.getMinutes(), '{sec}': now.getSeconds(),
    '{random}': randomBytes(8).toString('hex'), '{random32}': randomBytes(16).toString('hex'),
    '{fileName}': file.slice(0, file.lastIndexOf('.')).slice(0, 15), '{suffix}': suffix, '{.suffix}': `.${suffix}`, '{fileSha1}': sha1,
  };
  return template.replace(/^\//, '').replace(new RegExp(Object.keys(values).join('|'), 'g'), (key) => values[key])
    .replace(/[\s:@#?&=',+]+/gu, '');
}

const sha256 = (data) => createHash('sha256').update(data).digest('hex');

/** Packages, manifest and listing text of one plugin folder of the release set. */
export async function loadPlugin(dir, id, sourceRoot = resolve(process.env.PLUGIN_PACKAGE_SOURCE || resolve(repo, 'official-plugins'))) {
  const files = readdirSync(dir);
  const sums = new Map((await readFile(resolve(dir, 'SHA256SUMS'), 'utf8')).trim().split('\n')
    .map((line) => line.trim().split(/\s+/)).map(([digest, file]) => [file, digest]));
  const packages = [];
  let manifest = null;
  for (const file of files.filter((name) => name.endsWith('.min')).sort()) {
    const data = await readFile(resolve(dir, file));
    const digest = sha256(data);
    if (sums.get(file) !== digest) throw new Error(`${file} 与 SHA256SUMS 不一致`);
    const packed = JSON.parse(readZipEntry(data, 'plugin.json')?.toString('utf8') ?? 'null');
    if (!packed || packed.id !== id) throw new Error(`${file} 中的 plugin.json 不属于 ${id}`);
    if (manifest && packed.version !== manifest.version) throw new Error(`${file} 的版本 ${packed.version} 与其他包不同`);
    manifest ??= packed;
    const target = packageTarget(file, id, packed.version);
    if (!target) throw new Error(`无法从文件名 ${file} 识别平台`);
    if (packages.some((item) => item.target === target)) throw new Error(`${target} 有多个包`);
    packages.push({ file, target, data, sha256: digest });
  }
  if (!manifest) throw new Error('没有 .min 安装包');
  const changelog = resolve(sourceRoot, id, 'CHANGELOG.md');
  const cover = files.find((name) => /^cover\./.test(name) && COVER_TYPES[extname(name).toLowerCase()]);
  return {
    id, manifest, packages,
    notes: existsSync(changelog) ? releaseNotes(await readFile(changelog, 'utf8'), manifest.version) : '',
    readme: files.includes('README.md') ? await readFile(resolve(dir, 'README.md'), 'utf8') : '',
    cover: cover ? resolve(dir, cover) : '',
    category: marketCategory(id, manifest),
  };
}

/** Super-administrator client for the Cloud admin API (also used by tools/release/upload-desktop.mjs). */
export class Admin {
  constructor(server, token) {
    this.server = server;
    this.token = token;
  }

  static async signIn(server, user) {
    if (process.env.MINCODE_ADMIN_TOKEN) return new Admin(server, process.env.MINCODE_ADMIN_TOKEN);
    if (!user && process.stdin.isTTY) {
      const token = (await ask('粘贴超级管理员的 batoken（浏览器开发者工具里后台请求的请求头）：')).trim();
      if (token) return new Admin(server, token);
    }
    if (!user || !process.env.MINCODE_ADMIN_PASSWORD) {
      throw new Error('请设置 MINCODE_ADMIN_TOKEN（超级管理员的 batoken），或用 --user 并设置 MINCODE_ADMIN_PASSWORD');
    }
    const admin = new Admin(server, '');
    const state = await admin.request('GET', '/admin/index/login');
    if (state.captcha) throw new Error('后台登录开启了验证码，请改用 MINCODE_ADMIN_TOKEN');
    const result = await admin.request('POST', '/admin/index/login', { username: user, password: process.env.MINCODE_ADMIN_PASSWORD, keep: false });
    admin.token = result.userInfo?.token ?? '';
    if (!admin.token) throw new Error('登录没有返回 token');
    return admin;
  }

  async request(method, path, body, query, { emptyOk = false } = {}) {
    const url = new URL(path, this.server);
    for (const [key, value] of Object.entries(query ?? {})) url.searchParams.set(key, String(value));
    const headers = { server: 'true', 'think-lang': 'zh-cn', ...(this.token ? { batoken: this.token } : {}) };
    let payload;
    if (body instanceof FormData) payload = body;
    else if (body !== undefined) {
      headers['content-type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    let response;
    try {
      response = await fetch(url, { method, headers, body: payload });
    } catch (error) {
      throw new Error(`无法连接 ${url.origin}：${error.cause?.message ?? error.message}`);
    }
    const text = await response.text();
    if (emptyOk && response.ok && text.trim() === '') return {};
    let json;
    try { json = JSON.parse(text); } catch { throw new Error(`${method} ${url.pathname} 返回 HTTP ${response.status}：${text.slice(0, 200)}`); }
    if (json.code !== 1) throw new Error(`${url.pathname}：${json.msg || `code ${json.code}`}`);
    return json.data ?? {};
  }

  api(action, body) {
    return body === undefined || body.query ? this.request('GET', API + action, undefined, body?.query) : this.request('POST', API + action, body);
  }

  async findPlugin(id) {
    const page = await this.api('index', { query: { keyword: id, limit: 50 } });
    const row = (page.items ?? []).find((item) => item.product_key === id);
    return row ? this.api('detail', { query: { product_id: row.id } }) : null;
  }

  /** Uploads a cover image the way the admin cover field does and returns its public HTTPS URL. */
  async uploadCover(path) {
    const data = await readFile(path);
    const name = basename(path);
    const type = COVER_TYPES[extname(name).toLowerCase()];
    const { siteConfig } = await this.request('GET', '/admin/index/index');
    const upload = siteConfig.upload;
    let url;
    if (upload.mode === 'local') {
      const form = new FormData();
      form.append('file', new Blob([data], { type }), name);
      url = (await this.request('POST', '/admin/ajax/upload', form)).file?.full_url ?? '';
    } else {
      const sha1 = createHash('sha1').update(data).digest('hex');
      const key = ossSaveName(upload.saveName, name, sha1);
      const form = new FormData();
      form.append('key', key);
      for (const [field, value] of Object.entries(upload.params ?? {})) form.append(field, String(value));
      form.append('file', new Blob([data], { type }), name);
      const response = await fetch(upload.url, { method: 'POST', body: form });
      if (!response.ok) throw new Error(`封面上传到存储失败：HTTP ${response.status} ${(await response.text()).slice(0, 200)}`);
      // Records the attachment; BuildAdmin answers it with an empty 200 (the admin web client ignores the reply).
      await this.request('POST', '/admin/Alioss/callback', { url: `/${key}`, name, size: data.length, type, sha1 }, undefined, { emptyOk: true });
      url = `${siteConfig.cdnUrl.replace(/\/+$/, '')}/${key}`;
    }
    if (!url.startsWith('https://')) throw new Error(`封面地址不是 HTTPS：${url}`);
    return url;
  }
}

export async function ask(question) {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try { return await rl.question(question); } finally { rl.close(); }
}

const confirm = async (question) => /^y(es)?$/i.test((await ask(question)).trim());

/**
 * A listing without a cover (drafts from the official-plugins SQL start with none) gets the release set's cover;
 * everything else on the listing stays as it is online.
 */
async function ensureCover(admin, label, detail, plugin) {
  const product = detail.product;
  if (product.cover_url || !plugin.cover) return detail;
  const cover = await admin.uploadCover(plugin.cover);
  await admin.api('updateProduct', {
    product_id: product.id, name: product.name, description: product.description, icon: product.icon ?? '',
    homepage: product.homepage ?? '', cover_url: cover, category: product.category, tags: product.tags ?? [],
  });
  console.log(`${label}：线上没有封面，已上传 ${cover}`);
  return admin.api('detail', { query: { product_id: product.id } });
}

async function overwritePackages(admin, label, release, packages) {
  const plan = overwritePlan(release, packages);
  if (plan.missing.length) console.log(`${label}：线上此版本没有 ${plan.missing.join(', ')}，已发布的版本不能补平台，这些包未上传（需新版本号）`);
  if (plan.extra.length) console.log(`${label}：本地没有 ${plan.extra.join(', ')}，线上这些包保持不变`);
  if (!plan.replace.length) return console.log(`${label}：安装包与线上一致，无需覆盖`);
  const republish = release.status === 'published';
  if (republish) {
    await admin.api('setReleaseStatus', { release_id: release.id, status: 'withdrawn', reason: '覆盖安装包（upload-official-plugins）' });
    console.log(`${label}：已临时撤回`);
  }
  try {
    for (const { artifact, item } of plan.replace) {
      const form = new FormData();
      form.append('artifact_id', String(artifact.id));
      form.append('file', new Blob([item.data], { type: 'application/zip' }), item.file);
      await admin.request('POST', `${API}replaceArtifact`, form);
      console.log(`${label}：已覆盖 ${item.target}`);
    }
  } finally {
    // Put the version back on the market even when a replacement failed; the replaced ones stay replaced.
    if (republish) {
      await admin.api('setReleaseStatus', { release_id: release.id, status: 'published', reason: '' });
      console.log(`${label}：已重新上架`);
    }
  }
  if (!republish) console.log(`${label}：此版本线上状态为 ${release.status}，覆盖后保持不变`);
  console.log(`${label}：覆盖完成（${plan.replace.length} 个平台${plan.same ? `，${plan.same} 个与线上一致未动` : ''}）`);
}

async function uploadPlugin(admin, plugin, options) {
  const { id, manifest, packages } = plugin;
  const label = `${id} ${manifest.version}`;
  let detail = await admin.findPlugin(id);
  let action = releaseAction(detail, manifest.version);
  if (action.step === 'error') throw new Error(action.reason);
  if (detail) detail = await ensureCover(admin, label, detail, plugin);
  if (action.step === 'exists') {
    if (plugin.choice !== 'overwrite') return console.log(`${label}：线上已有此版本，跳过`);
    return overwritePackages(admin, label, action.release, packages);
  }

  let createdNow = false;
  if (action.step === 'new-plugin') {
    createdNow = true;
    const cover = plugin.cover ? await admin.uploadCover(plugin.cover) : '';
    if (cover) console.log(`${label}：封面已上传 ${cover}`);
    const draft = await admin.api('draft', {
      product_key: id, name: manifest.name, version: manifest.version, description: manifest.description ?? '',
      manifest_api: manifest.apiVersion, release_notes: plugin.notes, homepage: manifest.homepage ?? '', icon: manifest.icon ?? '',
      cover_url: cover, category: plugin.category, tags: manifest.tags ?? [], readme: plugin.readme,
    });
    // freeLicenseEnsure is the alias of ensureFreeLicense: production nginx blocks URLs ending in "license".
    await admin.api('freeLicenseEnsure', { product_id: draft.product_id });
    console.log(`${label}：已创建插件、版本草稿和免费许可`);
  } else if (action.step === 'new-version') {
    const product = detail.product;
    if (options.syncInfo) {
      await admin.api('updateProduct', {
        product_id: product.id, name: manifest.name, description: manifest.description ?? '', icon: manifest.icon ?? '',
        homepage: manifest.homepage ?? '', cover_url: product.cover_url ?? '', category: plugin.category,
        tags: manifest.tags ?? [], readme: plugin.readme,
      });
      detail = await admin.api('detail', { query: { product_id: product.id } });
      console.log(`${label}：已按插件包更新线上资料`);
    } else if (product.name !== manifest.name || product.description !== (manifest.description ?? '')) {
      console.log(`${label}：插件包的名称或简介与线上不同，线上资料保持不变（加 --sync-info 可更新）`);
    }
    await admin.api('draft', {
      product_key: id, name: detail.product.name, version: manifest.version, description: detail.product.description,
      manifest_api: manifest.apiVersion, release_notes: plugin.notes,
      ...(detail.product.owner_user_id ? { owner_user_id: detail.product.owner_user_id } : {}),
    });
    console.log(`${label}：已创建版本草稿`);
  }

  detail = await admin.api('detail', { query: { product_id: (await admin.findPlugin(id)).product.id } });
  action = releaseAction(detail, manifest.version);
  // A run that stopped after creating a new plugin (before its free license) continues here: the plugin's only
  // version is still this unpublished one, so make sure it has the free license (the server call is idempotent).
  if (!createdNow && action.step === 'upload' && (detail.releases ?? []).length === 1 && !(detail.licenses ?? []).length) {
    await admin.api('freeLicenseEnsure', { product_id: detail.product.id });
    console.log(`${label}：已补建免费许可`);
  }
  const release = action.release;
  if (action.step === 'upload') {
    // Drafts made earlier (by hand or the official-plugins SQL) may carry older notes than CHANGELOG.md.
    if (plugin.notes && (release.release_notes ?? '') !== plugin.notes) {
      await admin.api('updateDraftNotes', { release_id: release.id, release_notes: plugin.notes });
      console.log(`${label}：已按 CHANGELOG 更新发布说明`);
    }
    for (const item of missingPackages(release, packages)) {
      const [os, arch] = item.target.split('/');
      const form = new FormData();
      form.append('release_id', String(release.id));
      form.append('os', os);
      form.append('arch', arch);
      form.append('file', new Blob([item.data], { type: 'application/zip' }), item.file);
      await admin.request('POST', `${API}upload`, form);
      console.log(`${label}：已上传 ${item.target}`);
    }
    await admin.api('approve', { release_id: release.id });
    console.log(`${label}：已审核通过`);
  } else if (action.step !== 'publish') throw new Error(`创建草稿后状态异常：${action.step}`);

  const preview = await admin.api('publicationPreview', { query: { release_id: release.id } });
  const expected = new Map(packages.map((item) => [item.target, item.sha256]));
  for (const row of preview.artifacts) {
    if (expected.get(`${row.os}/${row.arch}`) !== row.sha256) throw new Error(`服务器上 ${row.os}/${row.arch} 包的 SHA-256 与本地不一致`);
  }
  if (preview.artifacts.length !== packages.length) throw new Error(`服务器上有 ${preview.artifacts.length} 个包，本地有 ${packages.length} 个`);
  if (!options.publish) return console.log(`${label}：已上传并审核，未发布（--no-publish）`);
  await admin.api('publish', {
    release_id: release.id,
    confirmed_json: JSON.stringify(preview.artifacts.map((row) => ({ artifact_id: row.artifact_id, sha256: row.sha256 }))),
  });
  console.log(`${label}：已发布（${packages.length} 个平台）`);
}

export async function main(argv) {
  const options = parseUploadArgs(argv);
  // pnpm runs the script from vue-app/; a relative --dir is relative to where the command was typed.
  const dir = options.dir ? resolve(process.env.INIT_CWD ?? process.cwd(), options.dir) : latestReleaseDir(resolve(repo, 'dist'));
  if (!dir || !existsSync(dir)) throw new Error('找不到发布目录，先运行 pnpm release:plugins，或用 --dir 指定');
  const available = readdirSync(dir).filter((name) => existsSync(resolve(dir, name, 'SHA256SUMS'))).sort();
  const unknown = options.ids.filter((id) => !available.includes(id));
  if (unknown.length) throw new Error(`发布目录中没有：${unknown.join(', ')}`);
  const ids = options.ids.length ? options.ids : available;
  if (!ids.length) throw new Error(`${dir} 中没有插件`);

  const plugins = [];
  for (const id of ids) plugins.push(await loadPlugin(resolve(dir, id), id));
  const admin = await Admin.signIn(options.server, options.user);
  console.log(`发布目录：${dir}\n服务器：${options.server}\n`);
  for (const plugin of plugins) {
    const detail = await admin.findPlugin(plugin.id);
    const action = releaseAction(detail, plugin.manifest.version);
    let step = { 'new-plugin': '新插件', 'new-version': '新版本', upload: '继续上传草稿', publish: '发布已审核版本', error: `无法处理：${action.reason}` }[action.step];
    if (action.step === 'exists') {
      const plan = overwritePlan(action.release, plugin.packages);
      plugin.overwritable = plan.replace.length > 0;
      step = plugin.overwritable ? `线上已有此版本，${plan.replace.length} 个平台的包与本地不同` : '线上已有此版本，安装包一致，跳过';
    }
    if (detail && !detail.product.cover_url && plugin.cover) step += '，补上封面';
    const owner = detail?.product.owner_user_id ? `（归属会员 ${detail.product.owner_name ?? detail.product.owner_user_id}）` : '';
    console.log(`  ${plugin.id} ${plugin.manifest.version}  ${plugin.packages.map((item) => item.target).join(', ')}  → ${step}${owner}`);
    if (!plugin.notes && action.step !== 'exists') console.log(`    （CHANGELOG 中没有 ${plugin.manifest.version} 的说明，发布说明为空）`);
  }
  const existing = plugins.filter((plugin) => plugin.overwritable);
  if (existing.length) {
    console.log(`\n${existing.length} 个插件线上已有相同版本。可以覆盖安装包（临时撤回 → 替换 → 重新上架，已安装的用户不会收到更新），`
      + '或跳过；要作为新版本发布，请先提升 plugin.json 的 version 并重新 pnpm release:plugins。');
  }
  if (options.dryRun) return;
  for (const plugin of existing) {
    if (options.existing !== 'ask') plugin.choice = options.existing;
    else if (!process.stdin.isTTY) throw new Error('线上已有相同版本：非交互环境请加 --overwrite 或 --skip-existing');
    else plugin.choice = /^o/i.test((await ask(`${plugin.id} ${plugin.manifest.version}：[o] 覆盖安装包 / [s] 跳过（默认）？ `)).trim()) ? 'overwrite' : 'skip';
  }
  if (!options.yes) {
    if (!process.stdin.isTTY) throw new Error('非交互环境请加 --yes 确认上传');
    if (!await confirm(`\n上传${options.publish ? '并发布' : ''}以上插件？[y/N] `)) return console.log('已取消');
  }
  const failures = [];
  for (const plugin of plugins) {
    try {
      await uploadPlugin(admin, plugin, options);
    } catch (error) {
      failures.push(plugin.id);
      console.error(`${plugin.id}：${error instanceof Error ? error.message : error}`);
    }
  }
  if (failures.length) throw new Error(`\n${failures.length} 个插件失败：${failures.join(', ')}；修正后重新运行即可从中断处继续`);
  console.log(`\n完成：${plugins.length} 个插件`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
