import type { BackendCommand } from "@/generated/backend-commands";
import type { GitFileEntry, GitProjectStatus } from "@/domain/projects/types";
import { loadPersistedValue } from "@/services/persistent-store";
import { chooseNativeDirectory, invokeBackend } from "@/services/desktop-bridge";

export interface GitLogEntry {
  hash: string;
  short_hash: string;
  author: string;
  email?: string;
  message: string;
  time: number;
  parents?: string[];
  refs?: string[];
}
export interface GitFileDiff {
  original: string;
  modified: string;
  language: string;
  binary: boolean;
}
export interface GitBranchEntry {
  name: string;
  is_current: boolean;
  is_remote: boolean;
}
export interface GitBranches {
  current: string;
  branches: GitBranchEntry[];
}
export interface GitRepositoryEntry {
  name: string;
  path: string;
  relative_path: string;
}
/** 仓库列表每行的概览信息；`staged`/`unstaged`/`untracked` 是数量而非文件条目。 */
export interface GitRepositorySummary {
  path: string;
  branch: string;
  upstream?: string | null;
  ahead: number;
  behind: number;
  staged: number;
  unstaged: number;
  untracked: number;
}
export interface GitTagEntry {
  name: string;
  /** 只有附注标签才有说明；轻量标签为 null。 */
  message?: string | null;
  target_hash: string;
  /** 标签最终指向的提交（附注标签已解引用）。 */
  commit_hash?: string;
  annotated?: boolean;
  /** ISO 8601；附注标签取打标签时间，轻量标签取提交时间。 */
  date?: string;
}
export interface GitStashEntry {
  index: number;
  message: string;
  oid: string;
  files: GitFileEntry[];
}
export interface GitShelfEntry {
  id: string;
  message: string;
  created_at: number;
  files: GitFileEntry[];
}
export interface GitCommitFile {
  path: string;
  old_path?: string;
  status: string;
}
export interface GitCommitDetail {
  commit: GitLogEntry;
  files: GitCommitFile[];
}
export interface GitConflictContent {
  ours: string;
  theirs: string;
  base: string;
  raw: string;
}
export interface GitMergeState {
  active: boolean;
  conflicts: string[];
}
export interface GitBlameHunk {
  line_start: number;
  line_end: number;
  commit_hash: string;
  commit_hash_short: string;
  author: string;
  message: string;
}
export interface GitWorktreeChangeStats {
  additions: number;
  deletions: number;
}
export interface GitWorktreeEntry {
  path: string;
  branch?: string;
  head?: string;
  lock_reason?: string;
  is_main: boolean;
  is_current: boolean;
  is_bare: boolean;
  is_detached: boolean;
  is_locked: boolean;
  is_prunable: boolean;
  /** 相对 HEAD 的增删行数；bare、prunable 或统计失败时缺省。 */
  changes?: GitWorktreeChangeStats;
}
export type GitRebaseAction = "pick" | "squash" | "fixup" | "drop";
export interface GitRebaseActionEntry {
  hash: string;
  action: GitRebaseAction;
  message: string;
}

export function chooseGitDirectory(title: string, directory?: string) {
  return chooseNativeDirectory(title, directory);
}
export function listProjectRepositories(projectRoot: string) {
  return invokeBackend<GitRepositoryEntry[]>("git_list_project_repositories", { projectRoot });
}
/** 批量读取仓库概览；后端并发执行且跳过读不到的仓库，返回结果可能少于入参。 */
export function getRepositorySummaries(repoPaths: string[]) {
  return invokeBackend<GitRepositorySummary[]>("git_get_repository_summaries", { repoPaths });
}

export interface AiGitRepositorySnapshot {
  repositories: GitRepositoryEntry[];
  activeRepositoryPath?: string;
}

const aiGitRepositoryScans = new Map<string, Promise<GitRepositoryEntry[]>>();

export async function loadAiGitRepositories(projectRoot: string): Promise<AiGitRepositorySnapshot> {
  const root = projectRoot.trim();
  if (!root) return { repositories: [] };
  let scan = aiGitRepositoryScans.get(root);
  if (!scan) {
    scan = listProjectRepositories(root).catch(() => []);
    aiGitRepositoryScans.set(root, scan);
  }
  const [repositories, persistedPath] = await Promise.all([
    scan,
    loadPersistedValue<string>("app_project_source_repo_path"),
  ]);
  const selectedPath = typeof persistedPath === "string" &&
    repositories.some((repository) => repository.path === persistedPath)
    ? persistedPath
    : repositories[0]?.path;
  return { repositories, activeRepositoryPath: selectedPath };
}

