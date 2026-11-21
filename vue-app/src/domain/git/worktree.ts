import type { GitWorktreeChangeStats, GitWorktreeEntry } from "@/services/git-service";
import type { WorkspaceDefinition, WorkspaceWorktreeBinding } from "@/types/workspace";

export function buildWorktreeBinding(
  repoPath: string,
  entry: Pick<GitWorktreeEntry, "path" | "branch">
): WorkspaceWorktreeBinding | undefined {
  const repo = repoPath.trim();
  const worktreePath = entry.path.trim();
  if (!repo || !worktreePath) return undefined;
  const branch = entry.branch?.trim();
  return {
    repoPath: repo,
    worktreePath,
    ...(branch ? { branch } : {}),
  };
}

/**
 * 从 worktree 列表反查归属主仓库。绑定必须同时记录仓库与 worktree，
 * 而用户打开的目录本身可能就是某个 linked worktree。
 */
export function resolveOwningRepoPath(
  entries: readonly GitWorktreeEntry[],
  fallbackRoot: string
) {
  return entries.find((entry) => entry.is_main)?.path ?? fallbackRoot;
}

/**
 * 找出绑定到指定路径的所有工作区。worktree 目录被删除后必须解除这些绑定，
 * 否则终端与 AI 会话会落到不存在的路径。
 */
export function workspaceIdsBoundTo(
  workspaces: readonly WorkspaceDefinition[],
  worktreePath: string
) {
  const target = worktreePath.trim();
  if (!target) return [];
  return workspaces
    .filter((workspace) => workspace.worktree?.worktreePath === target)
    .map((workspace) => workspace.id);
}

/** 结构化的项目条目，避免 domain 层反向依赖 store。 */
export interface WorktreeProjectCandidate {
  root: string;
  name: string;
}

/** 已列出的项目里，从属于另一个已列出主仓库的那些 worktree。 */
export interface WorktreeProjectRelation extends WorktreeProjectCandidate {
  branch?: string;
  changes?: GitWorktreeChangeStats;
  isLocked: boolean;
  isPrunable: boolean;
}

export interface WorktreeProjectGrouping<T extends WorktreeProjectCandidate> {
  roots: T[];
  childrenByRoot: Map<string, WorktreeProjectRelation[]>;
}

/** 归一化比较键。大小写不敏感，与后端判定 is_current 的方式保持一致。 */
function pathKey(path: string) {
  return path.trim().replace(/\\/g, "/").replace(/\/+$/, "").toLowerCase();
}

/**
 * 把「属于同一仓库」的项目条目收进主仓库名下，其余留在顶层。
 *
 * 关系只认 `git worktree list` 里路径完全相等的条目，绝不按路径前缀推断：
 * worktree 目录常常就落在主仓库内部（如 `<repo>/.claude/worktrees/x`），
 * 前缀匹配会把普通子目录误判成 worktree，也会让它同时出现在两处。
 */
export function groupProjectWorktrees<T extends WorktreeProjectCandidate>(
  projects: readonly T[],
  entriesByRoot: ReadonlyMap<string, readonly GitWorktreeEntry[]>
): WorktreeProjectGrouping<T> {
  const listedRoots = new Map(projects.map((project) => [pathKey(project.root), project]));
  const roots: T[] = [];
  const childrenByRoot = new Map<string, WorktreeProjectRelation[]>();

  for (const project of projects) {
    const entries = entriesByRoot.get(project.root) ?? [];
    const key = pathKey(project.root);
    const self = entries.find((entry) => pathKey(entry.path) === key);
    const mainEntry = entries.find((entry) => entry.is_main);
    const owner = mainEntry ? listedRoots.get(pathKey(mainEntry.path)) : undefined;

    // 自身即主仓库、并非 worktree 根（可能只是某个子目录），或主仓库未被加入
    // 项目列表时，都没有可归属的父节点。
    if (!self || self.is_main || !owner || pathKey(owner.root) === key) {
      roots.push(project);
      continue;
    }

    const children = childrenByRoot.get(owner.root) ?? [];
    children.push({
      root: project.root,
      name: project.name,
      ...(self.branch ? { branch: self.branch } : {}),
      ...(self.changes ? { changes: self.changes } : {}),
      isLocked: self.is_locked,
      isPrunable: self.is_prunable,
    });
    childrenByRoot.set(owner.root, children);
  }

  return { roots, childrenByRoot };
}

export function formatWorktreeChanges(changes: GitWorktreeChangeStats | undefined) {
  if (!changes) return undefined;
  return { additions: `+${changes.additions}`, deletions: `-${changes.deletions}` };
}

export function hasWorktreeChanges(changes: GitWorktreeChangeStats | undefined) {
  return Boolean(changes && (changes.additions > 0 || changes.deletions > 0));
}
