// 从各 store 组装笔记关联解析用的对象快照，并负责点击关联后的跳转。
// 解析逻辑本身在纯函数模块 domain/notes/link-targets.ts；这里只读 store、不做判断。
import { computed, shallowRef, watch, type ComputedRef } from "vue";
import type { Router } from "vue-router";
import { ElMessage } from "element-plus";
import type { HttpRequestCollection } from "@/domain/http/request-collection";
import { resolveLink, type LinkObjectSnapshot } from "@/domain/notes/link-targets";
import type { NoteLink, NoteLinkType } from "@/domain/notes/types";
import type { WorkspaceView } from "@/types/workspace";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { loadAiWorkspaceOverview } from "@/services/ai-conversation-persistence";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import { canInvoke } from "@/services/desktop-bridge";
import {
  loadAiAssistantStore,
  peekAiAssistantStore,
  useAiAssistantShellStore,
  type AiAssistantStore,
} from "@/stores/ai-assistant-shell";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useHttpResourcesStore } from "@/stores/http-resources";
import { useLocalProjectStore } from "@/stores/local-project";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useWorkbenchStore } from "@/stores/workbench";

interface ConversationTitle {
  id: string;
  title: string;
}

/** 持久化的工作区对话概览是原生返回的未校验数据，只取 id 与标题。 */
function conversationTitles(value: unknown): ConversationTitle[] {
  const list = (value as { conversations?: unknown } | null)?.conversations;
  if (!Array.isArray(list)) return [];
  return list.flatMap((item: unknown) => {
    const { id, title } = (item ?? {}) as { id?: unknown; title?: unknown };
    return typeof id === "string" && id ? [{ id, title: typeof title === "string" ? title : "" }] : [];
  });
}

function collectHttpRequests(
  collections: readonly HttpRequestCollection[],
  names: Map<string, string>,
  parents: Map<string, string>,
) {
  for (const collection of collections) {
    for (const request of collection.requests) {
      names.set(request.id, request.name);
      parents.set(request.id, collection.id);
    }
    collectHttpRequests(collection.children ?? [], names, parents);
  }
}

// ---- 当前工作区 AI 对话概览：模块级共享缓存 ----
// 读取概览会让后端解码该工作区全部对话，并占用 AI 持久化的共享队列（会推迟 AI 的保存），
// 所以编辑区、侧栏、对话框共用一份：每个 scope 至多一个进行中的请求，切换工作区或显式失效时才重读。

type AiOverviewEntry = { status: "ready"; conversations: ConversationTitle[] } | { status: "failed" };

const aiOverviews = shallowRef<ReadonlyMap<string, AiOverviewEntry>>(new Map());
const aiOverviewLoads = new Map<string, Promise<void>>();

function setAiOverview(scopeId: string, entry: AiOverviewEntry | undefined) {
  const next = new Map(aiOverviews.value);
  if (entry) next.set(scopeId, entry);
  else next.delete(scopeId);
  aiOverviews.value = next;
}

/** 确保该 scope 的概览已加载或正在加载；`refresh` 丢弃已完成的缓存后重读（进行中的请求直接复用）。 */
function ensureAiOverview(scopeId: string, refresh = false) {
  if (aiOverviewLoads.has(scopeId)) return;
  if (aiOverviews.value.has(scopeId) && !refresh) return;
  if (!canInvoke("ai_store_load_workspace")) {
    // 浏览器预览没有 AI 对话存储：无法确认对话是否存在。
    setAiOverview(scopeId, { status: "failed" });
    return;
  }
  const load = loadAiWorkspaceOverview<unknown>(scopeId)
    .then((value) => setAiOverview(scopeId, { status: "ready", conversations: conversationTitles(value) }))
    .catch(() => setAiOverview(scopeId, { status: "failed" }))
    .finally(() => aiOverviewLoads.delete(scopeId));
  aiOverviewLoads.set(scopeId, load);
}

