import type {
  SshConnectionProfile,
  SshRuntimeCredentials,
} from "@/domain/ssh/types";

export type TerminalSplitDirection = "horizontal" | "vertical";

export type LocalShellType =
  | "system"
  | "zsh"
  | "bash"
  | "fish"
  | "pwsh"
  | "powershell"
  | "cmd"
  | "wsl";

export interface LocalTerminalPaneOptions {
  shellType?: LocalShellType;
  customPath?: string;
  initialCwd?: string;
  initialCommand?: string;
  extraEnv?: Record<string, string>;
}

export interface LocalTerminalPane {
  id: string;
  type: "local";
  shellType: LocalShellType;
  customPath?: string;
  initialCwd?: string;
  /** Last known working directory of the live shell, used to start a fresh PTY after restart. */
  cwd?: string;
  initialCommand?: string;
  extraEnv?: Record<string, string>;
  reloadVersion?: number;
}

export interface SshTerminalPaneOptions {
  profile: SshConnectionProfile;
  credentials?: SshRuntimeCredentials;
}

export interface SshTerminalPane {
  id: string;
  type: "ssh";
  profile: SshConnectionProfile;
  credentials?: SshRuntimeCredentials;
  reloadVersion?: number;
}

export interface TerminalSplitPane {
  id: string;
  type: "split";
  direction: TerminalSplitDirection;
  children: TerminalPaneNode[];
  sizes: number[];
}

export type TerminalLeafPane = LocalTerminalPane | SshTerminalPane;
export type TerminalPaneNode = TerminalLeafPane | TerminalSplitPane;

export function cloneTerminalPaneTree(
  root: TerminalPaneNode,
  activePaneId: string,
  createId: (prefix: string) => string
): { root: TerminalPaneNode; activePaneId: string } {
  const paneIds = new Map<string, string>();
  const cloneNode = (node: TerminalPaneNode): TerminalPaneNode => {
    if (node.type === "local") {
      const id = createId("terminal");
      paneIds.set(node.id, id);
      const cwd = node.cwd?.trim() || node.initialCwd;
      return {
        id,
        type: "local",
        shellType: node.shellType,
        customPath: node.customPath,
        initialCwd: cwd,
        cwd,
      };
    }
    if (node.type === "ssh") {
      const id = createId("ssh-terminal");
      paneIds.set(node.id, id);
      return {
        id,
        type: "ssh",
        profile: { ...node.profile },
      };
    }
    return {
      id: createId("terminal-split"),
      type: "split",
      direction: node.direction,
      children: node.children.map(cloneNode),
      sizes: [...node.sizes],
    };
  };

  const clonedRoot = cloneNode(root);
  return {
    root: clonedRoot,
    activePaneId: paneIds.get(activePaneId) ?? terminalLeafIds(clonedRoot)[0]!,
  };
}

function terminalLeafIds(node: TerminalPaneNode): string[] {
  if (node.type !== "split") return [node.id];
  return node.children.flatMap(terminalLeafIds);
}

export function isLocalTerminalPane(node: TerminalPaneNode): node is LocalTerminalPane {
  return node.type === "local";
}

/**
 * 分屏时新 Pane 默认继承源 Pane 的当前工作目录，避免反复手动 cd；
 * 调用方显式传入 initialCwd 时保持原值不变。
 */
export function inheritSplitCwd(
  options: LocalTerminalPaneOptions,
  source: TerminalLeafPane | undefined
): LocalTerminalPaneOptions {
  if (options.initialCwd) return options;
  if (!source || source.type !== "local") return options;
  const cwd = source.cwd?.trim();
  return cwd ? { ...options, initialCwd: cwd } : options;
}

/**
 * 新建终端标签页时落在工作区绑定的 worktree 目录。
 * 仅用于全新标签页：分屏应继承同级 Pane 的实时目录，套用工作区根会把用户弹回仓库顶层。
 */
export function withWorkspaceCwd(
  options: LocalTerminalPaneOptions,
  worktreePath: string | undefined
): LocalTerminalPaneOptions {
  if (options.initialCwd) return options;
  const cwd = worktreePath?.trim();
  return cwd ? { ...options, initialCwd: cwd } : options;
}

export function isSshTerminalPane(node: TerminalPaneNode): node is SshTerminalPane {
  return node.type === "ssh";
}

export function isTerminalLeafPane(node: TerminalPaneNode): node is TerminalLeafPane {
  return node.type !== "split";
}
