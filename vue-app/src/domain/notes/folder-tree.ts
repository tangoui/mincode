import type { ExportNotesInput, NoteTree } from "@/domain/notes/types";

/** 左栏导出的对象：若干笔记或一个文件夹（目标目录由导出时选择）。 */
export type NotesExportTarget = Omit<ExportNotesInput, "destDir">;

/** 笔记页左栏树的节点。key 带类型前缀，文件夹与笔记的 ID 不会冲突。 */
export interface NoteFolderNode {
  key: string;
  kind: "folder";
  id: string;
  name: string;
  /** 文件夹及其子文件夹中的笔记总数。 */
  count: number;
  children: NoteTreeNode[];
}

export interface NoteLeafNode {
  key: string;
  kind: "note";
  id: string;
}

export type NoteTreeNode = NoteFolderNode | NoteLeafNode;
export type NoteNodeKind = NoteTreeNode["kind"];
export type NoteDropType = "before" | "after" | "inner";

export const folderNodeKey = (id: string) => `f:${id}`;
export const noteNodeKey = (id: string) => `n:${id}`;

function byPosition<T extends { position: number }>(items: readonly T[]): T[] {
  return [...items].sort((a, b) => a.position - b.position);
}

/**
 * 把扁平的文件夹与笔记组装成树：每层先文件夹、后笔记，各按 position 升序。
 * 笔记节点只带 ID、不读标题：标题由渲染时按 ID 查找，输入标题不会重建整棵树。
 * 找不到父文件夹的条目放在根目录。
 */
export function buildNoteTreeNodes(tree: NoteTree): NoteTreeNode[] {
  const folderIds = new Set(tree.folders.map((folder) => folder.id));
  const folderNodes = new Map<string, NoteFolderNode>();
  for (const folder of tree.folders) {
    folderNodes.set(folder.id, { key: folderNodeKey(folder.id), kind: "folder", id: folder.id, name: folder.name, count: 0, children: [] });
  }
  const childFolders = new Map<string, NoteFolderNode[]>();
  for (const folder of byPosition(tree.folders)) {
    const parent = folderIds.has(folder.parentId) && folder.parentId !== folder.id ? folder.parentId : "";
    const list = childFolders.get(parent) ?? [];
    list.push(folderNodes.get(folder.id)!);
    childFolders.set(parent, list);
  }
  const childNotes = new Map<string, NoteLeafNode[]>();
  for (const note of byPosition(tree.notes)) {
    const parent = folderIds.has(note.folderId) ? note.folderId : "";
    const list = childNotes.get(parent) ?? [];
    list.push({ key: noteNodeKey(note.id), kind: "note", id: note.id });
    childNotes.set(parent, list);
  }

  const visited = new Set<string>();
  function children(parentId: string): NoteTreeNode[] {
    const folders = (childFolders.get(parentId) ?? []).filter((node) => !visited.has(node.id));
    for (const node of folders) {
      visited.add(node.id);
      node.children = children(node.id);
      node.count = node.children.reduce((sum, child) => sum + (child.kind === "folder" ? child.count : 1), 0);
    }
    return [...folders, ...(childNotes.get(parentId) ?? [])];
  }
  return children("");
}

/** 笔记不能有子节点；排序只在同类节点之间进行（每层文件夹在前、笔记在后）。 */
export function canDropNoteNode(dragged: NoteNodeKind, target: NoteNodeKind, type: NoteDropType): boolean {
  if (type === "inner") return target === "folder";
  return dragged === target;
}

interface NodeRef {
  kind: NoteNodeKind;
  id: string;
}

/**
 * 拖放后的父文件夹与 position：放进文件夹时排在末尾（空文件夹为 1）；
 * 放在某项前后时取与相邻项的中点，首尾分别为该项 position ∓ 1。不允许的拖放返回 null。
 */
export function noteDropTarget(
  tree: NoteTree,
  dragged: NodeRef,
  target: NodeRef,
  type: NoteDropType,
): { parentId: string; position: number } | null {
  if (!canDropNoteNode(dragged.kind, target.kind, type)) return null;
  const parentOf = (ref: NodeRef) =>
    ref.kind === "folder"
      ? tree.folders.find((folder) => folder.id === ref.id)?.parentId
      : tree.notes.find((note) => note.id === ref.id)?.folderId;
  const parentId = type === "inner" ? target.id : parentOf(target);
  if (parentId === undefined) return null;
  const siblings = byPosition<{ id: string; position: number }>(
    dragged.kind === "folder"
      ? tree.folders.filter((folder) => folder.parentId === parentId && folder.id !== dragged.id)
      : tree.notes.filter((note) => note.folderId === parentId && note.id !== dragged.id),
  );
  if (type === "inner") {
    const last = siblings[siblings.length - 1];
    return { parentId, position: last ? last.position + 1 : 1 };
  }
  const index = siblings.findIndex((item) => item.id === target.id);
  const anchor = siblings[index];
  if (!anchor) return null;
  const neighbour = siblings[type === "before" ? index - 1 : index + 1];
  const position = neighbour
    ? (neighbour.position + anchor.position) / 2
    : anchor.position + (type === "before" ? -1 : 1);
  return { parentId, position };
}
