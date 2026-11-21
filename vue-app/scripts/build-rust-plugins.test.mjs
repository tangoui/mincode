import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { buildRustPlugins, rustPlugins, rustTarget } from './build-rust-plugins.mjs';

const packages = resolve(import.meta.dirname, '../../official-plugins');

test('Rust backends are discovered from backend/Cargo.toml', () => {
  assert.ok(rustPlugins.includes('rdp-client'));
  for (const name of rustPlugins) assert.ok(existsSync(resolve(packages, name, 'backend', 'Cargo.toml')), name);
  assert.ok(!rustPlugins.includes('etcd-manager'), 'Go backends are not Rust backends');
});

test('the Rust target follows the Go target names', async () => {
  assert.deepEqual(rustTarget({ GOOS: 'darwin', GOARCH: 'arm64' }), { triple: 'aarch64-apple-darwin', GOOS: 'darwin', GOARCH: 'arm64' });
  assert.deepEqual(rustTarget({ GOOS: 'windows', GOARCH: 'amd64' }), { triple: 'x86_64-pc-windows-msvc', GOOS: 'windows', GOARCH: 'amd64' });
  assert.deepEqual(rustTarget({ GOOS: 'linux', GOARCH: 'amd64' }), { triple: 'x86_64-unknown-linux-gnu', GOOS: 'linux', GOARCH: 'amd64' });
  assert.throws(() => rustTarget({ GOOS: 'linux', GOARCH: '386' }), /Unsupported/);
});

test('builds the rdp-client process at its manifest entry for the host target', { timeout: 900_000 }, async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'rust-plugin-build-'));
  try {
    const built = await buildRustPlugins(['rdp-client', 'etcd-manager'], { outputPackages: output });
    assert.deepEqual(built, ['rdp-client'], 'only Rust backends are built');
    const manifest = JSON.parse(await readFile(resolve(packages, 'rdp-client/plugin.json'), 'utf8'));
    const binary = resolve(output, 'rdp-client', manifest.runtime.entrypoints[process.platform === 'win32' ? 'windows' : process.platform]);
    const info = await stat(binary);
    assert.ok(info.size > 0);
    if (process.platform !== 'win32') assert.ok(info.mode & 0o111, 'the process binary is executable');
  } finally {
    await rm(output, { recursive: true, force: true });
  }
});
