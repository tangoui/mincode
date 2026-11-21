import type {
  SshConnectionProfile,
  SshRuntimeCredentials,
  SshTerminalStatus,
} from "@/domain/ssh/types";
import type { TerminalSplitDirection } from "@/domain/terminal/types";

// ---------- serialization / deserialization ----------

type SerializedTerminalPane = {
  id: string;
  type: "terminal";
  profileId: string;
};

type SerializedSplitPane = {
  id: string;
  type: "split";
  direction: TerminalSplitDirection;
  children: SerializedPaneNode[];
  sizes: number[];
};

type SerializedPaneNode = SerializedTerminalPane | SerializedSplitPane;

export interface SerializedSshWorkspaceSessionTab {
  id: string;
  workspaceScopeId: string;
  activePaneId: string;
  root: SerializedPaneNode;
}

function serializePaneNode(node: SshWorkspacePaneNode): SerializedPaneNode {
  if (isSshWorkspaceTerminalPane(node)) {
    return { id: node.id, type: "terminal", profileId: node.profile.id };
  }
  return {
    id: node.id,
    type: "split",
    direction: node.direction,
    children: node.children.map(serializePaneNode),
    sizes: node.sizes,
  };
}

export function serializeSshWorkspaceSessionTab(
  tab: SshWorkspaceSessionTab
): SerializedSshWorkspaceSessionTab {
  return {
    id: tab.id,
    workspaceScopeId: tab.workspaceScopeId,
    activePaneId: tab.activePaneId,
    root: serializePaneNode(tab.root),
  };
}

function deserializePaneNode(
  raw: unknown,
  lookupProfile: (id: string) => SshConnectionProfile | undefined,
  lookupCredentials: (profileId: string) => SshRuntimeCredentials | undefined,
  leafCount: { value: number }
): SshWorkspacePaneNode | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const node = raw as Record<string, unknown>;

  if (node.type === "terminal") {
    if (typeof node.id !== "string" || typeof node.profileId !== "string") return null;
    const profile = lookupProfile(node.profileId);
    if (!profile) return null;
    leafCount.value += 1;
    if (leafCount.value > MAX_SSH_SESSION_PANES) return null;
    return {
      id: node.id,
      type: "terminal",
      profile,
      credentials: lookupCredentials(profile.id),
      reloadVersion: 0,
      status: "connecting",
    };
  }

  if (node.type === "split") {
    if (
      typeof node.id !== "string" ||
      (node.direction !== "horizontal" && node.direction !== "vertical") ||
      !Array.isArray(node.children) ||
      !Array.isArray(node.sizes)
    ) {
      return null;
    }
    const children: SshWorkspacePaneNode[] = [];
    for (const child of node.children) {
      const deserialized = deserializePaneNode(child, lookupProfile, lookupCredentials, leafCount);
      if (!deserialized) return null;
      children.push(deserialized);
    }
    if (children.length < 2) return null;
    return {
      id: node.id,
      type: "split",
      direction: node.direction,
      children,
      sizes: node.sizes as number[],
    };
  }

  return null;
}

export function deserializeSshWorkspaceSessionTab(
  raw: unknown,
  lookupProfile: (id: string) => SshConnectionProfile | undefined,
  lookupCredentials: (profileId: string) => SshRuntimeCredentials | undefined = () => undefined
): SshWorkspaceSessionTab | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const data = raw as Record<string, unknown>;
  if (
    typeof data.id !== "string" ||
    typeof data.workspaceScopeId !== "string" ||
    typeof data.activePaneId !== "string"
  ) {
    return null;
  }
  const leafCount = { value: 0 };
  const root = deserializePaneNode(data.root, lookupProfile, lookupCredentials, leafCount);
  if (!root) return null;
  return {
    id: data.id,
    workspaceScopeId: data.workspaceScopeId,
    activePaneId: data.activePaneId,
    root,
  };
}


export const MAX_SSH_SESSION_PANES = 8;

export interface SshWorkspaceTerminalPane {
  id: string;
  type: "terminal";
  profile: SshConnectionProfile;
  credentials?: SshRuntimeCredentials;
  reloadVersion: number;
  status: SshTerminalStatus;
  statusDetail?: string;
}

