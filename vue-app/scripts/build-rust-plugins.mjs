import { execFile } from 'node:child_process';
import { existsSync, readdirSync } from 'node:fs';
import { chmod, copyFile, mkdir, readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { goTarget } from './build-go-plugins.mjs';

const exec = promisify(execFile);
const packages = resolve(dirname(fileURLToPath(import.meta.url)), '../../official-plugins');

// Every official plugin with a Rust backend (backend/Cargo.toml). Go backends are
// built by build-go-plugins.mjs; a plugin has one or the other, never both.
export const rustPlugins = readdirSync(packages, { withFileTypes: true })
  .filter(entry => entry.isDirectory() && existsSync(resolve(packages, entry.name, 'backend', 'Cargo.toml')))
  .map(entry => entry.name)
  .sort();

const TRIPLES = {
  'darwin/arm64': 'aarch64-apple-darwin',
  'darwin/amd64': 'x86_64-apple-darwin',
  'linux/amd64': 'x86_64-unknown-linux-gnu',
  'linux/arm64': 'aarch64-unknown-linux-gnu',
  'windows/amd64': 'x86_64-pc-windows-msvc',
  'windows/arm64': 'aarch64-pc-windows-msvc',
};

// Official targets are named like Go's (GOOS/GOARCH) everywhere else in the
// release tooling, so the Rust target is derived from them.
export function rustTarget({ GOOS, GOARCH }) {
  const triple = TRIPLES[`${GOOS}/${GOARCH}`];
  if (!triple) throw new Error(`Unsupported official Rust plugin target: ${GOOS}/${GOARCH}`);
  return { triple, GOOS, GOARCH };
}

async function hostTriple(env) {
  const { stdout } = await exec('rustc', ['-vV'], { env });
  return stdout.match(/^host: (\S+)$/m)?.[1] ?? '';
}

// Builds each requested Rust plugin for the Go-named target into
// <outputPackages>/<name>/<manifest entry>. Cargo.lock is required (--locked).
export async function buildRustPlugins(requested = rustPlugins, { outputPackages = packages, env = process.env } = {}) {
  const names = [...new Set(requested)].filter(name => rustPlugins.includes(name));
  if (!names.length) return names;
  const target = rustTarget(await goTarget(env));
  // A cross target needs its standard library and linker (rustup target add / a cross toolchain).
  const cross = target.triple !== await hostTriple(env);
  for (const name of names) {
    const source = resolve(packages, name);
    const backend = resolve(source, 'backend');
    const manifest = JSON.parse(await readFile(resolve(source, 'plugin.json'), 'utf8'));
    const entry = manifest.runtime.entrypoints[target.GOOS];
    if (!entry) throw new Error(`${name} has no ${target.GOOS} entrypoint`);
    const { stdout } = await exec('cargo', ['metadata', '--format-version', '1', '--no-deps', '--locked'], { cwd: backend, env, maxBuffer: 16 * 1024 * 1024 });
    const binaries = JSON.parse(stdout).packages.flatMap(item => item.targets.filter(t => t.kind.includes('bin')).map(t => t.name));
    if (binaries.length !== 1) throw new Error(`${name} backend must build exactly one binary, found ${binaries.join(', ') || 'none'}`);
    console.log(`Building ${name} for ${target.GOOS}/${target.GOARCH} (rust ${target.triple})`);
    const args = ['build', '--release', '--locked', ...(cross ? ['--target', target.triple] : [])];
    await exec('cargo', args, { cwd: backend, env, timeout: 900000, maxBuffer: 16 * 1024 * 1024 });
    const built = resolve(backend, 'target', ...(cross ? [target.triple] : []), 'release', binaries[0] + (target.GOOS === 'windows' ? '.exe' : ''));
    const output = resolve(outputPackages, name, entry);
    await mkdir(dirname(output), { recursive: true });
    await copyFile(built, output);
    await chmod(output, 0o755);
  }
  return names;
}

async function main(args) {
  if (args.some(arg => !rustPlugins.includes(arg))) throw new Error(`Usage: build-rust-plugins.mjs [<rust-plugin>...]; known Rust plugins: ${rustPlugins.join(', ')}`);
  await buildRustPlugins(args.length ? args : rustPlugins);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch(error => {
    console.error(error);
    process.exitCode = 1;
  });
}
