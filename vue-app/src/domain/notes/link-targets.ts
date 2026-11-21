/**
 * 笔记关联对象的名称与存在性解析（纯函数，不依赖 Vue 与 store）。
 * 快照由 `composables/use-note-link-snapshot.ts` 从各 store 组装；此处只按快照判断
 * 关联对象在当前工作区、只在其他工作区，还是已被删除（spec §7.2）。
 */
import type { NoteLink, NoteLinkType } from "@/domain/notes/types";

/** `unknown`：该类对象的来源仍在加载或加载失败，找不到对象时不能断定已删除。 */
export type LinkStatus = "current" | "otherWorkspace" | "deleted" | "unknown";

export interface LinkObjectSnapshot {
  /** SSH 连接 id → 名称（全局对象）。 */
  ssh: Map<string, string>;
  /** 数据库连接 id → 名称（全局对象）。 */
  database: Map<string, string>;
  /** 已保存 HTTP 请求 id → 名称（全局对象）。 */
  http: Map<string, string>;
  /** 已保存 HTTP 请求 id → 当前所在集合 id（请求可在集合间移动，打开时用它而非关联里的 parentId）。 */
  httpCollections: Map<string, string>;
  /** 当前工作区的项目：根路径 → 名称。 */
  currentProjects: Map<string, string>;
  /** 其他仍存在的工作区（按 scopeId）列出的项目根路径。 */
  otherWorkspaceProjects: Map<string, string[]>;
  /** 当前工作区的 AI 对话：id → 标题（标题可能为空）。 */
  currentAi: Map<string, string>;
  currentScopeId: string;
  /** 仍存在的工作区的 scopeId。 */
  liveScopeIds: Set<string>;
  /** workspaceId 或 scopeId → 工作区名称。 */
  workspaceNames: Map<string, string>;
  /** 来源仍在加载或加载失败的对象类型：其中找不到的对象解析为 `unknown` 而不是 `deleted`。 */
  unavailable: Set<NoteLinkType>;
}

export interface ResolvedLink {
  status: LinkStatus;
  name: string;
  workspaceName?: string;
}

/** 同一笔记内关联的唯一键，与后端 (object_type, object_id, scope_id) 唯一约束一致。 */
export function noteLinkKey(link: Pick<NoteLink, "objectType" | "objectId" | "scopeId">) {
  return `${link.objectType}\u0000${link.objectId}\u0000${link.scopeId}`;
}

function present(name: string, link: NoteLink): ResolvedLink {
  return { status: "current", name: name || link.nameSnapshot };
}

function missing(link: NoteLink, snapshot: LinkObjectSnapshot): ResolvedLink {
  return { status: snapshot.unavailable.has(link.objectType) ? "unknown" : "deleted", name: link.nameSnapshot };
}

function otherWorkspace(link: NoteLink, workspaceName: string): ResolvedLink {
  return { status: "otherWorkspace", name: link.nameSnapshot, workspaceName };
}

function globalObject(objects: Map<string, string>, link: NoteLink, snapshot: LinkObjectSnapshot): ResolvedLink {
  const name = objects.get(link.objectId);
  return name === undefined ? missing(link, snapshot) : present(name, link);
}

export function resolveLink(link: NoteLink, snapshot: LinkObjectSnapshot): ResolvedLink {
  switch (link.objectType) {
    case "ssh":
      return globalObject(snapshot.ssh, link, snapshot);
    case "database":
      return globalObject(snapshot.database, link, snapshot);
    case "http":
      return globalObject(snapshot.http, link, snapshot);
    case "project": {
      const name = snapshot.currentProjects.get(link.objectId);
      if (name !== undefined) return present(name, link);
      for (const [workspaceId, roots] of snapshot.otherWorkspaceProjects) {
        if (roots.includes(link.objectId)) {
          return otherWorkspace(link, snapshot.workspaceNames.get(workspaceId) ?? "");
        }
      }
      return missing(link, snapshot);
    }
    case "ai": {
      const scopeId = link.scopeId || snapshot.currentScopeId;
      if (scopeId === snapshot.currentScopeId) return globalObject(snapshot.currentAi, link, snapshot);
      // 对话可以移动到别的工作区，记录的 scope 随之过期：当前工作区有这个对话就以当前为准。
      const here = snapshot.currentAi.get(link.objectId);
      if (here !== undefined) return present(here, link);
      // 其他工作区的对话没有加载：工作区仍在即视为「在其他工作区」，不验证对话本身。
      if (snapshot.liveScopeIds.has(scopeId)) {
        return otherWorkspace(link, snapshot.workspaceNames.get(scopeId) ?? link.scopeNameSnapshot);
      }
      // 工作区列表总是已知的：工作区不在了就是已删除。
      return { status: "deleted", name: link.nameSnapshot };
    }
    default:
      return { status: "deleted", name: link.nameSnapshot };
  }
}