/** 让 AI 对话概览缓存失效（不传 scope 则全部失效）；仍在使用的快照会按需重读。 */
export function invalidateNoteLinkAiOverviews(scopeId?: string) {
  if (scopeId === undefined) {
    aiOverviews.value = new Map();
    return;
  }
  setAiOverview(scopeId, undefined);
}

function liveAiStoreFor(aiShell: ReturnType<typeof useAiAssistantShellStore>, scopeId: string): AiAssistantStore | undefined {
  // 打开 AI 助手才会创建其 store；读 open 让依赖方在创建后重新计算。
  void aiShell.open;
  const store = peekAiAssistantStore();
  return store?.hydrated && store.activeWorkspaceScopeId === scopeId ? store : undefined;
}

/**
 * 当前应用数据的关联对象快照。SSH / 数据库 / HTTP / 项目来自各自的 store（首次使用时加载）；
 * 当前工作区的 AI 对话优先取已加载的 AI 助手 store，否则用共享的持久化对话概览缓存，
 * 避免为解析名称加载整个 AI 助手。某类来源仍在加载或加载失败时列入 `unavailable`。
 */
export function useLinkObjectSnapshot(): ComputedRef<LinkObjectSnapshot> {
  const ssh = useSshConnectionsStore();
  const database = useDatabaseWorkspaceStore();
  const http = useHttpResourcesStore();
  const projects = useLocalProjectStore();
  const workspaceScope = useWorkspaceStateScope();
  const workbench = useWorkbenchStore();
  const aiShell = useAiAssistantShellStore();

  // 加载失败时 hydrated 保持 false，对应类型留在 unavailable 中。
  void ssh.hydrate().catch(() => undefined);
  void database.initialize().catch(() => undefined);
  void http.hydrate().catch(() => undefined);
  void workspaceScope.hydrate().catch(() => undefined);

  // AI 助手 store 已持有当前工作区时不读概览；不再持有（切走或在别处改过）时刷新一次。
  watch(
    [
      () => workspaceScope.scopeId.value,
      () => Boolean(liveAiStoreFor(aiShell, workspaceScope.scopeId.value)),
      // 缓存被显式失效后重读（进行中的请求由 ensureAiOverview 去重）。
      () => aiOverviews.value.has(workspaceScope.scopeId.value),
    ] as const,
    ([scopeId, live], previous) => {
      if (live) return;
      // 立即执行的首次回调里 previous 是空数组（多源 watch）。
      const [previousScopeId, previousLive] = previous ?? [];
      const changed = previousScopeId !== undefined && (previousScopeId !== scopeId || Boolean(previousLive));
      ensureAiOverview(scopeId, changed);
    },
    { immediate: true },
  );

  return computed<LinkObjectSnapshot>(() => {
    const currentScopeId = workspaceScope.scopeId.value;
    // 解析 AI 关联需要每个仍存在的工作区的 scopeId（关联里记录的是 scopeId），
    // 只读不写，所以这里直接用 scopeIdFor 做 workspaceId → scopeId 的映射。
    const preferences = workspaceScope.preferences;
    const unavailable = new Set<NoteLinkType>();

    const workspaceNames = new Map<string, string>();
    const liveScopeIds = new Set<string>();
    for (const workspace of workbench.workspaces) {
      const scopeId = preferences.scopeIdFor(workspace.id);
      liveScopeIds.add(scopeId);
      workspaceNames.set(workspace.id, workspace.name);
      if (!workspaceNames.has(scopeId)) workspaceNames.set(scopeId, workspace.name);
    }

    const otherWorkspaceProjects = new Map<string, string[]>();
    for (const [scopeId, roots] of projects.rootsByOtherWorkspace()) {
      // 已删除（可恢复）的工作区不算「其他工作区」。
      if (scopeId !== currentScopeId && liveScopeIds.has(scopeId)) otherWorkspaceProjects.set(scopeId, roots);
    }
    if (!projects.hydrated) unavailable.add("project");
    if (!ssh.hydrated) unavailable.add("ssh");
    // 只看已保存连接列表的加载结果；database.loading / error 也会因单次查询而变化。
    if (database.connectionsStatus !== "ready") unavailable.add("database");
    if (!http.hydrated) unavailable.add("http");

    const httpNames = new Map<string, string>();
    const httpCollections = new Map<string, string>();
    collectHttpRequests(http.collections, httpNames, httpCollections);

    const currentAi = new Map<string, string>();
    const live = liveAiStoreFor(aiShell, currentScopeId);
    const overview = aiOverviews.value.get(currentScopeId);
    if (live) {
      for (const conversation of live.conversations) currentAi.set(conversation.id, conversation.title);
    } else if (overview?.status === "ready") {
      for (const conversation of overview.conversations) currentAi.set(conversation.id, conversation.title);
    } else {
      unavailable.add("ai");
    }

    return {
      ssh: new Map(ssh.profiles.map((profile) => [profile.id, profile.name])),
      database: new Map(database.connections.map((connection) => [connection.id, connection.name])),
      http: httpNames,
      httpCollections,
      currentProjects: new Map(projects.projects.map((entry) => [entry.root, entry.name])),
      otherWorkspaceProjects,
      currentAi,
      currentScopeId,
      liveScopeIds,
      workspaceNames,
      unavailable,
    };
  });
}

