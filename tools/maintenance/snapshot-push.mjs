// Publishes a generated directory as one commit on top of a public GitHub repository
// (plugin-templates, plugin-sdk-go). The remote keeps its own history; each sync adds at most one
// commit that names the MinCode commit it came from, so the next sync can list what changed since.
import { execFileSync } from 'node:child_process';
import { rmSync } from 'node:fs';
import { cp, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export function git(args, cwd, { env, input } = {}) {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf8',
    input,
    stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'],
    env: env ? { ...process.env, ...env } : undefined,
  });
}

/** Retries network steps: SSH connections to GitHub drop now and then on this network. */
function withRetry(step, attempts = 3) {
  for (let attempt = 1; ; attempt += 1) {
    try {
      return step();
    } catch (error) {
      if (attempt >= attempts) throw error;
      console.error(`network step failed (attempt ${attempt}/${attempts}), retrying in 5 s`);
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5000);
    }
  }
}

/** The MinCode commit a previous sync commit names ("from MinCode <sha>" or "... at <sha>"), if any. */
export function syncedSource(message) {
  return /\bfrom MinCode ([0-9a-f]{7,40})\b/.exec(message)?.[1] ?? /\bMinCode monorepo \S+ at ([0-9a-f]{7,40})\b/.exec(message)?.[1];
}

/**
 * Public author for sync commits: the github.mailmap entry for this checkout's user.email, else its first
 * entry, so private addresses never appear in the public repositories.
 */
export async function publicIdentity(repo) {
  const entries = (await readFile(join(repo, 'tools/maintenance/github.mailmap'), 'utf8'))
    .split('\n')
    .map(line => /^([^#<]+?)\s*<([^>]+)>\s*<([^>]+)>\s*$/.exec(line))
    .filter(Boolean)
    .map(([, name, email, old]) => ({ name, email, old }));
  if (!entries.length) throw new Error('tools/maintenance/github.mailmap has no entries');
  let local = '';
  try { local = git(['config', 'user.email'], repo).trim(); } catch { /* unset */ }
  const { name, email } = entries.find(entry => entry.old === local) ?? entries[0];
  return { name, email };
}

/**
 * Replaces the content of `branch` on `remote` with the files in `dir` and pushes one commit when anything
 * changed. `message(previousSource)` builds the commit message, where previousSource is the MinCode commit
 * named by the last sync commit. Returns 'unchanged' or 'pushed'.
 *
 * With `tag: { name, message }` the snapshot commit (the new one, or the unchanged branch head) also gets an
 * annotated tag. A remote tag of that name on the same commit is accepted as already done; on any other commit
 * it is an error raised before anything is pushed, because published Go module versions must never move.
 */
export async function pushSnapshot({ dir, remote, branch = 'main', identity, message, tag }) {
  const work = await mkdtemp(join(tmpdir(), 'mincode-snapshot-'));
  try {
    withRetry(() => {
      rmSync(work, { recursive: true, force: true });
      git(['clone', '--quiet', remote, work]);
    });
    let previous;
    try {
      git(['checkout', '--quiet', branch], work);
      previous = syncedSource(git(['log', '-1', '--format=%B'], work));
    } catch {
      git(['checkout', '--quiet', '--orphan', branch], work);
    }
    for (const name of await readdir(work)) if (name !== '.git') await rm(join(work, name), { recursive: true, force: true });
    await cp(dir, work, { recursive: true });
    git(['add', '-A'], work);
    const changed = Boolean(git(['status', '--porcelain'], work).trim());
    const env = { GIT_AUTHOR_NAME: identity.name, GIT_AUTHOR_EMAIL: identity.email, GIT_COMMITTER_NAME: identity.name, GIT_COMMITTER_EMAIL: identity.email };
    if (changed) git(['commit', '--quiet', '-F', '-'], work, { env, input: message(previous) });
    const tagged = tag ? remoteTagCommit(work, tag.name) : undefined;
    const head = tag ? git(['rev-parse', 'HEAD'], work).trim() : undefined;
    if (tagged && tagged !== head) throw new Error(`远端标签 ${tag.name} 已指向 ${tagged.slice(0, 9)}，不是本次快照 ${head.slice(0, 9)}；已发布的版本标签不能移动，请换一个版本号`);
    if (changed) withRetry(() => git(['push', '--quiet', 'origin', `HEAD:${branch}`], work));
    if (tag && !tagged) {
      git(['tag', '--annotate', '--file', '-', tag.name, head], work, { env, input: `${tag.message}\n` });
      withRetry(() => git(['push', '--quiet', 'origin', `refs/tags/${tag.name}`], work));
    }
    return changed ? 'pushed' : 'unchanged';
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

/** The commit the remote tag `name` points at (peeled for annotated tags), or undefined when there is none. */
function remoteTagCommit(work, name) {
  const lines = withRetry(() => git(['ls-remote', '--tags', 'origin', `refs/tags/${name}`, `refs/tags/${name}^{}`], work)).split('\n');
  const refs = new Map(lines.filter(Boolean).map(line => { const [sha, ref] = line.split('\t'); return [ref, sha]; }));
  return refs.get(`refs/tags/${name}^{}`) ?? refs.get(`refs/tags/${name}`);
}

/**
 * Commit message for a sync of `paths` at MinCode HEAD: the subject, then the subjects of the MinCode commits
 * that touched `paths` since `previous` (when it is still in this history; at most 50), then the source line.
 */
export function syncMessage({ repo, subject, paths, previous }) {
  const head = git(['rev-parse', '--short=9', 'HEAD'], repo).trim();
  let changes = [];
  if (previous) {
    try {
      git(['merge-base', '--is-ancestor', previous, 'HEAD'], repo);
      changes = git(['log', '--no-merges', '--format=- %s', `${previous}..HEAD`, '--', ...paths], repo).split('\n').filter(Boolean);
    } catch { /* unknown or rewritten source: no list */ }
  }
  const list = changes.length > 50 ? [...changes.slice(0, 50), `- … and ${changes.length - 50} more`] : changes;
  return [subject.replace('{head}', head), '', ...(list.length ? [...list, ''] : []), `Synced from MinCode ${head}.`, ''].join('\n');
}
