import { goTarget } from './build-go-plugins.mjs';
import { lstat, mkdir, mkdtemp, open, readdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { deflateRawSync } from 'node:zlib';
const root = resolve(process.env.PLUGIN_PACKAGE_SOURCE || fileURLToPath(new URL('../../official-plugins/', import.meta.url)));
const out = resolve(process.env.PLUGIN_PACKAGE_OUT || fileURLToPath(new URL('../../dist/plugins/', import.meta.url)));
const { GOOS: platform, GOARCH: arch } = await goTarget();
// Package members match the CLI and the host (tools/mincode-cli contributionPackageDirs):
// the base set plus fileRef.dirs and assets.dirs of every enabled contribution point.
const contract = JSON.parse(await readFile(fileURLToPath(new URL('../../plugins/api/contribution-points.json', import.meta.url)), 'utf8'));
const packageMembers = new Set(['plugin.json', 'ui', 'assets', 'lib', 'models', 'bin']);
for (const point of Object.values(contract.points ?? {})) {
  if (point.status !== 'enabled') continue;
  for (const dir of [...(point.fileRef?.dirs ?? []), ...(point.assets?.dirs ?? [])]) packageMembers.add(dir);
}
const args = process.argv.slice(2);
const requested = new Set(args.filter(arg => arg !== '--no-build'));
// Packaging ships ui/ and bin/ as found on disk. Rebuild them from source first
// so a package can never carry stale UI or backend output; --no-build is for
// callers that have just built them (CI) or package prebuilt fixtures.
if (!args.includes('--no-build') && !process.env.PLUGIN_PACKAGE_SOURCE) {
  const { buildPluginUI } = await import('./build-plugin-ui.mjs');
  await buildPluginUI([...requested]);
}
function crc32(data) { let crc = 0xffffffff; for (const byte of data) { crc ^= byte; for (let i=0;i<8;i++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0); } return (crc ^ 0xffffffff) >>> 0; }
// ZIP with fixed timestamps, deflate and Unix modes; no external zip executable.
function zip(files) {
  const local=[], central=[]; let offset=0;
  for (const { name, data, executable } of files) {
    const path=Buffer.from(name), packed=deflateRawSync(data), crc=crc32(data);
    const header=Buffer.alloc(30); header.writeUInt32LE(0x04034b50); header.writeUInt16LE(20,4); header.writeUInt16LE(0x800,6); header.writeUInt16LE(8,8); header.writeUInt16LE(33,12); header.writeUInt32LE(crc,14); header.writeUInt32LE(packed.length,18); header.writeUInt32LE(data.length,22); header.writeUInt16LE(path.length,26);
    const c=Buffer.alloc(46); c.writeUInt32LE(0x02014b50); c.writeUInt16LE(0x314,4); c.writeUInt16LE(20,6); c.writeUInt16LE(0x800,8); c.writeUInt16LE(8,10); c.writeUInt16LE(33,14); c.writeUInt32LE(crc,16); c.writeUInt32LE(packed.length,20); c.writeUInt32LE(data.length,24); c.writeUInt16LE(path.length,28); c.writeUInt32LE(((executable?0o100755:0o100644)*65536)>>>0,38); c.writeUInt32LE(offset,42);
    local.push(header,path,packed); central.push(c,path); offset+=header.length+path.length+packed.length;
  }
  const directory=Buffer.concat(central), end=Buffer.alloc(22); end.writeUInt32LE(0x06054b50); end.writeUInt16LE(files.length,8); end.writeUInt16LE(files.length,10); end.writeUInt32LE(directory.length,12); end.writeUInt32LE(offset,16);
  return Buffer.concat([...local,directory,end]);
}
// Windows indexers may hold a just-created directory briefly. Retry only a
// Windows lock-related rename with an absent destination; never overwrite it.
async function publishRename(source, destination) {
  for (let attempt = 0; ; attempt++) {
    try { await lstat(destination); throw Error(`Publication target already exists: ${destination}`); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    try { await rename(source, destination); return; }
    catch (error) {
      if (process.platform !== 'win32' || !['EPERM', 'EACCES', 'EBUSY'].includes(error.code) || attempt >= 7) throw error;
      await new Promise(done => setTimeout(done, (attempt + 1) * 100));
    }
  }
}
function sameFile(a, b) { return a.dev === b.dev && a.ino === b.ino; }
async function verifyDirectories(directories) {
  for (const { path, info } of directories) {
    const current = await lstat(path);
    if (!current.isDirectory() || !sameFile(info, current)) throw Error(`Directory changed during packaging: ${path}`);
  }
}
async function regularFile(path, name, limit = 64 * 1024 * 1024, directories = []) {
  await verifyDirectories(directories);
  const before = await lstat(path);
  if (!before.isFile()) throw Error(`Non-regular file or symlink rejected: ${name}`);
  // NOFOLLOW prevents a replacement symlink from being followed; NONBLOCK avoids waiting on a replacement FIFO.
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const opened = await handle.stat();
    if (!opened.isFile() || !sameFile(before, opened)) throw Error(`File changed during packaging: ${name}`);
    if (opened.size > limit) throw Error(`Package exceeds host 64 MiB limit: ${name}`);
    const data = await handle.readFile();
    const after = await lstat(path);
    await verifyDirectories(directories);
    if (!after.isFile() || !sameFile(opened, after) || data.length > limit || data.length !== opened.size) {
      throw Error(`File changed during packaging: ${name}`);
    }
    return data;
  } finally { await handle.close(); }
}
async function directoryEntries(path, name) {
  const before = await lstat(path);
  if (!before.isDirectory()) throw Error(`Non-directory or symlink rejected: ${name}`);
  const entries = await readdir(path, { withFileTypes: true });
  const after = await lstat(path);
  if (!after.isDirectory() || !sameFile(before, after)) throw Error(`Directory changed during packaging: ${name}`);
  return entries.sort((a, b) => a.name.localeCompare(b.name));
}
async function writeDirectory(path, files) {
  await mkdir(path,{recursive:true});
  for (const file of files) {
    const destination=join(path,file.name);
    await mkdir(dirname(destination),{recursive:true});
    await writeFile(destination,file.data,{mode:file.executable?0o755:0o644});
  }
}
await mkdir(out,{recursive:true});
const catalogName=`catalog-${platform}-${arch}.json`;
const previousCatalog = await readFile(join(out,catalogName),'utf8')
  .then(JSON.parse).catch(error => { if (error.code === 'ENOENT') return { packages: [] }; throw error; });
const previousPackages = Array.isArray(previousCatalog.packages) ? previousCatalog.packages : [];
const stage = await mkdtemp(join(out,'.package-stage-'));
let restored = true;
try {
const catalog=[];
const selected = new Set();
const sourceDirectory = await lstat(root);
if (!sourceDirectory.isDirectory()) throw Error(`Non-directory or symlink rejected: ${root}`);
for (const entry of await directoryEntries(root, root)) {
  if (entry.isSymbolicLink()) throw Error(`Symlink rejected: ${entry.name}`);
  if (!entry.isDirectory()) {
    if (!entry.isFile()) throw Error(`Non-regular entry rejected: ${entry.name}`);
    continue;
  }
  if (requested.size && !requested.has(entry.name)) continue;
  const dir=join(root,entry.name);
  const pluginDirectory = await lstat(dir);
  if (!pluginDirectory.isDirectory()) throw Error(`Non-directory or symlink rejected: ${entry.name}`);
  const baseDirectories = [{ path: root, info: sourceDirectory }, { path: dir, info: pluginDirectory }];
  const manifest = await regularFile(join(dir,'plugin.json'),`${entry.name}/plugin.json`,64*1024*1024,baseDirectories).then(data => JSON.parse(data.toString('utf8'))).catch(error => {
    if (error.code === 'ENOENT') return null;
    throw error;
  });
  const directoryAfterManifest = await lstat(dir);
  if (!directoryAfterManifest.isDirectory() || !sameFile(pluginDirectory, directoryAfterManifest)) throw Error(`Directory changed during packaging: ${entry.name}`);
  if (!manifest || !manifest.platforms.includes(platform)) continue;
  if (manifest.id !== entry.name || !/^[a-z0-9][a-z0-9._-]{1,127}$/.test(manifest.id)
    || !/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(manifest.version)) throw Error(`Invalid package identity: ${entry.name}`);
  // Declarative (data-only) packages have no runtime binary: no bin/, no build-go-plugins entry.
  const declarative = manifest.runtime?.type === 'declarative';
  const runtime = declarative ? null : manifest.runtime.entrypoints?.[platform] || manifest.runtime.entry;
  const files=[];
  let bytes=0;
  async function collect(relative, ancestors = baseDirectories) {
    await verifyDirectories(ancestors);
    const directory = join(dir, relative);
    const before = relative ? await lstat(directory) : pluginDirectory;
    if (!before.isDirectory()) throw Error(`Non-directory or symlink rejected: ${entry.name}/${relative}`);
    const directories = relative ? [...ancestors, { path: directory, info: before }] : ancestors;
    for(const item of await directoryEntries(directory, `${entry.name}/${relative}`)) {
      const name=relative?`${relative}/${item.name}`:item.name;
      if(item.isSymbolicLink()) throw Error(`Symlink rejected: ${name}`);
      if(!item.isDirectory() && !item.isFile()) throw Error(`Non-regular file rejected: ${name}`);
      if(!relative && !packageMembers.has(item.name) && !item.name.endsWith('.mjs')) continue;
      if(item.name.endsWith('.test.mjs')) continue;
      if(item.isDirectory()) await collect(name, directories);
      else {
        if(name.startsWith('bin/') && name!==runtime) continue;
        if(files.length >= 2000) throw Error(`Package exceeds host 2000 entry limit: ${entry.name}`);
        const data = await regularFile(join(dir,name),name,64*1024*1024-bytes,directories);
        bytes+=data.length;
        files.push({name,data,executable:name===runtime});
      }
    }
    await verifyDirectories(directories);
  }
  await collect('');
  await verifyDirectories(baseDirectories);
  if (!declarative && !files.some(f => f.name === runtime)) throw Error(`Missing runtime ${entry.name}/${runtime}; build plugins for ${platform}/${arch} first`);
  if (manifest.id === 'draw') {
    for (const required of ['ui/index.html', 'ui/drawio/index.html', 'ui/drawio/js/bootstrap.js', 'ui/drawio/js/main.js', 'ui/drawio/resources/dia.txt', 'ui/drawio/resources/dia_zh.txt', 'ui/THIRD_PARTY_NOTICES.md']) {
      if (!files.some(file => file.name === required)) throw Error(`Draw package missing ${required}; build official plugin UI assets before packaging.`);
    }
  }
  if(files.reduce((sum,f)=>sum+f.data.length,0)>64*1024*1024) throw Error(`Package exceeds host 64 MiB limit: ${entry.name}`);
  const baseName=`${manifest.id}-${manifest.version}-${platform}-${arch}`;
  const name=`${baseName}.min`, data=zip(files);
  if(data.length>64*1024*1024) throw Error(`ZIP exceeds host 64 MiB limit: ${entry.name}`);
  await writeDirectory(join(stage,baseName),files);
  await writeFile(join(stage,name),data);
  selected.add(manifest.id);
  catalog.push({id:manifest.id,version:manifest.version,platform,arch,file:name,directory:baseName,sha256:createHash('sha256').update(data).digest('hex'),bytes:data.length,unpackedBytes:bytes});
  console.log(`${name}: ${data.length} bytes; directory: ${baseName}`);
}
// A catalog is not authority to remove arbitrary paths from the output directory.
const owned = previousPackages.filter(item => item && selected.has(item.id)
  && typeof item.id === 'string' && /^[a-z0-9][a-z0-9._-]{1,127}$/.test(item.id)
  && typeof item.version === 'string' && /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(item.version)
  && item.directory === `${item.id}-${item.version}-${platform}-${arch}`
  && (item.file === `${item.directory}.zip` || item.file === `${item.directory}.min`));
const nextCatalog = [...previousPackages.filter(item => !item || !selected.has(item.id)), ...catalog];
await writeFile(join(stage,catalogName),JSON.stringify({schemaVersion:1,packages:nextCatalog},null,2)+'\n');
const replacing = new Set([...catalog.flatMap(item => [item.directory,item.file]), catalogName]);
const removing = new Set(owned.flatMap(item => [item.directory,item.file]));
const backup = join(stage,'backup');
await mkdir(backup);
const moved = [], published = [];
try {
  for (const name of new Set([...replacing,...removing])) {
    // First publication normally has no previous output. Do not rely on a failed
    // rename as the existence probe: filesystem adapters may wrap its ENOENT.
    // lstat intentionally includes dangling symlinks; other errors remain fatal.
    try { await lstat(join(out,name)); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    try { await publishRename(join(out,name),join(backup,name)); moved.push(name); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  for (const name of replacing) { await publishRename(join(stage,name),join(out,name)); published.push(name); }
} catch (error) {
  try {
    // Return this attempt's outputs to its own stage instead of deleting them.
    // This preserves evidence and lets rollback restore the previous catalog.
    for (const name of published.reverse()) await publishRename(join(out,name),join(stage,name));
    for (const name of moved.reverse()) await publishRename(join(backup,name),join(out,name));
  } catch (restoreError) {
    restored = false;
    throw new AggregateError([error,restoreError],`Package publication failed; backup retained at ${backup}`, { cause: restoreError });
  }
  throw error;
}
} finally {
  if (restored) await rm(stage,{recursive:true,force:true});
}