export function loadGitOverview(repoPath: string) {
  return Promise.all([
    invokeBackend<GitProjectStatus>("git_get_status", { repoPath }),
    invokeBackend<GitLogEntry[]>("git_get_log", { repoPath, limit: 100 }),
    invokeBackend<GitBranches>("git_list_branches", { repoPath }),
  ]);
}
export function loadGitOptionalData(repoPath: string) {
  return Promise.allSettled([
    invokeBackend<GitTagEntry[]>("git_list_tags", { repoPath }),
    invokeBackend<GitStashEntry[]>("git_stash_list", { repoPath }),
    invokeBackend<string[]>("git_list_conflicts", { repoPath }),
    invokeBackend<string>("git_get_remote_url", { repoPath }),
  ]);
}
export function getGitDiff(repoPath: string, relativePath: string, staged: boolean, oldPath = "") {
  return invokeBackend<GitFileDiff>("git_get_diff", { repoPath, relativePath, staged, oldPath });
}
export function runGitCommand(
  command: BackendCommand,
  repoPath: string,
  args: Record<string, unknown> = {}
) {
  return invokeBackend(command, { repoPath, ...args });
}
export function getGitCommitDetail(repoPath: string, hash: string) {
  return invokeBackend<GitCommitDetail>("git_get_commit_detail", { repoPath, commitHash: hash });
}
export function getGitCommitFileDiff(repoPath: string, hash: string, path: string, oldPath = "") {
  return invokeBackend<GitFileDiff>("git_get_commit_file_diff", {
    repoPath,
    commitHash: hash,
    relativePath: path,
    oldPath,
  });
}
export function getGitBlame(repoPath: string, path: string) {
  return invokeBackend<GitBlameHunk[]>("git_blame", { repoPath, relativePath: path });
}
export function getGitConflictContent(repoPath: string, path: string) {
  return invokeBackend<GitConflictContent>("git_get_conflict_content", {
    repoPath,
    relativePath: path,
  });
}
export function getGitMergeState(repoPath: string) {
  return invokeBackend<GitMergeState>("git_get_merge_state", { repoPath });
}
export function resolveGitConflict(repoPath: string, path: string, content: string) {
  return invokeBackend("git_resolve_conflict", { repoPath, relativePath: path, content });
}
export function abortGitMerge(repoPath: string) {
  return invokeBackend("git_abort_merge", { repoPath });
}
export function generateGitChangelog(repoPath: string) {
  return invokeBackend<string>("git_generate_changelog", { repoPath });
}

export function getGitLog(repoPath: string, limit = 200) {
  return invokeBackend<GitLogEntry[]>("git_get_log", { repoPath, limit });
}
export function listGitWorktrees(repoPath: string) {
  return invokeBackend<GitWorktreeEntry[]>("git_list_worktrees", { repoPath });
}
export function addGitWorktree(
  repoPath: string,
  worktreePath: string,
  branch: string,
  newBranch: boolean,
  baseBranch?: string
) {
  return invokeBackend("git_add_worktree", {
    repoPath,
    worktreePath,
    branch,
    newBranch,
    baseBranch: baseBranch?.trim() || null,
  });
}
export function removeGitWorktree(
  repoPath: string,
  worktreePath: string,
  options: { force?: boolean; forceLocked?: boolean } = {}
) {
  return invokeBackend("git_remove_worktree", {
    repoPath,
    worktreePath,
    force: options.force === true,
    forceLocked: options.forceLocked === true,
  });
}
export function pruneGitWorktrees(repoPath: string) {
  return invokeBackend("git_prune_worktrees", { repoPath });
}
export function lockGitWorktree(repoPath: string, worktreePath: string, reason?: string) {
  return invokeBackend("git_lock_worktree", { repoPath, worktreePath, reason });
}
export function unlockGitWorktree(repoPath: string, worktreePath: string) {
  return invokeBackend("git_unlock_worktree", { repoPath, worktreePath });
}
export function runInteractiveGitRebase(
  repoPath: string,
  baseHash: string,
  actions: GitRebaseActionEntry[]
) {
  return invokeBackend<string>("git_interactive_rebase", { repoPath, baseHash, actions });
}
