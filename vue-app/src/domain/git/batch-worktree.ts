import type { GitWorktreeEntry } from "@/services/git-service";

export type BatchWorktreeBranchMode = "new" | "existing";

export type BatchWorktreeIssue =
  | "missing_target_root"
  | "invalid_branch"
  | "missing_existing_branch"
  | "missing_base_branch"
  | "base_branch_not_found"
  | "branch_not_found"
  | "branch_exists"
  | "branch_in_use"
  | "path_in_use"
  | "duplicate_target_path";

export interface BatchWorktreeRepository {
  name: string;
  relative_path: string;
  currentBranch: string;
  branchNames: readonly string[];
  localBranchNames: readonly string[];
  worktrees: readonly GitWorktreeEntry[];
}

export interface BatchWorktreeOptions {
  targetRoot: string;
  branch: string;
  baseBranch: string;
  branchMode?: BatchWorktreeBranchMode;
}

export interface BatchWorktreePlan {
  worktreePath: string;
  baseBranch: string;
  issue?: BatchWorktreeIssue;
}

function joinBatchPath(parent: string, child: string) {
  return [parent.replace(/\\/g, "/").replace(/\/+$/g, ""), child.trim()]
    .filter(Boolean)
    .join("/");
}

function pathKey(path: string) {
  return path.trim().replace(/\\/g, "/").replace(/\/+$/g, "").toLowerCase();
}

function branchSlug(branch: string) {
  return branch
    .trim()
    .replace(/[^\w.-]+/g, "-")
    .replace(/^[-.]+|[-.]+$/g, "");
}

function branchMatches(worktreeBranch: string | undefined, branch: string) {
  if (!worktreeBranch) return false;
  return worktreeBranch === branch || worktreeBranch === `refs/heads/${branch}`;
}

/**
 * 为批量工作树生成稳定路径并执行无副作用预检。仓库相对路径被保留，
 * 同一父目录下出现同名嵌套仓库时也不会落到相同目标目录。
 */
export function buildBatchWorktreePlan(
  repository: BatchWorktreeRepository,
  options: BatchWorktreeOptions,
): BatchWorktreePlan {
  const targetRoot = options.targetRoot.trim();
  const branch = options.branch.trim();
  const baseBranch = options.baseBranch.trim();
  const branchMode = options.branchMode ?? "new";
  const slug = branchSlug(branch);
  const relativePath = repository.relative_path === "."
    ? repository.name
    : repository.relative_path;
  const worktreePath = targetRoot && slug && relativePath
    ? joinBatchPath(joinBatchPath(targetRoot, slug), relativePath)
    : "";

  if (!targetRoot) return { worktreePath, baseBranch, issue: "missing_target_root" };
  if (!branch || !slug) {
    return {
      worktreePath,
      baseBranch,
      issue: branchMode === "existing" ? "missing_existing_branch" : "invalid_branch",
    };
  }

  if (branchMode === "existing") {
    if (!repository.localBranchNames.includes(branch)) {
      return { worktreePath, baseBranch, issue: "branch_not_found" };
    }
    if (repository.worktrees.some((worktree) => branchMatches(worktree.branch, branch))) {
      return { worktreePath, baseBranch, issue: "branch_in_use" };
    }
  } else {
    if (!baseBranch) return { worktreePath, baseBranch, issue: "missing_base_branch" };
    if (!repository.branchNames.includes(baseBranch)) {
      return { worktreePath, baseBranch, issue: "base_branch_not_found" };
    }
    if (repository.worktrees.some((worktree) => branchMatches(worktree.branch, branch))) {
      return { worktreePath, baseBranch, issue: "branch_in_use" };
    }
    if (repository.localBranchNames.includes(branch)) {
      return { worktreePath, baseBranch, issue: "branch_exists" };
    }
  }

  if (repository.worktrees.some((worktree) => pathKey(worktree.path) === pathKey(worktreePath))) {
    return { worktreePath, baseBranch, issue: "path_in_use" };
  }
  return { worktreePath, baseBranch };
}
