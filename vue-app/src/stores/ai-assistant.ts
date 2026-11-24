import { rewindConversation } from "@/domain/ai/conversation-rewind";
import { computed, nextTick, ref, watch } from "vue";
import { runtimePerformanceSettingsFromSnapshot } from "@/domain/performance/runtime-limits";
import { readSettingsSnapshot, settingsSnapshotVersion } from "@/services/settings-persistence";
import { createLatestTaskRunner } from "@/domain/async/serialized-task-queue";
import { acceptHMRUpdate, defineStore, storeToRefs } from "pinia";
import { compactAiAttachment, sanitizeAiAttachments, type AiAttachment } from "@/domain/ai/attachments";
import { normalizeAiConversationTitle } from "@/domain/ai/conversation-title";
import type { AiLoopMessage } from "@/services/ai-agent-loop";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import {
  archiveAiConversations,
  clearAllAiConversationHistory,
  deleteAiConversation,
  loadAiWorkspaceOverview,
  listAiWorkspaceOverviews,
  loadAiConversationDetail,
  moveAiConversation,
  replaceAiConversation,
  saveAiConversationDetail,
  saveAiWorkspaceConversationMeta,
  type AiConversationLocation,
} from "@/services/ai-conversation-persistence";
import { safeAiText } from "@/services/ai-tools/shared";
import { applyAiAgentEventTiming, normalizeAiEventTimestamp } from "@/domain/ai/agent-event-timing";
import { anchorUnassignedAiEvents, reanchorAiEvents } from "@/domain/ai/agent-event-groups";
import { normalizeAiTokenUsage, type AiTokenUsage } from "@/domain/ai/token-usage";
import { useSessionProgressStore } from "@/stores/session-progress";
import { useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import { useWorkbenchStore } from "@/stores/workbench";
import {
  AI_ASSISTANT_LAYOUT_MODE_KEY,
  AI_ASSISTANT_PLACEMENT_KEY,
  AI_ASSISTANT_WIDTH_KEY,
  registerAiAssistantStore,
  type AiAssistantLayoutMode,
  type AiAssistantPlacement,
  useAiAssistantShellStore,
} from "@/stores/ai-assistant-shell";
import { isPopupWindow } from "@/services/popup-tab";
import router from "@/router";

const STREAMING_UPDATED_AT_INTERVAL_MS = 1_000;
/** Recently viewed conversations whose details stay in memory after switching away. */
const RETAINED_LOADED_CONVERSATIONS = 3;
/** Stored event details keep this many leading characters, then "…". */
export const AI_EVENT_DETAIL_LIMIT = 2_000;

export type AiAssistantMode = "agentic" | "agent";
export type { AiAssistantLayoutMode, AiAssistantPlacement } from "@/stores/ai-assistant-shell";
type PersistedAiAssistantMode = AiAssistantMode | "chat";
export type AiAssistantMessageRole = "user" | "assistant" | "system";
/** "error" marks a failed request shown as an error card with retry actions. */
/** `loop-limit` marks the step-limit wrap-up so its "continue" action survives reloads. */
export type AiAssistantMessagePresentation = "task-summary" | "error" | "loop-limit";

export interface AiAssistantMessage {
  id?: string;
  role: AiAssistantMessageRole;
  content: string;
  createdAt?: number;
  attachments?: AiAttachment[];
  eventAnchor?: boolean;
  /** Optional structured presentation for special assistant delivery states. */
  presentation?: AiAssistantMessagePresentation;
  /** Aggregated provider-reported usage for the model turns that produced this reply. */
  tokenUsage?: AiTokenUsage;
}

export interface AiAssistantEvent {
  id: string;
  kind: "thought" | "tool" | "plan" | "mode";
  title: string;
  detail: string;
  status?: string;
  /** Native/ACP tool identifier when this is a tool event. */
  toolName?: string;
  /** Sanitized tool input retained so the UI can summarize completed tool calls. */
  toolInput?: string;
  /** Stable identity of the assistant message that triggered this event. */
  triggerMessageId?: string;
  /** Event start time in Unix milliseconds. Missing on legacy history. */
  startedAt?: number;
  /** Event finish time in Unix milliseconds. Missing while running or on legacy history. */
  finishedAt?: number;
  /** Legacy persisted anchor retained for backward compatibility. */
  afterMessageIndex?: number;
}

export interface AiAssistantPermission {
  requestId: string;
  serverId: string;
  sessionId: string;
  options: Array<{ optionId: string; name: string; kind?: string }>;
  toolCall?: unknown;
}

export interface AiQueuedMessage {
  id: string;
  content: string;
  attachments?: AiAttachment[];
  createdAt: number;
}

interface PendingAiContextAttachment {
  workspaceScopeId: string;
  conversationId: string;
  attachment: AiAttachment;
}

export interface AiAssistantSnapshot {
  version: 1;
  mode: AiAssistantMode;
  /** Per-conversation unsent composer text. */
  draft?: string;
  /** Per-conversation unsent composer attachments. */
  draftAttachments?: AiAttachment[];
  selectedModel?: string;
  /** Session-only override; an empty value uses the active profile default. */
  selectedReasoningEffort?: string;
  messages: AiAssistantMessage[];
  agentHistory?: AiLoopMessage[];
  agentEvents: AiAssistantEvent[];
  messageQueue?: AiQueuedMessage[];
  pendingPermission?: AiAssistantPermission;
  acpServerId: string;
  acpSessionId: string;
  /**
   * The ACP agent (Settings `acpAdapterId`) that created or last loaded
   * acpSessionId. Absent in conversations from before it was recorded.
   */
  acpAdapterId?: string;
  agentReady: boolean;
}

export type AiConversationStatus =
  | "not_started"
  | "in_progress"
  | "needs_input"
  | "completed"
  | "failed"
  | "interrupted";

export interface AiConversation extends AiAssistantSnapshot {
  id: string;
  /** Stable business ownership used by the global conversation filter. */
  workspaceId?: string;
  /** Name snapshot retained after the workspace is deleted. */
  workspaceName?: string;
  /** SQLite partition containing this conversation. */
  workspaceScopeId?: string;
  /** Project root captured when the conversation is created. */
  projectRoot?: string;
  /** Recovery records for projects this conversation was previously bound to. */
  acpProjectSessions?: { projectRoot: string; sessionId: string; adapterId?: string }[];
  title: string;
  createdAt: number;
  updatedAt: number;
  archived: boolean;
  pinned: boolean;
  status: AiConversationStatus;
  running: boolean;
  /** 会话尚未被用户查看，支持自动与手动标记。 */
  unread?: boolean;
  /** False only for workspace overview rows whose message/history details are not loaded yet. */
  detailsLoaded?: boolean;
  /** Keeps the original access boundary for imported relayed conversations. */
  external?: { workspace?: boolean };
  /** Links the dispatched task and its report to the conversation that sent it. */
  delegation?: {
    parentId: string;
    source: "manual" | "parallel";
    toolCallId?: string;
    triggerMessageId?: string;
    turnMessageId?: string;
    result?: string;
    resultStatus?: AiConversationStatus;
  };
}

type LegacyAiConversation = AiConversation & { completionUnread?: boolean };

export interface AiConversationCollection {
  version: 2;
  activeConversationId: string;
  conversations: AiConversation[];
}

function createId(prefix: string) {
  const uuid = globalThis.crypto?.randomUUID?.();
  return `${prefix}-${uuid ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
}

const AI_CONVERSATION_SIDEBAR_WIDTH_KEY = "app_ai_conversation_sidebar_width";
const AI_CONVERSATION_SIDEBAR_MIN_WIDTH = 210;
const AI_CONVERSATION_SIDEBAR_MAX_WIDTH = 720;
const AI_CONVERSATION_SIDEBAR_DEFAULT_WIDTH = 238;
function inactiveWorkspaceCacheLimits() {
  const limits = runtimePerformanceSettingsFromSnapshot(readSettingsSnapshot());
  return {
    count: limits.aiInactiveWorkspaceCacheCount,
    textBytes: limits.aiInactiveWorkspaceCacheMiB * 1024 * 1024,
  };
}
const DEFAULT_WORKSPACE_SCOPE_ID = "workspace:default";
const DEFAULT_WORKSPACE_ID = "workspace:default";

export interface AiConversationWorkspaceContext {
  workspaceId: string;
  workspaceName?: string;
  scopeId: string;
  projectRoot: string;
}
const aiConversationPersistence = {
  load: loadAiWorkspaceOverview<AiConversationCollection>,
  loadDetail: loadAiConversationDetail<AiConversation>,
  saveDetail: saveAiConversationDetail<AiConversation>,
  saveMeta: saveAiWorkspaceConversationMeta,
};

function isPersistedMode(value: unknown): value is PersistedAiAssistantMode {
  return value === "chat" || value === "agentic" || value === "agent";
}

function normalizeMode(value: PersistedAiAssistantMode): AiAssistantMode {
  return value === "chat" ? "agentic" : value;
}

const UNSETTLED_AGENT_EVENT_STATUSES = new Set(["running", "pending", "in_progress"]);

function safeAgentEvent(event: AiAssistantEvent): AiAssistantEvent {
  const { toolName, toolInput, startedAt, finishedAt, ...rest } = event;
  return {
    ...rest,
    title: safeAiText(event.title, 240),
    detail: safeAiText(event.detail, AI_EVENT_DETAIL_LIMIT),
    ...(toolName ? { toolName: safeAiText(toolName, 160) } : {}),
    ...(toolInput ? { toolInput: safeAiText(toolInput, 1_000) } : {}),
    ...(normalizeAiEventTimestamp(startedAt) !== undefined ? { startedAt: normalizeAiEventTimestamp(startedAt) } : {}),
    ...(normalizeAiEventTimestamp(finishedAt) !== undefined ? { finishedAt: normalizeAiEventTimestamp(finishedAt) } : {}),
  };
}

function settledAgentEvents(
  events: AiAssistantEvent[],
  status: string,
  finishedAt = Date.now(),
  kind?: AiAssistantEvent["kind"],
) {
  return events.map((event) =>
    event.status && UNSETTLED_AGENT_EVENT_STATUSES.has(event.status) && (!kind || event.kind === kind)
      ? {
          ...event,
          status,
          ...(normalizeAiEventTimestamp(event.startedAt) !== undefined
            ? { finishedAt: Math.max(event.startedAt!, finishedAt) }
            : {}),
        }
      : event,
  );
}

export const useAiAssistantStore = defineStore("ai-assistant", () => {
  registerAiAssistantStore(useAiAssistantStore);
  // 抽屉开关、布局与宽度由启动壳层持有，这里共享同一组 ref。
  // placement：右侧抽屉（drawer）或顶部标签页（tab），全局设置。
  const { open, fullscreen, layoutMode, placement, width } = storeToRefs(useAiAssistantShellStore());
  const mode = ref<AiAssistantMode>("agentic");
  // A session-level override: changing it never mutates the default AI profile in Settings.
  const selectedModel = ref("");
  // A session-level override: an empty value keeps the active profile default.
  const selectedReasoningEffort = ref("");
  const conversationSidebarWidth = ref(
    Number(localStorage.getItem(AI_CONVERSATION_SIDEBAR_WIDTH_KEY)) ||
      AI_CONVERSATION_SIDEBAR_DEFAULT_WIDTH,
  );
  const hydrated = ref(false);
  const workspaceSwitching = ref(false);
  const conversationPreferences = useAiConversationPreferencesStore();
  const sessionProgress = useSessionProgressStore();
  void sessionProgress.ensureListening();
  const activeWorkspaceScopeId = ref(DEFAULT_WORKSPACE_SCOPE_ID);
  const topWorkspaceId = ref(DEFAULT_WORKSPACE_ID);
  const topWorkspaceName = ref("");
  const topWorkspaceScopeId = ref(DEFAULT_WORKSPACE_SCOPE_ID);
  const topWorkspaceProjectRoot = ref("");
  const globalWorkspaceOverviews = ref<Array<{ scopeId: string; collection: AiConversationCollection }>>([]);
  const workspaceDirectory = ref<AiConversationWorkspaceContext[]>([]);
  const globalOverviewsLoading = ref(false);
  const messages = ref<AiAssistantMessage[]>([]);
  const agentHistory = ref<AiLoopMessage[]>([]);
  const agentEvents = ref<AiAssistantEvent[]>([]);
  const pendingPermission = ref<AiAssistantPermission>();
  const pendingResponseConversationIds = ref<Set<string>>(new Set());
  const acpServerId = ref(createId("vue-ai"));
  const acpSessionId = ref("");
  const acpAdapterId = ref("");
  const agentReady = ref(false);
  const agentStarting = ref(false);
  const activeQueue = ref<AiQueuedMessage[]>([]);
  const pendingContextAttachments = ref<PendingAiContextAttachment[]>([]);
  const draft = ref("");
  const draftAttachments = ref<AiAttachment[]>([]);
  const now = Date.now();
  const initialConversationId = createId("conversation");
  const activeConversationId = ref(initialConversationId);
  const conversations = ref<AiConversation[]>([
    {
      id: initialConversationId,
      title: "",
      createdAt: now,
      updatedAt: now,
      archived: false,
      pinned: false,
      status: "not_started",
      running: false,
      version: 1,
      mode: "agentic",
      messages: [],
      agentHistory: [],
      agentEvents: [],
      acpServerId: acpServerId.value,
      acpSessionId: "",
      agentReady: false,
      detailsLoaded: true,
    },
  ]);
  const activeConversation = computed(() =>
    conversations.value.find((conversation) => conversation.id === activeConversationId.value),
  );
  const loading = computed(() => activeConversation.value?.running ?? false);
  const awaitingResponse = computed(() =>
    loading.value && pendingResponseConversationIds.value.has(activeConversationId.value),
  );

  function setConversationAwaitingResponse(id: string, awaiting: boolean) {
    const current = pendingResponseConversationIds.value;
    if (current.has(id) === awaiting) return;
    const next = new Set(current);
    if (awaiting) next.add(id);
    else next.delete(id);
    pendingResponseConversationIds.value = next;
  }

  function normalizeWorkspaceScopeId(scopeId: string | undefined) {
    return scopeId?.trim() || DEFAULT_WORKSPACE_SCOPE_ID;
  }

  function normalizeWorkspaceContext(
    context?: Partial<AiConversationWorkspaceContext>,
  ): AiConversationWorkspaceContext {
    const scopeId = normalizeWorkspaceScopeId(context?.scopeId);
    return {
      workspaceId: context?.workspaceId?.trim() || scopeId || DEFAULT_WORKSPACE_ID,
      workspaceName: context?.workspaceName?.trim() || undefined,
      scopeId,
      projectRoot: context?.projectRoot?.trim() || "",
    };
  }

  function setWorkspaceDirectory(contexts: AiConversationWorkspaceContext[]) {
    workspaceDirectory.value = contexts.map(normalizeWorkspaceContext);
  }

  function uniqueWorkspaceForScope(scopeId: string) {
    const matches = workspaceDirectory.value.filter((context) => context.scopeId === scopeId);
    return matches.length === 1 ? matches[0] : undefined;
  }

  function rememberTopWorkspace(context?: Partial<AiConversationWorkspaceContext>) {
    const normalized = normalizeWorkspaceContext(context);
    topWorkspaceId.value = normalized.workspaceId;
    topWorkspaceName.value = normalized.workspaceName || "";
    topWorkspaceScopeId.value = normalized.scopeId;
    topWorkspaceProjectRoot.value = normalized.projectRoot;
    updateWorkspaceConversationRoots(normalized);
    return normalized;
  }

  function applyConversationOwnership(
    conversation: AiConversation,
    scopeId: string,
    fallback?: Partial<AiConversationWorkspaceContext>,
  ) {
    const normalizedScopeId = normalizeWorkspaceScopeId(scopeId);
    const inferredWorkspace = uniqueWorkspaceForScope(normalizedScopeId);
    const fallbackWorkspaceId = fallback?.workspaceId?.trim() || inferredWorkspace?.workspaceId;
    const fallbackWorkspaceName = fallback?.workspaceName?.trim() || inferredWorkspace?.workspaceName;
    const fallbackProjectRoot = fallback?.projectRoot?.trim() || inferredWorkspace?.projectRoot;
    const canInferTopWorkspace = normalizedScopeId === topWorkspaceScopeId.value
      && topWorkspaceScopeId.value === topWorkspaceId.value;
    conversation.workspaceScopeId = normalizedScopeId;
    conversation.workspaceId = conversation.workspaceId?.trim()
      || fallbackWorkspaceId
      || (canInferTopWorkspace ? topWorkspaceId.value : "");
    conversation.workspaceName = conversation.workspaceName?.trim()
      || fallbackWorkspaceName;
    conversation.projectRoot = conversation.projectRoot?.trim()
      || fallbackProjectRoot
      || (canInferTopWorkspace ? topWorkspaceProjectRoot.value : "");
    const legacyConversation = conversation as LegacyAiConversation;
    conversation.unread = conversation.unread === true || legacyConversation.completionUnread === true;
    delete legacyConversation.completionUnread;
    // Only rewritten when stored data is malformed, so repeated calls stay side-effect free.
    return conversation;
  }

  function collectionWithOwnership(
    collection: AiConversationCollection,
    scopeId: string,
    fallback?: Partial<AiConversationWorkspaceContext>,
  ) {
    for (const conversation of collection.conversations) {
      applyConversationOwnership(conversation, scopeId, fallback);
    }
    return collection;
  }

  function updateWorkspaceConversationRoots(context: AiConversationWorkspaceContext) {
    const updateCollection = (collection: AiConversationCollection, persist: boolean) => {
      for (const conversation of collection.conversations) {
        if (conversation.workspaceId !== context.workspaceId) continue;
        const changed = conversation.workspaceName !== context.workspaceName
          || conversation.projectRoot !== context.projectRoot;
        conversation.workspaceName = context.workspaceName;
        conversation.projectRoot = context.projectRoot;
        if (changed && persist) markConversationDirty(conversation.id, "meta");
      }
    };
    updateCollection({
      version: 2,
      activeConversationId: activeConversationId.value,
      conversations: conversations.value,
    }, activeWorkspaceScopeId.value === context.scopeId);
    for (const [scopeId, collection] of inactiveWorkspaceConversations) {
      updateCollection(collection, scopeId === context.scopeId);
    }
    for (const overview of globalWorkspaceOverviews.value) {
      updateCollection(overview.collection, false);
    }
  }

  const allConversations = computed(() => {
    if (!conversationPreferences.globalMode) return conversations.value;
    const collections = new Map<string, AiConversationCollection>();
    for (const overview of globalWorkspaceOverviews.value) {
      collections.set(overview.scopeId, overview.collection);
    }
    for (const [scopeId, collection] of inactiveWorkspaceConversations) {
      collections.set(scopeId, collection);
    }
    collections.set(activeWorkspaceScopeId.value, {
      version: 2,
      activeConversationId: activeConversationId.value,
      conversations: conversations.value,
    });
    return [...collections.entries()].flatMap(([scopeId, collection]) =>
      collection.conversations.map((conversation) => applyConversationOwnership(conversation, scopeId)),
    );
  });

  async function refreshGlobalConversationOverviews() {
    if (!conversationPreferences.globalMode || globalOverviewsLoading.value) return;
    globalOverviewsLoading.value = true;
    try {
      const overviews = await listAiWorkspaceOverviews<AiConversationCollection>();
      globalWorkspaceOverviews.value = overviews
        .filter((item) => item?.collection?.version === 2 && Array.isArray(item.collection.conversations))
        .map((item) => ({
          scopeId: normalizeWorkspaceScopeId(item.scopeId),
          collection: collectionWithOwnership(item.collection, item.scopeId),
        }));
    } finally {
      globalOverviewsLoading.value = false;
    }
  }

  function resetWorkspaceConversationState() {
    const createdAt = Date.now();
    const serverId = createId("vue-ai");
    const conversationId = createId("conversation");
    mode.value = "agentic";
    selectedModel.value = "";
    selectedReasoningEffort.value = "";
    messages.value = [];
    agentHistory.value = [];
    agentEvents.value = [];
    pendingPermission.value = undefined;
    pendingResponseConversationIds.value = new Set();
    acpServerId.value = serverId;
    acpSessionId.value = "";
    acpAdapterId.value = "";
    agentReady.value = false;
    agentStarting.value = false;
    activeQueue.value = [];
    draft.value = "";
    draftAttachments.value = [];
    activeConversationId.value = conversationId;
    conversations.value = [
      {
        id: conversationId,
        workspaceId: activeWorkspaceScopeId.value === topWorkspaceScopeId.value
          ? topWorkspaceId.value
          : activeWorkspaceScopeId.value,
        workspaceName: activeWorkspaceScopeId.value === topWorkspaceScopeId.value
          ? topWorkspaceName.value
          : undefined,
        workspaceScopeId: activeWorkspaceScopeId.value,
        projectRoot: activeWorkspaceScopeId.value === topWorkspaceScopeId.value
          ? topWorkspaceProjectRoot.value
          : "",
        title: "",
        createdAt,
        updatedAt: createdAt,
        archived: false,
        pinned: false,
        status: "not_started",
        running: false,
        version: 1,
        mode: "agentic",
        messages: [],
        agentHistory: [],
        agentEvents: [],
        acpServerId: serverId,
        acpSessionId: "",
        agentReady: false,
        detailsLoaded: true,
      },
    ];
  }

  async function restoreWorkspaceConversationState(scopeId: string) {
    const storedConversations = await aiConversationPersistence.load(scopeId);
    if (restoreConversationCollection(storedConversations)) {
      await switchConversation(activeConversationId.value);
      return true;
    }

    return false;
  }

  async function hydrate(
    scopeId?: string,
    workspaceContext?: Partial<AiConversationWorkspaceContext>,
  ) {
    await conversationPreferences.hydrate();
    const context = rememberTopWorkspace({ ...workspaceContext, scopeId });
    let targetScopeId = conversationPreferences.globalMode
      ? normalizeWorkspaceScopeId(conversationPreferences.activeScopeId || context.scopeId)
      : context.scopeId;
    const preHydrateDraft = draft.value;
    const preHydrateDraftAttachments = draftAttachments.value;
    if (hydrated.value) {
      await followTopWorkspace(context);
      return;
    }

    const [storedWidth, storedConversationSidebarWidth, storedLayoutMode, storedPlacement] = await Promise.all([
      loadPersistedValue<unknown>(AI_ASSISTANT_WIDTH_KEY),
      loadPersistedValue<unknown>(AI_CONVERSATION_SIDEBAR_WIDTH_KEY),
      loadPersistedValue<unknown>(AI_ASSISTANT_LAYOUT_MODE_KEY),
      loadPersistedValue<unknown>(AI_ASSISTANT_PLACEMENT_KEY),
    ]);
    if (typeof storedWidth === "number" && Number.isFinite(storedWidth)) {
      width.value = Math.max(340, Math.min(window.innerWidth * 0.72, storedWidth));
    }
    if (storedLayoutMode === "overlay" || storedLayoutMode === "docked") {
      layoutMode.value = storedLayoutMode;
    }
    if (storedPlacement === "drawer" || storedPlacement === "tab") {
      placement.value = storedPlacement;
    }
    if (
      typeof storedConversationSidebarWidth === "number" &&
      Number.isFinite(storedConversationSidebarWidth)
    ) {
      conversationSidebarWidth.value = Math.max(
        AI_CONVERSATION_SIDEBAR_MIN_WIDTH,
        Math.min(AI_CONVERSATION_SIDEBAR_MAX_WIDTH, storedConversationSidebarWidth),
      );
    }
    activeWorkspaceScopeId.value = targetScopeId;
    for (const conversation of conversations.value) {
      applyConversationOwnership(
        conversation,
        targetScopeId,
        targetScopeId === context.scopeId ? context : undefined,
      );
    }
    let restoredWorkspace = await restoreWorkspaceConversationState(targetScopeId);
    if (!restoredWorkspace && targetScopeId !== context.scopeId) {
      // The remembered global AI scope may have been permanently deleted.
      // Fall back to the current top workspace instead of creating an orphan scope.
      targetScopeId = context.scopeId;
      activeWorkspaceScopeId.value = targetScopeId;
      resetWorkspaceConversationState();
      for (const conversation of conversations.value) {
        applyConversationOwnership(conversation, targetScopeId, context);
      }
      restoredWorkspace = await restoreWorkspaceConversationState(targetScopeId);
    }
    // 保留 AI 入口在 hydrate 前写入的提示词/附件（例如终端分析和会话总结）。
    if (preHydrateDraft.trim() || preHydrateDraftAttachments.length) {
      draft.value = preHydrateDraft;
      draftAttachments.value = preHydrateDraftAttachments;
    }
    if (pendingContextAttachments.value.length) {
      pendingContextAttachments.value = pendingContextAttachments.value.map((item) => ({
        ...item,
        workspaceScopeId: activeWorkspaceScopeId.value,
        conversationId: activeConversationId.value,
      }));
    }
    hydrated.value = true;
    if (conversationPreferences.globalMode) {
      void conversationPreferences.setActiveScopeId(targetScopeId);
      void refreshGlobalConversationOverviews();
    } else if (restoredWorkspace) {
      await activateTopWorkspaceConversation();
    }
    if (!restoredWorkspace) {
      markConversationDirty(activeConversationId.value);
      markWorkspaceMetaDirty(targetScopeId);
    } else if (preHydrateDraft.trim() || preHydrateDraftAttachments.length) {
      markConversationDirty(activeConversationId.value, "meta");
    }
    // 轻量界面设置不阻塞 AI 会话恢复。
    void Promise.all([
      savePersistedValue(AI_ASSISTANT_WIDTH_KEY, width.value),
      savePersistedValue(AI_ASSISTANT_LAYOUT_MODE_KEY, layoutMode.value),
      savePersistedValue(AI_ASSISTANT_PLACEMENT_KEY, placement.value),
      savePersistedValue(AI_CONVERSATION_SIDEBAR_WIDTH_KEY, conversationSidebarWidth.value),
    ]);
  }
  function setDraft(value: string) {
    draft.value = value;
    markConversationDirty(activeConversationId.value, "meta");
  }
  function setDraftAttachments(value: AiAttachment[]) {
    draftAttachments.value = sanitizeAiAttachments(value).map(compactAiAttachment);
    markConversationDirty(activeConversationId.value, "meta");
  }
  function setSelectedModel(value: string) {
    selectedModel.value = value.trim();
    markConversationDirty(activeConversationId.value, "meta");
  }
  function setSelectedReasoningEffort(value: string) {
    selectedReasoningEffort.value = value.trim().toLowerCase();
    markConversationDirty(activeConversationId.value, "meta");
  }
  // tab 呈现模式：所有 show() 入口统一导航到顶部 AI 标签页。
  function presentInTab() {
    const workbench = useWorkbenchStore();
    workbench.openView("ai");
    void router.push({ name: "ai" }).catch(() => undefined);
  }
  function show(nextMode: AiAssistantMode = mode.value) {
    mode.value = nextMode;
    markConversationDirty(activeConversationId.value, "meta");
    open.value = true;
    markConversationRead(activeConversationId.value);
    if (placement.value === "tab" && !isPopupWindow()) presentInTab();
  }
  function addContextAttachment(
    attachment: AiAttachment,
    target = { workspaceScopeId: activeWorkspaceScopeId.value, conversationId: activeConversationId.value },
  ) {
    if (
      target.workspaceScopeId !== activeWorkspaceScopeId.value ||
      target.conversationId !== activeConversationId.value
    ) return false;
    pendingContextAttachments.value = [
      ...pendingContextAttachments.value.filter((item) =>
        item.workspaceScopeId !== target.workspaceScopeId ||
        item.conversationId !== target.conversationId ||
        item.attachment.name !== attachment.name
      ),
      { ...target, attachment: { ...attachment } },
    ];
    show("agentic");
    return true;
  }
  function takeContextAttachments() {
    const matching = pendingContextAttachments.value.filter((item) =>
      item.workspaceScopeId === activeWorkspaceScopeId.value &&
      item.conversationId === activeConversationId.value
    );
    pendingContextAttachments.value = pendingContextAttachments.value.filter((item) => !matching.includes(item));
    return matching.map((item) => ({ ...item.attachment }));
  }
  function toggle() {
    if (placement.value === "tab" && !isPopupWindow()) {
      if (router.currentRoute.value.name === "ai") {
        open.value = false;
        void router.push({ name: "dashboard" }).catch(() => undefined);
        return;
      }
      show();
      return;
    }
    open.value = !open.value;
    if (open.value) markConversationRead(activeConversationId.value);
  }
  function close() { open.value = false; fullscreen.value = false; }
  function setPlacement(value: AiAssistantPlacement) {
    placement.value = value;
    if (hydrated.value) void savePersistedValue(AI_ASSISTANT_PLACEMENT_KEY, value);
  }
  function setLayoutMode(value: AiAssistantLayoutMode) {
    layoutMode.value = value;
    if (hydrated.value) void savePersistedValue(AI_ASSISTANT_LAYOUT_MODE_KEY, value);
  }
  function setWidth(value: number) {
    width.value = Math.max(340, Math.min(window.innerWidth * 0.72, value));
  }
  function persistWidth() {
    if (hydrated.value) void savePersistedValue(AI_ASSISTANT_WIDTH_KEY, width.value);
  }
  function setConversationSidebarWidth(value: number) {
    conversationSidebarWidth.value = Math.max(
      AI_CONVERSATION_SIDEBAR_MIN_WIDTH,
      Math.min(AI_CONVERSATION_SIDEBAR_MAX_WIDTH, Math.round(value)),
    );
  }
  function persistConversationSidebarWidth() {
    if (hydrated.value) {
      void savePersistedValue(AI_CONVERSATION_SIDEBAR_WIDTH_KEY, conversationSidebarWidth.value);
    }
  }
  const inactiveWorkspaceConversations = new Map<string, AiConversationCollection>();
  type DirtyConversationKind = "meta" | "details";
  const dirtyConversations = new Map<string, Map<string, DirtyConversationKind>>();
  const dirtyWorkspaceMeta = new Set<string>();
  const conversationDetailLoads = new Map<string, Promise<AiConversation | null>>();
  let conversationSwitchSequence = 0;

  function markWorkspaceMetaDirty(scopeId = activeWorkspaceScopeId.value) {
    if (!hydrated.value) return;
    dirtyWorkspaceMeta.add(scopeId);
    if (autoPersistencePauseDepth > 0) autoPersistenceDirty = true;
    else schedulePersistence();
  }

  function markConversationDirty(id: string, kind: DirtyConversationKind = "details") {
    if (!hydrated.value) return;
    const scopeId = workspaceScopeForConversation(id);
    if (!scopeId) return;
    const scopeDirty = dirtyConversations.get(scopeId) ?? new Map<string, DirtyConversationKind>();
    const previous = scopeDirty.get(id);
    if (previous !== "details") scopeDirty.set(id, kind);
    dirtyConversations.set(scopeId, scopeDirty);
    if (autoPersistencePauseDepth > 0) autoPersistenceDirty = true;
    else schedulePersistence();
  }


  function estimatedTextBytes(value: unknown, remaining: number, seen = new WeakSet<object>()): number {
    if (remaining <= 0) return 0;
    if (typeof value === "string") return Math.min(remaining, value.length * 2);
    if (!value || typeof value !== "object" || seen.has(value)) return 0;
    seen.add(value);

    let total = 0;
    const values = Array.isArray(value) ? value : Object.values(value);
    for (const item of values) {
      total += estimatedTextBytes(item, remaining - total, seen);
      if (total >= remaining) break;
    }
    return total;
  }

  function pruneInactiveWorkspaceConversations() {
    const limits = inactiveWorkspaceCacheLimits();
    // 0 表示不限制：对应维度不参与淘汰。
    const countLimited = limits.count > 0;
    const bytesLimited = limits.textBytes > 0;
    const entries = [...inactiveWorkspaceConversations.entries()].map(([scopeId, collection]) => ({
      scopeId,
      collection,
      bytes: estimatedTextBytes(collection, bytesLimited ? limits.textBytes + 1 : Number.POSITIVE_INFINITY),
      running: collection.conversations.some((conversation) => conversation.running),
      dirty: dirtyWorkspaceMeta.has(scopeId) || Boolean(dirtyConversations.get(scopeId)?.size),
    }));
    const idleEntries = entries.filter((entry) => !entry.running && !entry.dirty);
    // 运行中/未保存的缓存不可淘汰，计入体积只会把空闲缓存全部清空。
    let totalBytes = idleEntries.reduce((total, entry) => total + entry.bytes, 0);

    // 运行中的后台任务不能淘汰，否则后续事件将失去归属；空闲缓存同时受数量和文本体积约束。
    while (
      idleEntries.length > 0 &&
      ((countLimited && idleEntries.length > limits.count) ||
        (bytesLimited && totalBytes > limits.textBytes))
    ) {
      const evicted = idleEntries.shift();
      if (!evicted) break;
      inactiveWorkspaceConversations.delete(evicted.scopeId);
      totalBytes -= evicted.bytes;
    }
  }

  watch(settingsSnapshotVersion, pruneInactiveWorkspaceConversations);

  function workspaceOverviewCollection(collection: AiConversationCollection): AiConversationCollection {
    return {
      version: 2,
      activeConversationId: collection.activeConversationId,
      conversations: collection.conversations.map((conversation) => ({
        ...conversation,
        messages: [],
        agentHistory: [],
        agentEvents: [],
        messageQueue: [],
        pendingPermission: undefined,
        agentReady: false,
        detailsLoaded: false,
      })),
    };
  }

  function cacheInactiveWorkspaceConversation(
    scopeId: string,
    collection: AiConversationCollection,
  ) {
    // Map 的插入顺序作为 LRU，切回工作区时会从缓存中移除。
    inactiveWorkspaceConversations.delete(scopeId);
    inactiveWorkspaceConversations.set(scopeId, collection);
    if (conversationPreferences.globalMode) {
      const index = globalWorkspaceOverviews.value.findIndex((item) => item.scopeId === scopeId);
      const overview = { scopeId, collection: workspaceOverviewCollection(collection) };
      if (index >= 0) globalWorkspaceOverviews.value[index] = overview;
      else globalWorkspaceOverviews.value.push(overview);
    }
    pruneInactiveWorkspaceConversations();
  }

  function conversationById(id: string) {
    const active = conversations.value.find((conversation) => conversation.id === id);
    if (active) return active;
    for (const collection of inactiveWorkspaceConversations.values()) {
      const conversation = collection.conversations.find((item) => item.id === id);
      if (conversation) return conversation;
    }
    for (const overview of globalWorkspaceOverviews.value) {
      const conversation = overview.collection.conversations.find((item) => item.id === id);
      if (conversation) return conversation;
    }
    return undefined;
  }

  function workspaceScopeForConversation(id: string) {
    if (conversations.value.some((conversation) => conversation.id === id)) {
      return activeWorkspaceScopeId.value;
    }
    for (const [scopeId, collection] of inactiveWorkspaceConversations) {
      if (collection.conversations.some((conversation) => conversation.id === id)) return scopeId;
    }
    for (const overview of globalWorkspaceOverviews.value) {
      if (overview.collection.conversations.some((conversation) => conversation.id === id)) {
        return overview.scopeId;
      }
    }
    return undefined;
  }
  function messagePresentation(value: unknown): AiAssistantMessagePresentation | undefined {
    return value === "task-summary" || value === "error" || value === "loop-limit" ? value : undefined;
  }
  function cloneMessage(message: AiAssistantMessage): AiAssistantMessage {
    return {
      ...message,
      presentation: messagePresentation(message.presentation),
      tokenUsage: normalizeAiTokenUsage(message.tokenUsage),
      attachments: message.attachments?.map(compactAiAttachment),
    };
  }
  function addMessageToConversation(id: string, message: Omit<AiAssistantMessage, "id"> & { id?: string }) {
    const conversation = conversationById(id);
    if (!conversation) return undefined;
    if (message.id && conversation.messages.some((item) => item.id === message.id)) return message.id;
    const cloned = cloneMessage({
      ...message,
      id: message.id ?? createId("message"),
      createdAt: typeof message.createdAt === "number" && Number.isFinite(message.createdAt)
        ? message.createdAt
        : Date.now(),
    } as AiAssistantMessage);
    conversation.messages.push(cloned);
    if (!conversation.title) conversation.title = conversationTitle(conversation);
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) messages.value.push(cloneMessage(cloned));
    if (cloned.role === "assistant" && (cloned.content.trim() || cloned.attachments?.length)) {
      setConversationAwaitingResponse(id, false);
    }
    return cloned.id!;
  }
  function addMessage(message: Omit<AiAssistantMessage, "id"> & { id?: string }) {
    return addMessageToConversation(activeConversationId.value, message);
  }
  function cloneAgentHistory(history: AiLoopMessage[]) {
    return history.map((message) => ({
      ...message,
      attachments: message.attachments?.map(compactAiAttachment),
      toolCalls: message.toolCalls?.map((call) => ({ ...call })),
      toolResult: message.toolResult
        ? {
            ...message.toolResult,
            source: message.toolResult.source
              ? { ...message.toolResult.source }
              : undefined,
          }
        : undefined,
    }));
  }
  function persistedAgentHistory(history: AiLoopMessage[]): AiLoopMessage[] {
    return history.map((message) => {
      const attachments = message.attachments?.map((attachment) => ({
        id: attachment.id,
        name: attachment.name,
        mimeType: attachment.mimeType,
        size: attachment.size,
        kind: attachment.kind,
      } as AiAttachment));
      const toolCalls = message.toolCalls?.map((call) => ({
        id: call.id,
        name: call.name,
        arguments: "{}",
      }));
      const toolResult = message.toolResult
        ? {
            status: message.toolResult.status,
            source: message.toolResult.source
              ? {
                  kind: message.toolResult.source.kind,
                  toolName: message.toolResult.source.toolName,
                  ...(message.toolResult.source.serverId !== undefined
                    ? { serverId: message.toolResult.source.serverId }
                    : {}),
                  ...(message.toolResult.source.serverName !== undefined
                    ? { serverName: message.toolResult.source.serverName }
                    : {}),
                  ...(message.toolResult.source.resourceId !== undefined
                    ? { resourceId: message.toolResult.source.resourceId }
                    : {}),
                }
              : undefined,
          }
        : undefined;
      const source = toolResult?.source;
      const provenance = source
        ? source.kind === "native"
          ? `${source.kind}:${source.toolName}`
          : `${source.kind}:${source.serverName ?? source.serverId}/${source.toolName}`
        : "tool";
      const content = message.role === "tool"
        ? `[${provenance}] ${toolResult?.status ?? "completed"}`.slice(0, 240)
        : message.content;
      return {
        role: message.role,
        content,
        ...(attachments?.length ? { attachments } : {}),
        ...(toolCalls?.length ? { toolCalls } : {}),
        ...(message.toolCallId !== undefined ? { toolCallId: message.toolCallId } : {}),
        ...(toolResult ? { toolResult } : {}),
      };
    });
  }
  /**
   * Removes a user message and everything after it (messages, their events and
   * the matching model history) so the turn can be sent again. Returns the
   * removed message, or undefined when the conversation cannot be rewound.
   */
  function rewindConversationTo(id: string, messageId: string) {
    const conversation = conversationById(id);
    if (!conversation || conversation.running || conversation.detailsLoaded === false) return undefined;
    const rewound = rewindConversation({
      messages: conversation.messages,
      agentEvents: conversation.agentEvents,
      agentHistory: conversation.agentHistory ?? [],
    }, messageId);
    if (!rewound) return undefined;
    conversation.messages = rewound.messages;
    conversation.agentEvents = rewound.agentEvents;
    conversation.agentHistory = rewound.agentHistory;
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      messages.value = rewound.messages.map(cloneMessage);
      agentEvents.value = rewound.agentEvents.map((event) => ({ ...event }));
      agentHistory.value = cloneAgentHistory(rewound.agentHistory);
    }
    // The removed turns' trees may now be unreferenced (deferred while any turn runs).
    return cloneMessage(rewound.removed);
  }
  function setConversationAgentHistory(id: string, history: AiLoopMessage[]) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    conversation.agentHistory = cloneAgentHistory(history);
    markConversationDirty(id);
    conversation.updatedAt = Date.now();
    if (id === activeConversationId.value) agentHistory.value = cloneAgentHistory(history);
    return true;
  }
  /** `moveEventsTo` keeps the removed message's events attached to the message replacing it. */
  function removeMessageFromConversation(id: string, messageId: string, moveEventsTo?: string) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const index = conversation.messages.findIndex((message) => message.id === messageId);
    if (index < 0) return false;
    if (moveEventsTo && conversation.messages.some((message) => message.id === moveEventsTo)) {
      const moved = reanchorAiEvents(conversation.agentEvents, messageId, moveEventsTo);
      if (moved !== conversation.agentEvents) {
        conversation.agentEvents = moved;
        if (id === activeConversationId.value) agentEvents.value = moved.map((event) => ({ ...event }));
      }
    }
    conversation.messages.splice(index, 1);
    markConversationDirty(id);
    conversation.updatedAt = Date.now();
    if (id === activeConversationId.value) messages.value.splice(index, 1);
    return true;
  }
  /** Marks an assistant reply as a special delivery (the task summary card). */
  function setMessagePresentation(id: string, messageId: string, presentation: AiAssistantMessagePresentation) {
    const conversation = conversationById(id);
    const stored = conversation?.messages.find((message) => message.id === messageId);
    if (!conversation || !stored || stored.role !== "assistant") return false;
    stored.presentation = presentation;
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      const active = messages.value.find((message) => message.id === messageId);
      if (active) active.presentation = presentation;
    }
    return true;
  }
  function setMessageTokenUsage(id: string, messageId: string, usage: AiTokenUsage) {
    const conversation = conversationById(id);
    const normalized = normalizeAiTokenUsage(usage);
    if (!conversation || !normalized) return false;
    const stored = conversation.messages.find((message) => message.id === messageId);
    if (!stored) return false;
    stored.tokenUsage = normalized;
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      const active = messages.value.find((message) => message.id === messageId);
      if (active) active.tokenUsage = { ...normalized, models: normalized.models ? [...normalized.models] : undefined };
    }
    return true;
  }
  // Streaming touches a conversation every frame; updating updatedAt at most once a
  // second keeps the sidebar and its watchers from re-rendering per chunk. The run's
  // end stamps the exact time.
  function touchStreamingConversation(conversation: AiConversation) {
    const now = Date.now();
    if (now - conversation.updatedAt >= STREAMING_UPDATED_AT_INTERVAL_MS) conversation.updatedAt = now;
  }
  function updateLastAssistantForConversation(id: string, content: string) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const storedLast = conversation.messages[conversation.messages.length - 1];
    if (storedLast?.role === "assistant") storedLast.content += content;
    else conversation.messages.push({ id: createId("message"), role: "assistant", content, createdAt: Date.now() });
    touchStreamingConversation(conversation);
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      const activeLast = messages.value[messages.value.length - 1];
      if (activeLast?.role === "assistant") activeLast.content += content;
      else {
        const created = conversation.messages[conversation.messages.length - 1];
        if (created) messages.value.push(cloneMessage(created));
      }
    }
    if (content.trim()) setConversationAwaitingResponse(id, false);
    return true;
  }
  function updateLastAssistant(content: string) {
    return updateLastAssistantForConversation(activeConversationId.value, content);
  }
  function appendAssistantMessageForConversation(id: string, messageId: string, content: string) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const stored = conversation.messages.find((message) => message.id === messageId);
    if (stored) {
      stored.content += content;
      stored.eventAnchor = false;
    } else conversation.messages.push({ id: messageId, role: "assistant", content, createdAt: Date.now() });
    touchStreamingConversation(conversation);
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      const active = messages.value.find((message) => message.id === messageId);
      if (active) {
        active.content += content;
        active.eventAnchor = false;
      } else {
        const created = conversation.messages.find((message) => message.id === messageId);
        if (created) messages.value.push(cloneMessage(created));
      }
    }
    if (content.trim()) setConversationAwaitingResponse(id, false);
    return true;
  }
  function upsertAgentEventForConversation(id: string, event: AiAssistantEvent) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const index = conversation.agentEvents.findIndex(
      (item) => item.id === event.id && item.triggerMessageId === event.triggerMessageId,
    );
    const previous = index >= 0 ? conversation.agentEvents[index] : undefined;
    const merged = safeAgentEvent({ ...(previous ?? {}), ...event });
    const sanitized = applyAiAgentEventTiming(previous, merged);
    if (index >= 0) conversation.agentEvents[index] = sanitized;
    else conversation.agentEvents.push(sanitized);
    touchStreamingConversation(conversation);
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      const activeIndex = agentEvents.value.findIndex(
        (item) => item.id === event.id && item.triggerMessageId === event.triggerMessageId,
      );
      if (activeIndex >= 0) agentEvents.value[activeIndex] = { ...agentEvents.value[activeIndex], ...sanitized };
      else agentEvents.value.push({ ...sanitized });
    }
    if (conversation.running) setConversationAwaitingResponse(id, true);
    return true;
  }
  function upsertAgentEvent(event: AiAssistantEvent) {
    return upsertAgentEventForConversation(activeConversationId.value, event);
  }
  function removeAgentEventForConversation(id: string, event: Pick<AiAssistantEvent, "id" | "triggerMessageId">) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const nextEvents = conversation.agentEvents.filter(
      (item) => item.id !== event.id || item.triggerMessageId !== event.triggerMessageId,
    );
    if (nextEvents.length === conversation.agentEvents.length) return false;
    conversation.agentEvents = nextEvents;
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      agentEvents.value = agentEvents.value.filter(
        (item) => item.id !== event.id || item.triggerMessageId !== event.triggerMessageId,
      );
    }
    return true;
  }
  function clearAgentEvents(id: string) {
    const conversation = conversationById(id);
    if (!conversation || !conversation.agentEvents.length) return false;
    conversation.agentEvents = [];
    markConversationDirty(id);
    conversation.updatedAt = Date.now();
    if (id === activeConversationId.value) agentEvents.value = [];
    return true;
  }
  function anchorUnassignedAgentEvents(id: string, triggerMessageId: string) {
    const conversation = conversationById(id);
    if (!conversation?.messages.some((message) => message.id === triggerMessageId)) return false;
    const anchored = anchorUnassignedAiEvents(
      conversation.messages,
      conversation.agentEvents,
      triggerMessageId,
    );
    if (anchored.every((event, index) => event === conversation.agentEvents[index])) return false;
    conversation.agentEvents = anchored;
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      agentEvents.value = anchored.map((event) => ({ ...event }));
    }
    return true;
  }
  function settleRunningAgentEvents(
    id: string,
    status: string,
    kind?: AiAssistantEvent["kind"],
  ) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const settled = settledAgentEvents(conversation.agentEvents, status, Date.now(), kind);
    if (settled.every((event, index) => event === conversation.agentEvents[index])) return false;
    conversation.agentEvents = settled;
    conversation.updatedAt = Date.now();
    markConversationDirty(id);
    if (id === activeConversationId.value) {
      agentEvents.value = conversation.agentEvents.map((event: AiAssistantEvent) => ({ ...event }));
    }
    return true;
  }
  function cloneQueuedMessage(message: AiQueuedMessage): AiQueuedMessage {
    return {
      ...message,
      attachments: message.attachments?.map(compactAiAttachment),
    };
  }
  function enqueueMessage(conversationId: string, message: Omit<AiQueuedMessage, "id"> & { id?: string }) {
    const conversation = conversationById(conversationId);
    if (!conversation) return undefined;
    if (message.id && conversation.messageQueue?.some((item) => item.id === message.id)) return message.id;
    const queued: AiQueuedMessage = cloneQueuedMessage({
      ...message,
      id: message.id ?? createId("queued"),
    } as AiQueuedMessage);
    if (!conversation.messageQueue) conversation.messageQueue = [];
    conversation.messageQueue.push(queued);
    conversation.updatedAt = Date.now();
    markConversationDirty(conversationId);
    if (conversationId === activeConversationId.value) activeQueue.value.push(cloneQueuedMessage(queued));
    return queued.id;
  }
  function prioritizeQueuedMessage(conversationId: string, messageId: string) {
    const conversation = conversationById(conversationId);
    if (!conversation?.messageQueue?.length) return false;
    const index = conversation.messageQueue.findIndex((message) => message.id === messageId);
    if (index < 0) return false;
    if (index > 0) {
      const [message] = conversation.messageQueue.splice(index, 1);
      conversation.messageQueue.unshift(message);
      conversation.updatedAt = Date.now();
      markConversationDirty(conversationId);
      if (conversationId === activeConversationId.value) {
        activeQueue.value = conversation.messageQueue.map(cloneQueuedMessage);
      }
    }
    return true;
  }
  function dequeueMessage(conversationId: string): AiQueuedMessage | undefined {
    const conversation = conversationById(conversationId);
    if (!conversation?.messageQueue?.length) return undefined;
    const [removed] = conversation.messageQueue.splice(0, 1);
    conversation.updatedAt = Date.now();
    markConversationDirty(conversationId);
    if (conversationId === activeConversationId.value) {
      activeQueue.value = conversation.messageQueue.map(cloneQueuedMessage);
    }
    return removed;
  }
  function removeQueuedMessage(conversationId: string, messageId: string) {
    const conversation = conversationById(conversationId);
    if (!conversation?.messageQueue) return false;
    const index = conversation.messageQueue.findIndex((message) => message.id === messageId);
    if (index < 0) return false;
    conversation.messageQueue.splice(index, 1);
    conversation.updatedAt = Date.now();
    markConversationDirty(conversationId);
    if (conversationId === activeConversationId.value) {
      activeQueue.value = conversation.messageQueue.map(cloneQueuedMessage);
    }
    return true;
  }
  function updateQueuedMessage(conversationId: string, messageId: string, updates: { content?: string; attachments?: AiAttachment[] }) {
    const conversation = conversationById(conversationId);
    if (!conversation?.messageQueue) return false;
    const target = conversation.messageQueue.find((message) => message.id === messageId);
    if (!target) return false;
    if (updates.content !== undefined) target.content = updates.content;
    if (updates.attachments !== undefined) target.attachments = updates.attachments.map((attachment) => ({ ...attachment }));
    conversation.updatedAt = Date.now();
    markConversationDirty(conversationId);
    if (conversationId === activeConversationId.value) {
      activeQueue.value = conversation.messageQueue.map(cloneQueuedMessage);
    }
    return true;
  }
  function queueLength(conversationId: string) {
    return conversationById(conversationId)?.messageQueue?.length ?? 0;
  }
  function snapshot(): AiAssistantSnapshot {
    return {
      version: 1,
      mode: mode.value,
      draft: draft.value,
      draftAttachments: draftAttachments.value.map(compactAiAttachment),
      selectedModel: selectedModel.value,
      selectedReasoningEffort: selectedReasoningEffort.value,
      messages: messages.value.map((message) => ({
        ...message,
        attachments: message.attachments?.map(compactAiAttachment),
      })),
      agentHistory: cloneAgentHistory(agentHistory.value),
      agentEvents: agentEvents.value.map(safeAgentEvent),
      messageQueue: activeQueue.value.map(cloneQueuedMessage),
      pendingPermission: pendingPermission.value
        ? { ...pendingPermission.value, options: pendingPermission.value.options.map((option) => ({ ...option })) }
        : undefined,
      acpServerId: acpServerId.value,
      acpSessionId: acpSessionId.value,
      ...(acpAdapterId.value ? { acpAdapterId: acpAdapterId.value } : {}),
      agentReady: agentReady.value,
    };
  }
  function runtimeConversationSnapshot(): AiAssistantSnapshot {
    return { ...snapshot(), pendingPermission: undefined, agentReady: false };
  }
  function persistedSnapshot(): AiAssistantSnapshot {
    return {
      ...runtimeConversationSnapshot(),
      agentHistory: persistedAgentHistory(agentHistory.value),
    };
  }
  function conversationTitle(snapshotValue: AiAssistantSnapshot) {
    const firstUserMessage = snapshotValue.messages.find((message) => message.role === "user")?.content.trim();
    if (!firstUserMessage) return "";
        return normalizeAiConversationTitle(firstUserMessage, 48);
  }
  function syncActiveConversation(updateTime = false) {
    const conversation = activeConversation.value;
    if (!conversation) return;
    // 工作区切换只隐藏会话，不结束运行态；缓存必须保留 ACP 会话和待审批状态。
    Object.assign(conversation, snapshot());
    conversation.messageQueue = activeQueue.value.map(cloneQueuedMessage);
    if (!conversation.title) conversation.title = conversationTitle(conversation);
    if (updateTime) conversation.updatedAt = Date.now();
  }
  function setConversationWorkspace(
    id: string,
    context: AiConversationWorkspaceContext,
  ) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const normalized = normalizeWorkspaceContext(context);
    conversation.workspaceId = normalized.workspaceId;
    conversation.workspaceName = normalized.workspaceName;
    conversation.workspaceScopeId ||= activeWorkspaceScopeId.value;
    conversation.projectRoot = normalized.projectRoot;
    markConversationDirty(id, "meta");
    return true;
  }

  function newConversation(nextMode: AiAssistantMode, owner: Pick<AiConversation,
    "workspaceId" | "workspaceName" | "workspaceScopeId" | "projectRoot">): AiConversation {
    const createdAt = Date.now();
    return {
      ...owner,
      id: createId("conversation"),
      title: "",
      createdAt,
      updatedAt: createdAt,
      archived: false,
      pinned: false,
      status: "not_started",
      running: false,
      version: 1,
      mode: nextMode,
      messages: [],
      agentHistory: [],
      agentEvents: [],
      acpServerId: createId("vue-ai"),
      acpSessionId: "",
      agentReady: false,
      detailsLoaded: true,
    };
  }
  function createConversation(nextMode: AiAssistantMode = mode.value) {
    syncActiveConversation();
    const conversation = newConversation(nextMode, {
      workspaceId: topWorkspaceId.value,
      workspaceName: topWorkspaceName.value || undefined,
      workspaceScopeId: activeWorkspaceScopeId.value,
      projectRoot: topWorkspaceProjectRoot.value,
    });
    conversations.value.unshift(conversation);
    activeConversationId.value = conversation.id;
    markWorkspaceMetaDirty();
    markConversationDirty(conversation.id);
    restoreSnapshot(conversation, false, false);
    return conversation;
  }
  function createDelegatedConversation(
    parentId: string, title: string, source: "manual" | "parallel", model?: string,
    origin?: Pick<NonNullable<AiConversation["delegation"]>, "toolCallId" | "triggerMessageId" | "turnMessageId">,
  ) {
    const parent = conversationById(parentId);
    const scopeId = workspaceScopeForConversation(parentId);
    if (!parent || parent.external || !scopeId) return undefined;
    const inactive = inactiveWorkspaceConversations.get(scopeId);
    const items = scopeId === activeWorkspaceScopeId.value ? conversations.value : inactive?.conversations;
    if (!items) return undefined;
    const child = newConversation("agentic", {
      workspaceId: parent.workspaceId,
      workspaceName: parent.workspaceName,
      workspaceScopeId: scopeId,
      projectRoot: parent.projectRoot,
    });
    child.title = normalizeAiConversationTitle(title);
    child.delegation = { parentId, source, ...origin };
    child.selectedModel = model ?? (parentId === activeConversationId.value ? selectedModel.value : parent.selectedModel);
    child.selectedReasoningEffort = parentId === activeConversationId.value
      ? selectedReasoningEffort.value
      : parent.selectedReasoningEffort;
    items.unshift(child);
    if (inactive) cacheInactiveWorkspaceConversation(scopeId, inactive);
    markWorkspaceMetaDirty(scopeId);
    markConversationDirty(child.id);
    return child;
  }
  function recordDelegatedConversationResult(id: string, result: string) {
    const conversation = conversationById(id);
    if (!conversation?.delegation || conversation.delegation.result !== undefined) return;
    conversation.delegation.result = safeAiText(result, 4_000);
    conversation.delegation.resultStatus = conversation.status;
    markConversationDirty(id, "meta");
  }
  function delegatedConversationReports(parentId: string) {
    const scopeId = workspaceScopeForConversation(parentId);
    if (!scopeId) return [];
    return (workspaceCollection(scopeId)?.conversations ?? [])
      .filter((child) => child.delegation?.parentId === parentId
        && child.delegation.source === "manual" && child.delegation.resultStatus === "completed"
        && child.delegation.result)
      .slice(0, 4)
      .map((child) => ({ title: child.title, report: child.delegation!.result! }));
  }
  async function switchConversation(id: string) {
    const conversation = conversations.value.find((item) => item.id === id);
    if (id === activeConversationId.value && conversation?.detailsLoaded !== false) {
      setConversationUnread(id, false);
      return true;
    }
    if (!conversation) return false;
    const requestId = ++conversationSwitchSequence;
    let target = conversation;
    if (conversation.detailsLoaded === false) {
      workspaceSwitching.value = true;
      try {
        const loaded = await loadConversationDetail(activeWorkspaceScopeId.value, id);
        if (requestId !== conversationSwitchSequence || !loaded) return false;
        const current = conversationById(id);
        if (!current) return false;
        // 概览里的元数据（标题、未读、运行态、排队消息）可能比磁盘上的更新，只补齐详情。
        if (current.detailsLoaded === false) mergeLoadedConversationDetails(current, loaded);
        target = applyConversationOwnership(current, activeWorkspaceScopeId.value);
      } finally {
        if (requestId === conversationSwitchSequence) workspaceSwitching.value = false;
      }
    }
    if (requestId !== conversationSwitchSequence) return false;
    const activeChanged = id !== activeConversationId.value;
    if (activeChanged) syncActiveConversation();
    activeConversationId.value = id;
    if (activeChanged) markWorkspaceMetaDirty();
    restoreSnapshot(target, false, false);
    setConversationUnread(id, false);
    noteConversationViewed(id);
    return true;
  }

  // One in-flight detail load per conversation, shared by every caller.
  function loadConversationDetail(scopeId: string, id: string) {
    const key = `${scopeId}\u0000${id}`;
    let load = conversationDetailLoads.get(key);
    if (!load) {
      load = aiConversationPersistence.loadDetail(scopeId, id);
      conversationDetailLoads.set(key, load);
      const cleanup = () => {
        if (conversationDetailLoads.get(key) === load) conversationDetailLoads.delete(key);
      };
      void load.then(cleanup, cleanup);
    }
    return load;
  }

  /**
   * Loads messages, loop history and events of an overview-only conversation in
   * place, without making it active. Anything that reads or appends to a
   * conversation that may not be open (queued or remote sends) calls this first.
   * In-memory metadata is kept because it can be newer than the saved copy.
   */
  async function ensureConversationDetails(id: string) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    if (conversation.detailsLoaded !== false) return true;
    const scopeId = workspaceScopeForConversation(id);
    if (!scopeId) return false;
    const loaded = await loadConversationDetail(scopeId, id);
    const current = conversationById(id);
    if (!loaded || !current) return false;
    if (current.detailsLoaded !== false) return true;
    mergeLoadedConversationDetails(current, loaded);
    if (id === activeConversationId.value) restoreSnapshot(current, false, false);
    return true;
  }

  function mergeById<T extends { id?: string }>(saved: readonly T[] | undefined, live: readonly T[] | undefined, key = (item: T) => item.id ?? "") {
    const merged = [...(saved ?? [])];
    const known = new Set(merged.map(key));
    for (const item of live ?? []) {
      const itemKey = key(item);
      if (!itemKey || !known.has(itemKey)) merged.push(item);
    }
    return merged;
  }

  /**
   * Fills an overview-only conversation with its saved details. Anything added in
   * memory while only the overview was loaded is kept instead of being overwritten.
   */
  function mergeLoadedConversationDetails(current: AiConversation, loaded: AiConversation) {
    current.messages = mergeById(loaded.messages, current.messages);
    current.agentHistory = current.agentHistory?.length ? current.agentHistory : loaded.agentHistory;
    current.agentEvents = mergeById(
      loaded.agentEvents,
      current.agentEvents,
      (event) => `${event.id}\u0000${event.triggerMessageId ?? ""}`,
    );
    current.messageQueue = mergeById(loaded.messageQueue, current.messageQueue);
    current.detailsLoaded = true;
  }

  // Conversations the user moved away from drop back to their overview so long
  // histories do not stay in memory; the most recently viewed ones stay loaded.
  const recentlyViewedConversationIds: string[] = [];
  function noteConversationViewed(id: string) {
    const index = recentlyViewedConversationIds.indexOf(id);
    if (index >= 0) recentlyViewedConversationIds.splice(index, 1);
    recentlyViewedConversationIds.unshift(id);
    recentlyViewedConversationIds.length = Math.min(recentlyViewedConversationIds.length, RETAINED_LOADED_CONVERSATIONS);
    releaseUnviewedConversationDetails();
  }
  function releaseUnviewedConversationDetails() {
    const keep = new Set([activeConversationId.value, ...recentlyViewedConversationIds]);
    const dirty = dirtyConversations.get(activeWorkspaceScopeId.value);
    for (const conversation of conversations.value) {
      if (keep.has(conversation.id) || conversation.detailsLoaded === false) continue;
      if (
        conversation.running
        || conversation.mode === "agent"
        || conversation.messageQueue?.length
        || conversation.pendingPermission
        || dirty?.has(conversation.id)
      ) continue;
      conversation.messages = [];
      conversation.agentHistory = [];
      conversation.agentEvents = [];
      conversation.detailsLoaded = false;
    }
  }

  function renameConversation(id: string, title: string) {
    const conversation = conversations.value.find((item) => item.id === id);
    const normalizedTitle = normalizeAiConversationTitle(title);
    if (!conversation || !normalizedTitle) return false;
    if (conversation.title === normalizedTitle) return true;
    conversation.title = normalizedTitle;
    conversation.updatedAt = Date.now();
    markConversationDirty(id, "meta");
    return true;
  }
  async function conversationFamily(id: string) {
    const overviews = await listAiWorkspaceOverviews<AiConversationCollection>();
    const records = new Map<string, { location: AiConversationLocation; conversation: AiConversation }>();
    const collect = (scopeId: string, items: AiConversation[]) => {
      for (const conversation of items) {
        records.set(conversation.id, { location: { scopeId, conversationId: conversation.id }, conversation });
      }
    };
    for (const overview of overviews) collect(overview.scopeId, overview.collection.conversations);
    for (const [scopeId, collection] of inactiveWorkspaceConversations) collect(scopeId, collection.conversations);
    collect(activeWorkspaceScopeId.value, conversations.value);
    const children = new Map<string, string[]>();
    for (const [childId, { conversation }] of records) {
      const parentId = conversation.delegation?.parentId;
      if (!parentId) continue;
      const siblings = children.get(parentId) ?? [];
      siblings.push(childId);
      children.set(parentId, siblings);
    }
    const result: Array<{ location: AiConversationLocation; conversation: AiConversation }> = [];
    const visited = new Set<string>();
    const pending = [id];
    while (pending.length) {
      const next = pending.pop()!;
      if (visited.has(next)) continue;
      visited.add(next);
      const record = records.get(next);
      if (record) result.push(record);
      pending.push(...(children.get(next) ?? []));
    }
    return result;
  }

  async function setConversationArchived(id: string, archived: boolean) {
    const family = await conversationFamily(id);
    if (!family.length) return false;
    await flushPersistence();
    await archiveAiConversations(family.map(({ location }) => location), archived);
    for (const { location } of family) {
      const conversation = conversationById(location.conversationId);
      if (!conversation) continue;
      conversation.archived = archived;
      conversation.updatedAt = Date.now();
      markConversationDirty(conversation.id, "meta");
    }
    if (conversationPreferences.globalMode) void refreshGlobalConversationOverviews();
    return true;
  }
  function setConversationPinned(id: string, pinned: boolean) {
    const conversation = conversations.value.find((item) => item.id === id);
    if (!conversation) return false;
    conversation.pinned = pinned;
    markConversationDirty(id, "meta");
    return true;
  }
  function acpStateForWorkspaceMove(conversation: AiConversation, projectRoot: string) {
    const previousRoot = conversation.projectRoot?.trim() || "";
    if (previousRoot === projectRoot) return undefined;
    const saved = Array.isArray(conversation.acpProjectSessions) ? conversation.acpProjectSessions : [];
    const recovery = saved.find((session) => session?.projectRoot === projectRoot);
    const otherProjects = saved.filter((session) => session && session.projectRoot !== projectRoot && session.projectRoot !== previousRoot);
    if (!conversation.acpSessionId && !recovery) return undefined;
    if (conversation.acpSessionId) {
      otherProjects.push({ projectRoot: previousRoot, sessionId: conversation.acpSessionId, adapterId: conversation.acpAdapterId });
    }
    return {
      acpProjectSessions: otherProjects,
      acpSessionId: recovery?.sessionId ?? "",
      acpAdapterId: recovery?.adapterId ?? "",
      agentReady: false,
    };
  }

  async function moveConversationToWorkspace(
    id: string,
    context: AiConversationWorkspaceContext,
  ) {
    const sourceScopeId = workspaceScopeForConversation(id);
    const targetContext = normalizeWorkspaceContext(context);
    if (!sourceScopeId || sourceScopeId !== activeWorkspaceScopeId.value) return false;
    if (sourceScopeId === targetContext.scopeId) {
      const conversation = conversationById(id);
      if (!conversation || (conversation.mode === "agent" && conversation.running)) return false;
      const acpState = acpStateForWorkspaceMove(conversation, targetContext.projectRoot);
      if (!setConversationWorkspace(id, targetContext)) return false;
      if (acpState) {
        conversation.acpProjectSessions = acpState.acpProjectSessions;
        setConversationAgentState(id, acpState);
      }
      return true;
    }
    if (!await switchConversation(id)) return false;
    const conversation = conversations.value.find((item) => item.id === id);
    if (!conversation || conversation.running || conversation.detailsLoaded === false) return false;

    syncActiveConversation();
    await flushPersistence();
    const sourceRemaining = conversations.value.filter((item) => item.id !== id);
    const sourceActiveConversationId = sourceRemaining[0]?.id ?? "";
    const targetOverview = await aiConversationPersistence.load(targetContext.scopeId);
    const targetConversationIds = targetOverview?.conversations
      ?.map((item) => item.id)
      .filter((conversationId) => conversationId !== id) ?? [];
    // 走持久化清洗（去掉运行态、审批与工具大参数），与常规保存保持一致。
    const movedConversation: AiConversation = {
      ...persistedConversation(conversation, true),
      ...acpStateForWorkspaceMove(conversation, targetContext.projectRoot),
      workspaceId: targetContext.workspaceId,
      workspaceName: targetContext.workspaceName,
      workspaceScopeId: targetContext.scopeId,
      projectRoot: targetContext.projectRoot,
      detailsLoaded: true,
    };

    await moveAiConversation({
      sourceScopeId,
      targetScopeId: targetContext.scopeId,
      conversationId: id,
      conversation: movedConversation,
      sourceActiveConversationId,
      sourceConversationIds: sourceRemaining.map((item) => item.id),
      targetConversationIds,
    });

    dirtyConversations.get(sourceScopeId)?.delete(id);
    dirtyWorkspaceMeta.delete(sourceScopeId);
    conversations.value = sourceRemaining;
    activeConversationId.value = sourceActiveConversationId;
    pendingResponseConversationIds.value.delete(id);
    if (sourceRemaining[0]) restoreSnapshot(sourceRemaining[0], false, false);
    inactiveWorkspaceConversations.delete(targetContext.scopeId);
    globalWorkspaceOverviews.value = globalWorkspaceOverviews.value.filter(
      (item) => item.scopeId !== sourceScopeId && item.scopeId !== targetContext.scopeId,
    );
    workspaceConversationLoads.delete(sourceScopeId);
    workspaceConversationLoads.delete(targetContext.scopeId);
    if (prefetchedWorkspaceConversation?.scopeId === targetContext.scopeId) {
      prefetchedWorkspaceConversation = undefined;
    }
    await switchWorkspaceScope(targetContext.scopeId);
    await switchConversation(id);
    if (conversationPreferences.globalMode) void refreshGlobalConversationOverviews();
    return true;
  }

  async function deleteConversation(id: string, relatedConversations: AiConversationLocation[] = []) {
    const index = conversations.value.findIndex((item) => item.id === id);
    const target = conversations.value[index];
    if (index < 0 || !target || target.running || conversationDeletionInProgress) return false;
    const deletedIds = new Set([id, ...relatedConversations.map((item) => item.conversationId)]);
    if (conversations.value.some((item) => deletedIds.has(item.id) && item.running)) return false;

    // 先同步活动态，失败回滚时可直接复用原对象，避免深拷贝大段会话历史。
    syncActiveConversation();
    const scopeId = activeWorkspaceScopeId.value;
    const previousConversations = [...conversations.value];
    const previousActiveConversationId = activeConversationId.value;
    const previousPendingResponseIds = new Set(pendingResponseConversationIds.value);
    const previousPendingContextAttachments = pendingContextAttachments.value;
    const previousDirtyConversations = new Map(dirtyConversations.get(scopeId) ?? []);
    const previousWorkspaceMetaDirty = dirtyWorkspaceMeta.has(scopeId);
    const previousConversationIds = new Set(previousConversations.map((conversation) => conversation.id));

    conversationDeletionInProgress = true;
    pauseAutoPersistence();
    try {
      conversations.value = conversations.value.filter((item) => !deletedIds.has(item.id));
      for (const deletedId of deletedIds) setConversationAwaitingResponse(deletedId, false);
      pendingContextAttachments.value = pendingContextAttachments.value.filter(
        (item) => !deletedIds.has(item.conversationId),
      );
      const scopeCleared = !conversations.value.length;
      if (scopeCleared) {
        createConversation();
      } else if (deletedIds.has(activeConversationId.value)) {
        activeConversationId.value = conversations.value[0].id;
        restoreSnapshot(conversations.value[0], false, false);
      }

      if (scopeCleared) {
        await replaceAiConversation(
          scopeId,
          id,
          persistedConversation(conversations.value[0], true),
          relatedConversations,
        );
      } else {
        await deleteAiConversation(
          scopeId,
          id,
          activeConversationId.value,
          conversations.value.map((conversation) => conversation.id),
          relatedConversations,
        );
      }
      const scopeDirty = dirtyConversations.get(scopeId);
      for (const deletedId of deletedIds) scopeDirty?.delete(deletedId);
      if (scopeDirty && scopeDirty.size === 0) dirtyConversations.delete(scopeId);
      dirtyWorkspaceMeta.delete(scopeId);
      if (dirtyConversations.get(scopeId)?.size) autoPersistenceDirty = true;
      const otherScopes = new Set(relatedConversations.map((item) => item.scopeId));
      otherScopes.delete(scopeId);
      for (const otherScope of otherScopes) {
        const cached = inactiveWorkspaceConversations.get(otherScope);
        const overview = globalWorkspaceOverviews.value.find((item) => item.scopeId === otherScope)?.collection;
        for (const collection of new Set([cached, overview])) {
          if (!collection) continue;
          collection.conversations = collection.conversations.filter((item) => !deletedIds.has(item.id));
          if (deletedIds.has(collection.activeConversationId)) {
            collection.activeConversationId = collection.conversations[0]?.id ?? "";
          }
        }
        const dirty = dirtyConversations.get(otherScope);
        for (const deletedId of deletedIds) dirty?.delete(deletedId);
        if (dirty?.size === 0) dirtyConversations.delete(otherScope);
      }
      if (activeConversation.value?.detailsLoaded === false) {
        void switchConversation(activeConversationId.value).catch(() => undefined);
      }
      // The deleted turns' snapshots are no longer referenced by this conversation.
      return true;
    } catch (error) {
      const concurrentStateChanged = autoPersistenceDirty;
      // 工作区已切换时不得用旧作用域数据覆盖当前界面；旧存储本身仍保持未删除状态。
      if (activeWorkspaceScopeId.value === scopeId) {
        conversations.value = previousConversations;
        activeConversationId.value = previousActiveConversationId;
        pendingResponseConversationIds.value = previousPendingResponseIds;
        pendingContextAttachments.value = previousPendingContextAttachments;
        const previousActiveConversation = conversations.value.find(
          (conversation) => conversation.id === previousActiveConversationId,
        );
        if (previousActiveConversation) {
          restoreSnapshot(previousActiveConversation, false, false);
        }
        await nextTick();
        const currentDirty = dirtyConversations.get(scopeId);
        const restoredDirty = new Map(previousDirtyConversations);
        for (const [conversationId, kind] of currentDirty ?? []) {
          if (!previousConversationIds.has(conversationId)) continue;
          if (restoredDirty.get(conversationId) !== "details") restoredDirty.set(conversationId, kind);
        }
        if (restoredDirty.size) dirtyConversations.set(scopeId, restoredDirty);
        else dirtyConversations.delete(scopeId);
        if (previousWorkspaceMetaDirty) dirtyWorkspaceMeta.add(scopeId);
        else dirtyWorkspaceMeta.delete(scopeId);
        // 回滚本身无需覆盖原存储，仅补写等待期间真实发生的其他状态变化。
        autoPersistenceDirty = concurrentStateChanged || restoredDirty.size > 0 || previousWorkspaceMetaDirty;
      }
      throw error;
    } finally {
      conversationDeletionInProgress = false;
      resumeAutoPersistence();
    }
  }
  function clearConversationMessages(id: string) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    // The cleared state is complete, so saving it must replace the stored items.
    conversation.detailsLoaded = true;
    conversation.messages = [];
    conversation.agentHistory = [];
    conversation.agentEvents = [];
    markConversationDirty(id);
    conversation.messageQueue = [];
    conversation.status = "not_started";
    conversation.unread = false;
    markConversationDirty(id);
    conversation.updatedAt = Date.now();
    if (id === activeConversationId.value) {
      messages.value = [];
      agentHistory.value = [];
      agentEvents.value = [];
      activeQueue.value = [];
    }
    return true;
  }
  function conversationIsVisible(id: string) {
    return open.value
      && id === activeConversationId.value
      && workspaceScopeForConversation(id) === activeWorkspaceScopeId.value
      && document.visibilityState === "visible"
      && document.hasFocus();
  }
  function setConversationUnread(id: string, unread: boolean) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    if (conversation.unread === unread) return true;
    conversation.unread = unread;
    markConversationDirty(id, "meta");
    return true;
  }

  function markConversationRead(id: string) {
    const conversation = conversationById(id);
    if (!conversation?.unread || !conversationIsVisible(id)) return false;
    return setConversationUnread(id, false);
  }

  function setConversationStatus(id: string, status: AiConversationStatus) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    const previousStatus = conversation.status;
    conversation.status = status;
    if (status === "completed" && previousStatus !== "completed") {
      conversation.unread = !conversationIsVisible(id);
    }
    conversation.updatedAt = Date.now();
    markConversationDirty(id, "meta");
    return true;
  }
  function setActiveConversationStatus(status: AiConversationStatus) {
    return setConversationStatus(activeConversationId.value, status);
  }
  function applyConversationRunningProjection(id: string, running: boolean) {
    const conversation = conversationById(id);
    if (!conversation || conversation.running === running) return Boolean(conversation);
    conversation.running = running;
    markConversationDirty(id, "meta");
    setConversationAwaitingResponse(id, running);
    conversation.updatedAt = Date.now();
    if (!running && hydrated.value) {
      const scopeId = workspaceScopeForConversation(id);
      // Save after the running=false render. flushPersistence clears the pending
      // timer, coalescing the stop transition into one incremental save.
      if (scopeId === activeWorkspaceScopeId.value) {
        void nextTick().then(() => flushPersistenceInBackground());
      } else if (scopeId) {
        flushPersistenceInBackground();
        pruneInactiveWorkspaceConversations();
      }
    }
    return true;
  }

  function syncRunningConversationIds(activeConversationIds: ReadonlySet<string>) {
    for (const conversation of conversations.value) {
      applyConversationRunningProjection(conversation.id, activeConversationIds.has(conversation.id));
    }
    for (const collection of inactiveWorkspaceConversations.values()) {
      for (const conversation of collection.conversations) {
        applyConversationRunningProjection(conversation.id, activeConversationIds.has(conversation.id));
      }
    }
    for (const overview of globalWorkspaceOverviews.value) {
      for (const conversation of overview.collection.conversations) {
        conversation.running = activeConversationIds.has(conversation.id);
      }
    }
  }

  // String keys: progress events that don't change which conversations are running
  // (or which conversations exist) must not walk every conversation again.
  const runningConversationIdsKey = computed(() => {
    const activeConversationIds = new Set<string>();
    for (const run of sessionProgress.activeRuns) {
      if (run.navigation.type === "ai-conversation") {
        activeConversationIds.add(run.navigation.conversationId);
      }
    }
    return [...activeConversationIds].sort().join("\n");
  });
  const conversationIdsKey = computed(() => conversations.value.map((conversation) => conversation.id).join("\n"));
  const globalOverviewConversationIdsKey = computed(() => globalWorkspaceOverviews.value
    .map((overview) => overview.collection.conversations.map((conversation) => conversation.id).join(","))
    .join("\n"));
  watch(
    [runningConversationIdsKey, conversationIdsKey, globalOverviewConversationIdsKey],
    ([runningKey]) => {
      const activeConversationIds = new Set(runningKey ? runningKey.split("\n") : []);
      syncRunningConversationIds(activeConversationIds);
    },
    { immediate: true, flush: "sync" },
  );

  function setConversationPermission(id: string, permission?: AiAssistantPermission) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    conversation.pendingPermission = permission
      ? { ...permission, options: permission.options.map((option) => ({ ...option })) }
      : undefined;
    if (id === activeConversationId.value) pendingPermission.value = conversation.pendingPermission;
    setConversationAwaitingResponse(id, !permission && conversation.running);
    conversation.updatedAt = Date.now();
    markConversationDirty(id, "meta");
    return true;
  }
  function setConversationAgentState(
    id: string,
    state: { acpSessionId?: string; acpAdapterId?: string; agentReady?: boolean },
  ) {
    const conversation = conversationById(id);
    if (!conversation) return false;
    if (state.acpSessionId !== undefined) conversation.acpSessionId = state.acpSessionId;
    if (state.acpAdapterId !== undefined) conversation.acpAdapterId = state.acpAdapterId;
    if (state.agentReady !== undefined) conversation.agentReady = state.agentReady;
    if (id === activeConversationId.value) {
      if (state.acpSessionId !== undefined) acpSessionId.value = state.acpSessionId;
      if (state.acpAdapterId !== undefined) acpAdapterId.value = state.acpAdapterId;
      if (state.agentReady !== undefined) agentReady.value = state.agentReady;
    }
    conversation.updatedAt = Date.now();
    markConversationDirty(id, "meta");
    return true;
  }
  /**
   * The host closed these ACP connections (acp:disconnected): conversations
   * on them, in any workspace held in memory, are no longer ready, so their
   * next send starts the agent again. Returns the ids of the conversations
   * that were ready.
   */
  function markAcpServersDisconnected(serverIds: readonly string[]): string[] {
    const ids = new Set(serverIds);
    if (!ids.size) return [];
    const stopped = new Set<string>();
    const collections = [
      conversations.value,
      ...[...inactiveWorkspaceConversations.values()].map((collection) => collection.conversations),
      ...globalWorkspaceOverviews.value.map((overview) => overview.collection.conversations),
    ];
    for (const collection of collections) {
      for (const conversation of collection) {
        if (!conversation.agentReady || !ids.has(conversation.acpServerId)) continue;
        conversation.agentReady = false;
        stopped.add(conversation.id);
      }
    }
    for (const id of stopped) {
      if (id === activeConversationId.value) agentReady.value = false;
      markConversationDirty(id, "meta");
    }
    return [...stopped];
  }
  function findConversationByAcpSession(serverId: string, sessionId: string) {
    const active = conversations.value.find(
      (conversation) => conversation.acpServerId === serverId && conversation.acpSessionId === sessionId,
    );
    if (active) return active;
    for (const collection of inactiveWorkspaceConversations.values()) {
      const conversation = collection.conversations.find(
        (item) => item.acpServerId === serverId && item.acpSessionId === sessionId,
      );
      if (conversation) return conversation;
    }
    return undefined;
  }


  function restoreConversationCollection(value: unknown) {
    if (!value || typeof value !== "object") return false;
    const collection = value as Partial<AiConversationCollection>;
    if (collection.version !== 2 || !Array.isArray(collection.conversations) || !collection.conversations.length) return false;
    const valid = collection.conversations.filter(
      (conversation): conversation is AiConversation =>
        Boolean(conversation) && typeof conversation.id === "string" && restoreableSnapshot(conversation),
    );
    if (!valid.length) return false;
    conversations.value = valid.map((conversation) => {
      const restoredStatus = conversation.status === "in_progress" || conversation.status === "needs_input"
        ? "interrupted"
        : conversation.status;
      const restoredMessages = conversation.messages.map((message) => ({
        ...message,
        id: typeof message.id === "string" && message.id ? message.id : createId("message"),
        createdAt: typeof message.createdAt === "number" && Number.isFinite(message.createdAt)
          ? message.createdAt
          : undefined,
        presentation: messagePresentation(message.presentation),
        tokenUsage: normalizeAiTokenUsage(message.tokenUsage),
        attachments: Array.isArray(message.attachments)
          ? message.attachments.map(compactAiAttachment)
          : undefined,
      }));
      const eventStatus = restoredStatus === "completed"
        ? "completed"
        : restoredStatus === "failed"
          ? "failed"
          : "cancelled";
      return applyConversationOwnership({
        ...conversation,
        mode: normalizeMode(conversation.mode as PersistedAiAssistantMode),
        pinned: Boolean(conversation.pinned),
        messages: restoredMessages,
        running: false,
        status: restoredStatus,
        agentHistory: persistedAgentHistory(conversation.agentHistory ?? []),
        agentEvents: settledAgentEvents(conversation.agentEvents.map(safeAgentEvent), eventStatus),
        pendingPermission: undefined,
        agentReady: false,
      }, activeWorkspaceScopeId.value);
    });
    activeConversationId.value = valid.some((item) => item.id === collection.activeConversationId)
      ? collection.activeConversationId!
      : valid[0].id;
    return restoreSnapshot(activeConversation.value, false, false);
  }
  function restoreableSnapshot(value: unknown): value is AiAssistantSnapshot {
    if (!value || typeof value !== "object") return false;
    const candidate = value as Partial<AiAssistantSnapshot>;
    return candidate.version === 1 && isPersistedMode(candidate.mode) && Array.isArray(candidate.messages);
  }
  function restoreSnapshot(value: unknown, restoreActiveAgent = true, sanitizePersistedHistory = true) {
    if (!restoreableSnapshot(value)) return false;
    const snapshotValue = value;
    mode.value = normalizeMode(snapshotValue.mode as PersistedAiAssistantMode);
    draft.value = typeof snapshotValue.draft === "string" ? snapshotValue.draft : "";
    draftAttachments.value = Array.isArray(snapshotValue.draftAttachments)
      ? sanitizeAiAttachments(snapshotValue.draftAttachments).map(compactAiAttachment)
      : [];
    selectedModel.value = typeof snapshotValue.selectedModel === "string" ? snapshotValue.selectedModel.trim() : "";
    selectedReasoningEffort.value = typeof snapshotValue.selectedReasoningEffort === "string"
      ? snapshotValue.selectedReasoningEffort.trim().toLowerCase()
      : "";
    messages.value = snapshotValue.messages
      .filter((message): message is AiAssistantMessage => Boolean(message) && typeof message.role === "string" && typeof message.content === "string")
      .map((message) => ({
        ...message,
        id: typeof message.id === "string" && message.id ? message.id : createId("message"),
        createdAt: typeof message.createdAt === "number" && Number.isFinite(message.createdAt)
          ? message.createdAt
          : undefined,
        attachments: Array.isArray(message.attachments)
          ? message.attachments.map(compactAiAttachment)
          : undefined,
        presentation: messagePresentation(message.presentation),
        tokenUsage: normalizeAiTokenUsage(message.tokenUsage),
      }));
    const restoredAgentHistory = Array.isArray(snapshotValue.agentHistory)
      ? snapshotValue.agentHistory.filter(
          (message): message is AiLoopMessage => Boolean(message) && typeof message.role === "string" && typeof message.content === "string",
        )
      : [];
    agentHistory.value = sanitizePersistedHistory
      ? persistedAgentHistory(restoredAgentHistory)
      : cloneAgentHistory(restoredAgentHistory);
    agentEvents.value = Array.isArray(snapshotValue.agentEvents)
      ? snapshotValue.agentEvents.filter((event): event is AiAssistantEvent => Boolean(event) && typeof event.id === "string" && typeof event.title === "string" && typeof event.detail === "string")
        .map(safeAgentEvent)
      : [];
    pendingPermission.value = snapshotValue.pendingPermission && typeof snapshotValue.pendingPermission.requestId === "string"
      ? { ...snapshotValue.pendingPermission, options: [...(snapshotValue.pendingPermission.options ?? [])] }
      : undefined;
    acpServerId.value = typeof snapshotValue.acpServerId === "string" && snapshotValue.acpServerId.trim()
      ? snapshotValue.acpServerId
      : createId("vue-ai");
    acpSessionId.value = typeof snapshotValue.acpSessionId === "string" ? snapshotValue.acpSessionId : "";
    acpAdapterId.value = typeof snapshotValue.acpAdapterId === "string" ? snapshotValue.acpAdapterId.trim() : "";
    agentReady.value = Boolean(restoreActiveAgent && snapshotValue.agentReady && acpSessionId.value);
    activeQueue.value = Array.isArray(snapshotValue.messageQueue)
      ? snapshotValue.messageQueue
          .filter((message: unknown): message is AiQueuedMessage => Boolean(message) && typeof (message as AiQueuedMessage).id === "string" && typeof (message as AiQueuedMessage).content === "string")
          .map(cloneQueuedMessage)
      : [];
    return true;
  }


  let persistTimer: ReturnType<typeof setTimeout> | undefined;
  const runLatestPersistence = createLatestTaskRunner();
  let workspaceSwitchSequence = 0;
  let conversationHistoryRevision = 0;
  type WorkspaceConversationState = { storedConversations: unknown };
  const workspaceConversationLoads = new Map<string, Promise<WorkspaceConversationState>>();
  let workspaceConversationPreloadSequence = 0;
  let prefetchedWorkspaceConversation: {
    scopeId: string;
    state: WorkspaceConversationState;
  } | undefined;
  let autoPersistencePauseDepth = 0;
  let autoPersistenceDirty = false;
  let conversationDeletionInProgress = false;

  function pauseAutoPersistence() {
    autoPersistencePauseDepth += 1;
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = undefined;
    }
  }

  function schedulePersistence() {
    const hasRunningConversation = conversations.value.some((conversation) => conversation.running);
    // Streaming marks the conversation dirty every frame; keep the pending timer.
    if (persistTimer && hasRunningConversation) return;
    const hasDetailChanges = [...dirtyConversations.values()].some((scope) =>
      [...scope.values()].some((kind) => kind === "details")
    );
    const delay = hasRunningConversation ? 2_000 : hasDetailChanges ? 250 : 600;
    // 流式内容最多每 2 秒增量保存；普通详情与轻量元数据分别使用短/长防抖。
    if (persistTimer) {
      if (hasRunningConversation) return;
      clearTimeout(persistTimer);
    }
    persistTimer = setTimeout(() => {
      persistTimer = undefined;
      flushPersistenceInBackground();
    }, delay);
  }

  let persistenceRetryDelay = 0;
  let persistenceRetryTimer: ReturnType<typeof setTimeout> | undefined;
  /**
   * Fire-and-forget save. Failed writes stay dirty and are retried with backoff,
   * so a transient disk error neither loses changes nor surfaces as an unhandled rejection.
   */
  function flushPersistenceInBackground() {
    void flushPersistence().then(
      () => {
        persistenceRetryDelay = 0;
      },
      (error: unknown) => {
        // eslint-disable-next-line no-console -- Background save failures are retried silently; keep them diagnosable.
        console.warn("AI conversation persistence failed", error);
        persistenceRetryDelay = Math.min(30_000, persistenceRetryDelay ? persistenceRetryDelay * 2 : 1_000);
        if (persistenceRetryTimer) clearTimeout(persistenceRetryTimer);
        persistenceRetryTimer = setTimeout(() => {
          persistenceRetryTimer = undefined;
          flushPersistenceInBackground();
        }, persistenceRetryDelay);
      },
    );
  }

  function resumeAutoPersistence() {
    autoPersistencePauseDepth = Math.max(0, autoPersistencePauseDepth - 1);
    if (autoPersistencePauseDepth > 0 || !autoPersistenceDirty) return;
    autoPersistenceDirty = false;
    schedulePersistence();
  }

  function workspaceCollection(scopeId: string): AiConversationCollection | undefined {
    if (scopeId === activeWorkspaceScopeId.value) {
      return { version: 2, activeConversationId: activeConversationId.value, conversations: conversations.value };
    }
    return inactiveWorkspaceConversations.get(scopeId)
      ?? globalWorkspaceOverviews.value.find((item) => item.scopeId === scopeId)?.collection;
  }

  function persistedConversation(
    conversation: AiConversation,
    active = false,
    includeDetails = true,
  ): AiConversation {
    if (!includeDetails) {
      const { completionUnread: _legacyCompletionUnread, ...persistableConversation } = conversation as LegacyAiConversation;
      const metadata = {
        ...persistableConversation,
        ...(active
          ? {
              mode: mode.value,
              draft: draft.value,
              draftAttachments: draftAttachments.value.map(compactAiAttachment),
              selectedModel: selectedModel.value,
              selectedReasoningEffort: selectedReasoningEffort.value,
              acpServerId: acpServerId.value,
              acpSessionId: String(acpSessionId.value),
              ...(acpAdapterId.value ? { acpAdapterId: acpAdapterId.value } : {}),
            }
          : {}),
        messages: [],
        agentHistory: [],
        agentEvents: [],
        messageQueue: [],
        pendingPermission: undefined,
        agentReady: false,
      };
      return metadata;
    }
    const snapshotValue = active ? runtimeConversationSnapshot() : conversation;
    const { completionUnread: _legacyCompletionUnread, ...persistableConversation } = conversation as LegacyAiConversation;
    return {
      ...persistableConversation,
      ...(active ? snapshotValue : {}),
      detailsLoaded: conversation.detailsLoaded,
      title: conversation.title || conversationTitle(snapshotValue),
      agentHistory: persistedAgentHistory(snapshotValue.agentHistory ?? []),
      agentEvents: snapshotValue.agentEvents.map(safeAgentEvent),
      pendingPermission: undefined,
      agentReady: false,
    };
  }

  async function flushPersistence() {
    if (!hydrated.value) return;
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = undefined;
    }
    await runLatestPersistence(async () => {
      const scopeIds = new Set([...dirtyWorkspaceMeta, ...dirtyConversations.keys()]);
      for (const scopeId of scopeIds) {
        const collection = workspaceCollection(scopeId);
        if (!collection) continue;
        const pendingLoad = workspaceConversationLoads.get(scopeId);
        if (pendingLoad) {
          // 工作区仍在读取：保留 dirty 标记，读取结束后再保存，避免改动一直滞留。
          void pendingLoad.then(schedulePersistence, schedulePersistence);
          continue;
        }
        const metaDirty = dirtyWorkspaceMeta.delete(scopeId);
        const metaSnapshot = metaDirty
          ? {
              activeConversationId: collection.activeConversationId,
              conversationIds: collection.conversations.map((conversation) => conversation.id),
            }
          : undefined;
        const scopeDirty = dirtyConversations.get(scopeId);
        dirtyConversations.delete(scopeId);
        try {
          for (const [conversationId, kind] of scopeDirty ?? []) {
            const position = collection.conversations.findIndex((conversation) => conversation.id === conversationId);
            if (position < 0) continue;
            const conversation = collection.conversations[position];
            const active = scopeId === activeWorkspaceScopeId.value && conversationId === activeConversationId.value;
            const replaceItems = kind === "details" && conversation.detailsLoaded !== false;
            await aiConversationPersistence.saveDetail(
              scopeId,
              persistedConversation(conversation, active, replaceItems),
              position,
              replaceItems,
            );
          }
          if (metaSnapshot) {
            await aiConversationPersistence.saveMeta(
              scopeId,
              metaSnapshot.activeConversationId,
              metaSnapshot.conversationIds,
            );
          }
          pruneInactiveWorkspaceConversations();
        } catch (error) {
          if (metaDirty) dirtyWorkspaceMeta.add(scopeId);
          if (scopeDirty?.size) {
            const retry = dirtyConversations.get(scopeId) ?? new Map<string, DirtyConversationKind>();
            for (const [conversationId, kind] of scopeDirty) {
              if (retry.get(conversationId) !== "details") retry.set(conversationId, kind);
            }
            dirtyConversations.set(scopeId, retry);
          }
          throw error;
        }
      }
    });
  }

  async function clearConversationHistory() {
    if (
      conversations.value.some((conversation) => conversation.running)
      || [...inactiveWorkspaceConversations.values()].some((collection) =>
        collection.conversations.some((conversation) => conversation.running)
      )
    ) {
      throw new Error("ai_conversation_running");
    }
    pauseAutoPersistence();
    try {
      // 让清空前发起的工作区读取结果失效，避免旧缓存或预取结果在删除后回灌。
      conversationHistoryRevision += 1;
      workspaceSwitchSequence += 1;
      conversationSwitchSequence += 1;
      workspaceConversationPreloadSequence += 1;
      prefetchedWorkspaceConversation = undefined;
      workspaceConversationLoads.clear();
      inactiveWorkspaceConversations.clear();
      dirtyConversations.clear();
      dirtyWorkspaceMeta.clear();
      conversationDetailLoads.clear();
      workspaceSwitching.value = false;
      await clearAllAiConversationHistory();
      resetWorkspaceConversationState();
      await nextTick();
      autoPersistenceDirty = false;
    } finally {
      resumeAutoPersistence();
    }
  }

  function loadWorkspaceConversationState(scopeId: string) {
    if (prefetchedWorkspaceConversation?.scopeId === scopeId) {
      const state = prefetchedWorkspaceConversation.state;
      prefetchedWorkspaceConversation = undefined;
      return Promise.resolve(state);
    }
    const existing = workspaceConversationLoads.get(scopeId);
    if (existing) return existing;

    const load = aiConversationPersistence.load(scopeId).then(
      (storedConversations) => ({ storedConversations }),
    );
    workspaceConversationLoads.set(scopeId, load);
    void load.then(
      () => {
        if (workspaceConversationLoads.get(scopeId) === load) {
          workspaceConversationLoads.delete(scopeId);
        }
      },
      () => {
        if (workspaceConversationLoads.get(scopeId) === load) {
          workspaceConversationLoads.delete(scopeId);
        }
      },
    );
    return load;
  }

  function preloadWorkspaceScope(scopeId: string) {
    const targetScopeId = normalizeWorkspaceScopeId(scopeId);
    if (
      !hydrated.value
      || targetScopeId === activeWorkspaceScopeId.value
      || inactiveWorkspaceConversations.has(targetScopeId)
    ) return Promise.resolve();
    // 只保留最近一次预取结果；点击时直接消费，避免为多个工作区长期占用内存。
    if (prefetchedWorkspaceConversation?.scopeId !== targetScopeId) {
      prefetchedWorkspaceConversation = undefined;
    }
    const preloadSequence = ++workspaceConversationPreloadSequence;
    return loadWorkspaceConversationState(targetScopeId).then(
      (state) => {
        if (
          preloadSequence === workspaceConversationPreloadSequence
          && targetScopeId !== activeWorkspaceScopeId.value
          && !inactiveWorkspaceConversations.has(targetScopeId)
        ) {
          prefetchedWorkspaceConversation = { scopeId: targetScopeId, state };
        }
      },
      () => undefined,
    );
  }

  async function activateTopWorkspaceConversation() {
    if (activeConversation.value?.workspaceId === topWorkspaceId.value) return;
    const target = conversations.value.find(
      (conversation) => conversation.workspaceId === topWorkspaceId.value,
    );
    if (target) await switchConversation(target.id);
    else createConversation(mode.value);
  }

  watch(
    () => conversationPreferences.scopeMode,
    (scopeMode) => {
      if (!hydrated.value) return;
      if (scopeMode === "global") {
        void conversationPreferences.setActiveScopeId(activeWorkspaceScopeId.value);
        void refreshGlobalConversationOverviews();
      } else {
        void switchWorkspaceScope(topWorkspaceScopeId.value).then(activateTopWorkspaceConversation);
      }
    },
  );

  async function followTopWorkspace(context: AiConversationWorkspaceContext) {
    const normalized = rememberTopWorkspace(context);
    if (!hydrated.value) {
      await hydrate(normalized.scopeId, normalized);
      return;
    }
    if (conversationPreferences.globalMode) {
      void refreshGlobalConversationOverviews();
      return;
    }
    await switchWorkspaceScope(normalized.scopeId);
    await activateTopWorkspaceConversation();
  }

  async function switchToConversation(id: string, scopeId?: string) {
    const targetScopeId = scopeId?.trim()
      || allConversations.value.find((conversation) => conversation.id === id)?.workspaceScopeId?.trim();
    if (!targetScopeId) return false;
    if (targetScopeId !== activeWorkspaceScopeId.value) await switchWorkspaceScope(targetScopeId);
    return switchConversation(id);
  }

  async function createConversationForTopWorkspace(nextMode: AiAssistantMode = mode.value) {
    if (activeWorkspaceScopeId.value !== topWorkspaceScopeId.value) {
      await switchWorkspaceScope(topWorkspaceScopeId.value);
    }
    return createConversation(nextMode);
  }

  async function switchWorkspaceScope(scopeId: string) {
    const targetScopeId = normalizeWorkspaceScopeId(scopeId);
    if (!hydrated.value) {
      await hydrate(targetScopeId);
      return;
    }
    if (targetScopeId === activeWorkspaceScopeId.value) {
      if (conversationPreferences.globalMode) void conversationPreferences.setActiveScopeId(targetScopeId);
      return;
    }
    if (conversationPreferences.globalMode) void conversationPreferences.setActiveScopeId(targetScopeId);

    const requestId = ++workspaceSwitchSequence;
    conversationSwitchSequence += 1;
    const historyRevision = conversationHistoryRevision;
    const previousScopeId = activeWorkspaceScopeId.value;
    const previousWorkspaceLoading = workspaceConversationLoads.has(previousScopeId);
    let previousCollection: AiConversationCollection | undefined;
    if (!previousWorkspaceLoading) {
      syncActiveConversation();
      previousCollection = {
        version: 2,
        activeConversationId: activeConversationId.value,
        conversations: conversations.value,
      };
      cacheInactiveWorkspaceConversation(previousScopeId, previousCollection);
    }

    const cachedTarget = inactiveWorkspaceConversations.get(targetScopeId);
    const targetLoad = cachedTarget
      ? undefined
      : loadWorkspaceConversationState(targetScopeId);

    // 先切换作用域并隔离界面，避免读取磁盘期间继续展示上一个工作区的会话。
    // 目标概览读取优先；旧工作区 dirty 会话在切换后后台增量保存。
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = undefined;
    }
    activeWorkspaceScopeId.value = targetScopeId;
    flushPersistenceInBackground();
    if (cachedTarget) {
      workspaceSwitching.value = false;
      inactiveWorkspaceConversations.delete(targetScopeId);
      conversations.value = cachedTarget.conversations;
      activeConversationId.value = cachedTarget.conversations.some(
        (conversation) => conversation.id === cachedTarget.activeConversationId,
      )
        ? cachedTarget.activeConversationId
        : cachedTarget.conversations[0]?.id ?? "";
      if (activeConversation.value) restoreSnapshot(activeConversation.value, true, false);
      pendingResponseConversationIds.value = new Set(
        conversations.value.filter((conversation) => conversation.running).map((conversation) => conversation.id),
      );
      return;
    }

    workspaceSwitching.value = true;
    resetWorkspaceConversationState();
    let storedConversations: unknown;
    try {
      ({ storedConversations } = await targetLoad!);
    } catch (error) {
      if (requestId === workspaceSwitchSequence) workspaceSwitching.value = false;
      if (historyRevision !== conversationHistoryRevision) return;
      throw error;
    }
    if (
      requestId !== workspaceSwitchSequence
      || historyRevision !== conversationHistoryRevision
    ) {
      // 当前界面已切换或历史已清空；旧请求不能覆盖状态或重新写回已删除数据。
      return;
    }

    const restoredWorkspace = restoreConversationCollection(storedConversations);
    if (restoredWorkspace && activeConversation.value?.detailsLoaded === false) {
      await switchConversation(activeConversationId.value);
    }
    workspaceSwitching.value = false;
    if (!restoredWorkspace) {
      markConversationDirty(activeConversationId.value);
      markWorkspaceMetaDirty(targetScopeId);
    }
  }


  return {
    open, fullscreen, mode, layoutMode, placement, selectedModel, selectedReasoningEffort, width, conversationSidebarWidth, hydrated, workspaceSwitching, activeWorkspaceScopeId, topWorkspaceId, topWorkspaceName, topWorkspaceScopeId, topWorkspaceProjectRoot, globalOverviewsLoading, messages, agentHistory, agentEvents, activeQueue, pendingContextAttachments, pendingPermission, loading, awaitingResponse, draft, draftAttachments,
    acpServerId, acpSessionId, agentReady, agentStarting, conversations, allConversations, activeConversationId, activeConversation,
    hydrate, setWorkspaceDirectory, followTopWorkspace, preloadWorkspaceScope, switchWorkspaceScope, switchToConversation, createConversationForTopWorkspace, refreshGlobalConversationOverviews, setDraft, setDraftAttachments, setSelectedModel, setSelectedReasoningEffort, show, addContextAttachment, takeContextAttachments, toggle, close, setPlacement, setLayoutMode, setWidth, persistWidth, setConversationSidebarWidth, persistConversationSidebarWidth,
    addMessage, addMessageToConversation, removeMessageFromConversation, setMessagePresentation, setMessageTokenUsage, setConversationAgentHistory, updateLastAssistant, updateLastAssistantForConversation,
    appendAssistantMessageForConversation, upsertAgentEvent, upsertAgentEventForConversation, removeAgentEventForConversation, clearAgentEvents, anchorUnassignedAgentEvents, settleRunningAgentEvents,
    enqueueMessage, prioritizeQueuedMessage, dequeueMessage, removeQueuedMessage, updateQueuedMessage, queueLength,
    createConversation, createDelegatedConversation, recordDelegatedConversationResult, delegatedConversationReports, conversationFamily, setConversationWorkspace, moveConversationToWorkspace, switchConversation, renameConversation, setConversationArchived, setConversationPinned, deleteConversation, clearConversationMessages, clearConversationHistory,
    setActiveConversationStatus, setConversationStatus, setConversationUnread, markConversationRead, setConversationPermission,
    setConversationAgentState, markAcpServersDisconnected, conversationById, ensureConversationDetails, rewindConversationTo, findConversationByAcpSession, snapshot, persistedSnapshot, restoreSnapshot, flushPersistence,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiAssistantStore, import.meta.hot));
}
