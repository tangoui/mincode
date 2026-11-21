import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { git, publicIdentity, pushSnapshot, syncMessage, syncedSource } from './snapshot-push.mjs';

test('previous sync commits name their MinCode source in either message style', () => {
  assert.equal(syncedSource('Sync templates from MinCode ba767206\n\nbody'), 'ba767206');
  assert.equal(syncedSource('feat(host): x\n\nSynced from the MinCode monorepo packages/plugin-sdk-go at ba767206.'), 'ba767206');
  assert.equal(syncedSource('Initial import'), undefined);
});

test('sync commits use the public mailmap identity for the local user', async () => {
  const repo = await mkdtemp(join(tmpdir(), 'snapshot-ident-'));
  try {
    git(['init', '--quiet'], repo);
    git(['config', 'user.email', 'private@example.cn'], repo);
    await mkdir(join(repo, 'tools/maintenance'), { recursive: true });
    await writeFile(join(repo, 'tools/maintenance/github.mailmap'), '# comment\nfirst <first@public.test> <other@example.cn>\npub <pub@public.test> <private@example.cn>\n');
    assert.deepEqual(await publicIdentity(repo), { name: 'pub', email: 'pub@public.test' });
    git(['config', 'user.email', 'unknown@example.cn'], repo);
    assert.deepEqual(await publicIdentity(repo), { name: 'first', email: 'first@public.test' });
  } finally {
    await rm(repo, { recursive: true, force: true });
  }
});

test('pushes one commit on top of the remote history, nothing when unchanged, and removes deleted files', async () => {
  const root = await mkdtemp(join(tmpdir(), 'snapshot-push-'));
  try {
    const remote = join(root, 'remote.git');
    git(['init', '--quiet', '--bare', '--initial-branch=main', remote]);
    const identity = { name: 'pub', email: 'pub@public.test' };
    const content = join(root, 'content');
    await mkdir(join(content, 'pkg'), { recursive: true });
    await writeFile(join(content, 'a.txt'), 'a\n');
    await writeFile(join(content, 'pkg/b.txt'), 'b\n');
    const seen = [];
    const message = sha => previous => { seen.push(previous); return `Sync from MinCode ${sha}\n`; };

    assert.equal(await pushSnapshot({ dir: content, remote, identity, message: message('1111111') }), 'pushed');
    assert.equal(await pushSnapshot({ dir: content, remote, identity, message: message('2222222') }), 'unchanged');
    await rm(join(content, 'pkg'), { recursive: true });
    await writeFile(join(content, 'a.txt'), 'a2\n');
    assert.equal(await pushSnapshot({ dir: content, remote, identity, message: message('3333333') }), 'pushed');

    assert.deepEqual(seen, [undefined, '1111111'], 'no message is built when nothing changed');
    assert.equal(git(['log', '--format=%an <%ae>|%cn <%ce>|%s', 'main'], remote).trim(),
      'pub <pub@public.test>|pub <pub@public.test>|Sync from MinCode 3333333\npub <pub@public.test>|pub <pub@public.test>|Sync from MinCode 1111111');
    assert.equal(git(['ls-tree', '-r', '--name-only', 'main'], remote).trim(), 'a.txt');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('sync messages list the MinCode commits that touched the paths since the previous sync', async () => {
  const repo = await mkdtemp(join(tmpdir(), 'snapshot-msg-'));
  try {
    git(['init', '--quiet', '--initial-branch=main'], repo);
    git(['config', 'user.email', 't@example.test'], repo);
    git(['config', 'user.name', 't'], repo);
    const commit = async (file, subject) => {
      await mkdir(join(repo, file, '..'), { recursive: true });
      await writeFile(join(repo, file), subject);
      git(['add', '-A'], repo);
      git(['commit', '--quiet', '-m', subject], repo);
      return git(['rev-parse', 'HEAD'], repo).trim();
    };
    const first = await commit('sdk/a.go', 'feat(sdk): first');
    await commit('other/x.txt', 'chore: unrelated');
    await commit('sdk/b.go', 'fix(sdk): second');
    const head = git(['rev-parse', '--short=9', 'HEAD'], repo).trim();

    const message = syncMessage({ repo, subject: 'Sync from MinCode {head}', paths: ['sdk'], previous: first.slice(0, 8) });
    assert.equal(message, `Sync from MinCode ${head}\n\n- fix(sdk): second\n\nSynced from MinCode ${head}.\n`);
    assert.equal(syncedSource(message), head);
    assert.equal(syncMessage({ repo, subject: 'S {head}', paths: ['sdk'], previous: 'deadbeef' }), `S ${head}\n\nSynced from MinCode ${head}.\n`);
  } finally {
    await rm(repo, { recursive: true, force: true });
  }
});

test('an unreachable remote fails after the retries instead of hanging', async () => {
  const root = await mkdtemp(join(tmpdir(), 'snapshot-retry-'));
  try {
    await mkdir(join(root, 'content'));
    await assert.rejects(pushSnapshot({ dir: join(root, 'content'), remote: join(root, 'missing.git'), identity: { name: 'p', email: 'p@x.test' }, message: () => 'm' }));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('a tag goes on the snapshot commit, reruns accept it, and a tag on another commit is refused before pushing', async () => {
  const root = await mkdtemp(join(tmpdir(), 'snapshot-tag-'));
  try {
    const remote = join(root, 'remote.git');
    git(['init', '--quiet', '--bare', '--initial-branch=main', remote]);
    const identity = { name: 'pub', email: 'pub@public.test' };
    const content = join(root, 'content');
    await mkdir(content);
    await writeFile(join(content, 'go.mod'), 'module example.test/sdk\n');
    const tag = { name: 'v0.3.0', message: 'plugin-sdk-go v0.3.0' };
    const message = () => 'Sync from MinCode 1111111\n';

    assert.equal(await pushSnapshot({ dir: content, remote, identity, message, tag }), 'pushed');
    const head = git(['rev-parse', 'main'], remote).trim();
    assert.equal(git(['cat-file', '-t', 'v0.3.0'], remote).trim(), 'tag', 'annotated tag');
    assert.equal(git(['rev-parse', 'v0.3.0^{commit}'], remote).trim(), head);
    assert.match(git(['cat-file', 'tag', 'v0.3.0'], remote), /tagger pub <pub@public\.test>[\s\S]*plugin-sdk-go v0\.3\.0/);

    assert.equal(await pushSnapshot({ dir: content, remote, identity, message, tag }), 'unchanged', 'same snapshot, same tag: nothing to do');

    await writeFile(join(content, 'go.mod'), 'module example.test/sdk\n\ngo 1.24\n');
    await assert.rejects(pushSnapshot({ dir: content, remote, identity, message, tag }), /远端标签 v0\.3\.0 已指向/);
    assert.equal(git(['rev-parse', 'main'], remote).trim(), head, 'the branch is not pushed when the tag conflicts');

    assert.equal(await pushSnapshot({ dir: content, remote, identity, message, tag: { name: 'v0.3.1', message: 'plugin-sdk-go v0.3.1' } }), 'pushed');
    assert.equal(git(['rev-parse', 'v0.3.1^{commit}'], remote).trim(), git(['rev-parse', 'main'], remote).trim());
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