export interface SshWorkspaceSplitPane {
  id: string;
  type: "split";
  direction: TerminalSplitDirection;
  children: SshWorkspacePaneNode[];
  sizes: number[];
}

export type SshWorkspacePaneNode = SshWorkspaceTerminalPane | SshWorkspaceSplitPane;

export interface SshWorkspaceSessionTab {
  id: string;
  workspaceScopeId: string;
  root: SshWorkspacePaneNode;
  activePaneId: string;
}

export function isSshWorkspaceTerminalPane(
  node: SshWorkspacePaneNode
): node is SshWorkspaceTerminalPane {
  return node.type === "terminal";
}

export function sshWorkspacePaneIds(node: SshWorkspacePaneNode): string[] {
  if (isSshWorkspaceTerminalPane(node)) return [node.id];
  return node.children.flatMap(sshWorkspacePaneIds);
}

export function countSshWorkspacePanes(node: SshWorkspacePaneNode) {
  return sshWorkspacePaneIds(node).length;
}

export function findSshWorkspacePane(
  node: SshWorkspacePaneNode,
  paneId: string
): SshWorkspaceTerminalPane | undefined {
  if (isSshWorkspaceTerminalPane(node)) return node.id === paneId ? node : undefined;
  for (const child of node.children) {
    const found = findSshWorkspacePane(child, paneId);
    if (found) return found;
  }
  return undefined;
}

function normalizedSizes(sizes: readonly number[], count: number) {
  if (
    sizes.length !== count ||
    sizes.some((size) => !Number.isFinite(size) || size <= 0)
  ) {
    return Array.from({ length: count }, () => 1 / count);
  }
  const total = sizes.reduce((sum, size) => sum + size, 0);
  return total > 0 ? sizes.map((size) => size / total) : Array.from({ length: count }, () => 1 / count);
}

export function splitSshWorkspacePane(
  node: SshWorkspacePaneNode,
  targetPaneId: string,
  direction: TerminalSplitDirection,
  newPane: SshWorkspaceTerminalPane,
  splitId: string
): SshWorkspacePaneNode | undefined {
  if (isSshWorkspaceTerminalPane(node)) {
    if (node.id !== targetPaneId) return undefined;
    return {
      id: splitId,
      type: "split",
      direction,
      children: [node, newPane],
      sizes: [0.5, 0.5],
    };
  }

  let didSplit = false;
  const children = node.children.map((child) => {
    const nextChild = splitSshWorkspacePane(child, targetPaneId, direction, newPane, splitId);
    if (nextChild) {
      didSplit = true;
      return nextChild;
    }
    return child;
  });
  return didSplit ? { ...node, children } : undefined;
}

export function removeSshWorkspacePane(
  node: SshWorkspacePaneNode,
  paneId: string
): SshWorkspacePaneNode | undefined {
  if (isSshWorkspaceTerminalPane(node)) return node.id === paneId ? undefined : node;

  const childResults = node.children.map((child) => removeSshWorkspacePane(child, paneId));
  const children = childResults.filter((child): child is SshWorkspacePaneNode => child !== undefined);
  if (children.length === 0) return undefined;
  if (children.length === 1) return children[0];
  if (childResults.every((child, index) => child === node.children[index])) return node;
  const sizes = node.sizes.filter((_, index) => childResults[index] !== undefined);
  return { ...node, children, sizes: normalizedSizes(sizes, children.length) };
}

export function updateSshWorkspacePane(
  node: SshWorkspacePaneNode,
  paneId: string,
  update: (pane: SshWorkspaceTerminalPane) => SshWorkspaceTerminalPane
): SshWorkspacePaneNode {
  if (isSshWorkspaceTerminalPane(node)) return node.id === paneId ? update(node) : node;
  return {
    ...node,
    children: node.children.map((child) => updateSshWorkspacePane(child, paneId, update)),
  };
}

export function resizeSshWorkspaceSplit(
  node: SshWorkspacePaneNode,
  splitId: string,
  sizes: number[]
): SshWorkspacePaneNode {
  if (isSshWorkspaceTerminalPane(node)) return node;
  if (node.id === splitId) return { ...node, sizes: normalizedSizes(sizes, node.children.length) };
  return {
    ...node,
    children: node.children.map((child) => resizeSshWorkspaceSplit(child, splitId, sizes)),
  };
}