/** 等 App 在 AI 助手打开时完成恢复（App.vue 的 ensureAssistantLoaded），最多等待 10 秒。 */
function untilHydrated(store: AiAssistantStore) {
  if (store.hydrated) return Promise.resolve(true);
  return new Promise<boolean>((resolve) => {
    const timer = window.setTimeout(() => {
      stop();
      resolve(false);
    }, 10_000);
    const stop = watch(
      () => store.hydrated,
      (hydrated) => {
        if (!hydrated) return;
        window.clearTimeout(timer);
        stop();
        resolve(true);
      },
    );
  });
}

async function openView(router: Router, view: WorkspaceView) {
  useWorkbenchStore().openView(view);
  await router.push({ name: view });
}

/**
 * 打开关联对象：在当前工作区则跳到对应页面；项目、HTTP 请求、AI 对话用页面已有的不产生连接的
 * 方法选中，SSH 与数据库只打开页面（不建立会话或连接）。只在其他工作区时提示，不自动切换工作区；
 * 已删除或状态未知时不做任何事。
 */
export async function openLinkTarget(link: NoteLink, snapshot: LinkObjectSnapshot, router: Router): Promise<void> {
  const { t } = useVueI18n();
  const resolved = resolveLink(link, snapshot);
  if (resolved.status === "deleted" || resolved.status === "unknown") return;
  if (resolved.status === "otherWorkspace") {
    ElMessage.info(t("notes.link.inOtherWorkspace", { name: resolved.workspaceName || link.scopeNameSnapshot }));
    return;
  }
  try {
    switch (link.objectType) {
      // SSH 与数据库页面的已注册动作（open-profile / open-connection）会建立会话或连接，
      // 页面也没有只选中不连接的方法：只打开页面，不连接。
      case "ssh":
        await openView(router, "ssh");
        return;
      case "project":
        await openView(router, "projects");
        await requestAiWorkspaceAction("projects", { action: "select", root: link.objectId });
        return;
      case "database":
        await openView(router, "database");
        return;
      case "http":
        await openView(router, "http");
        await requestAiWorkspaceAction("http", {
          action: "open-saved-request",
          collectionId: snapshot.httpCollections.get(link.objectId) ?? link.parentId,
          requestId: link.objectId,
        });
        return;
      case "ai": {
        const assistant = await loadAiAssistantStore();
        assistant.show();
        if (!await untilHydrated(assistant)) throw new Error("AI assistant did not finish loading");
        // 到这里 resolveLink 已判定对话在当前工作区（记录的 scope 可能因移动而过期）。
        if (!await assistant.switchToConversation(link.objectId, snapshot.currentScopeId)) {
          throw new Error("AI conversation is unavailable");
        }
        return;
      }
    }
  } catch {
    // 页面处理器的错误是面向开发者的英文；只提示打开失败。
    ElMessage.error(t("notes.link.openFailed"));
  }
}
