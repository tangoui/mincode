export const workspaceViews = [
 "dashboard",
 "terminal",
 "ssh",
 "projects",
 "database",
 "sftp",
 "http",
 "notes",
 "sessions",
 "browser",
 "settings",
 "ai",
] as const;

export type WorkspaceView = (typeof workspaceViews)[number];

/**
 * 工作区绑定的 Git worktree。缺省表示工作区未绑定目录，沿用全局项目根。
 */
export interface WorkspaceWorktreeBinding {
 /** 主仓库路径，worktree 的归属仓库。 */
 repoPath: string;
 /** worktree 所在的绝对路径，同时作为该工作区的默认工作目录。 */
 worktreePath: string;
 /** worktree 检出的分支名。 */
 branch?: string;
 /** 创建时的起始 ref，用于后续对比与合并。 */
 baseRef?: string;
}

export interface WorkspaceDefinition {
 id: string;
 name: string;
 createdAt: number;
 updatedAt: number;
 worktree?: WorkspaceWorktreeBinding;
}

export interface WorkspaceTab {
 id: string;
 workspaceId: string;
 view: WorkspaceView;
 titleKey: string;
 title?: string;
 closable: boolean;
 pinned?: boolean;
}
