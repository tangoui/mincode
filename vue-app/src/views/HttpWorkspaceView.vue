<script setup lang="ts">
import { handoffHttpResponse } from "@/services/http-ai-handoff";
import { isSensitiveDataName } from "@/services/ai-tools/shared";
import "@/styles/workspaces/http/workspace.css";
import {
  computed,
  defineAsyncComponent,
  nextTick,
  onActivated,
  onBeforeUnmount,
  onDeactivated,
  onMounted,
  ref,
  shallowRef,
  toRaw,
  watch,
} from "vue";
import { storeToRefs } from "pinia";
import { ElMessage, ElMessageBox, type ScrollbarInstance } from "element-plus";
import {
  ArrowDown,
  CircleClose,
  Close,
  CopyDocument,
  Delete,
  FolderOpened,
  MagicStick,
  MoreFilled,
  Operation,
  Plus,
  Promotion,
  Setting,
  Upload,
} from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { canInvoke, invokeBackend } from "@/services/desktop-bridge";
import { formatJsonOffThread } from "@/services/json-format";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { registerUiSessionFlusher } from "@/services/ui-session";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import {
  filterHttpHistory,
  groupHttpHistory,
  historyDisplayName,
  historyStatusTone,
  historyUrlSummary,
  type HttpHistoryEntry,
} from "@/domain/http/history";
import { loadHttpState, saveHttpState } from "@/services/http-state-persistence";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { registerAiWorkspaceActionHandler } from "@/services/ai-workspace-actions";
import HttpBodyEditor from "@/components/http/HttpBodyEditor.vue";
import HttpCodeGeneratorDialog from "@/components/http/HttpCodeGeneratorDialog.vue";
import HttpCollectionTreeNode from "@/components/http/HttpCollectionTreeNode.vue";
import HttpCollectionRunnerDialog from "@/components/http/HttpCollectionRunnerDialog.vue";
import HttpCurlImportDialog from "@/components/http/HttpCurlImportDialog.vue";
import HttpMockServerPanel from "@/components/http/HttpMockServerPanel.vue";
import HttpStreamPanel from "@/components/http/HttpStreamPanel.vue";
import HttpSocketIOPanel from "@/components/http/HttpSocketIOPanel.vue";
import { serializeCurlCommand, type ParsedCurlRequest } from "@/domain/http/curl";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { contextMenuAnchor } from "@/components/ui/app-context-menu";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { buildNativeHttpRequestPayload } from "@/domain/http/native-request";
import {
  formatJsonResponseBody,
  formatResponseSizeLabel,
  isLargeResponseBody,
  looksLikeJsonBody,
} from "@/domain/http/response-body";
import {
  HTTP_HISTORY_MAX_ITEMS,
  normalizeHttpHistoryForPersistence,
} from "@/domain/http/persistence-budget";
import {
  COMMON_HTTP_HEADER_NAMES,
  createSavedHttpRequest,
  filterHttpCollections,
  findHttpCollection,
  findTopLevelHttpCollection,
  flattenCollectionRequests,
  moveHttpCollection,
  moveSavedHttpRequest,
  resolveHttpRequestEnvironment,
  uniqueCollectionName,
  type HttpAuthType,
  type HttpBodyField,
  type HttpBodyFieldType,
  type HttpBodyType,
  type HttpOAuth2GrantType,
  type HttpParameterType,
  type HttpRequestCollection,
  type SavedHttpRequest,
} from "@/domain/http/request-collection";
import {
  importApiDocument,
  importedApiCollection,
  mergeImportedCollection,
  mergeImportedEnvironments,
  serializeApiDocument,
  type ApiDocumentFormat,
} from "@/domain/http/api-document";
import { chooseApiDocumentFile, saveApiDocumentFile } from "@/services/http-api-document-service";
import { chooseHttpRequestUploadFile } from "@/services/http-request-file-service";
import type { HttpEnvironment } from "@/domain/http/resources";
import { useHttpResourcesStore } from "@/stores/http-resources";
// 响应体可能有几 MB、几十万行：只把可见行放进 DOM，见 VirtualTextViewer。
import VirtualTextViewer from "@/components/ui/VirtualTextViewer.vue";

// Markdown responses are rendered on demand; loading the renderer lazily keeps it out of this view's startup chunk.
const MarkdownPreview = defineAsyncComponent(() => import("@/components/ui/MarkdownPreview.vue"));

defineOptions({ name: "HttpWorkspaceView" });

interface KeyValueRow {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
  valueType?: HttpParameterType;
  description?: string;
  fileName?: string;
}

interface ResponseData {
  status: number;
  status_text: string;
  headers: [string, string][];
  body: string;
  elapsed_ms: number;
  size_bytes: number;
  /** 仅浏览器本地模式：与桌面端行为不同之处的说明（已翻译），如被忽略的请求头。 */
  browser_notes?: string[];
}

interface RequestTab {
  id: string;
  name: string;
  method: string;
  url: string;
  params: KeyValueRow[];
  headers: KeyValueRow[];
  body: string;
  bodyType: HttpBodyType;
  formBody: HttpBodyField[];
  binaryFilePath: string;
  binaryFileName: string;
  authType: HttpAuthType;
  authToken: string;
  authUsername: string;
  authPassword: string;
  apiKeyName: string;
  apiKeyValue: string;
  apiKeyAddTo: "header" | "query";
  oauth2GrantType: HttpOAuth2GrantType;
  oauth2AuthUrl: string;
  oauth2TokenUrl: string;
  oauth2ClientId: string;
  oauth2ClientSecret: string;
  oauth2Scope: string;
  oauth2CallbackUrl: string;
  oauth2Username: string;
  oauth2Password: string;
  oauth2AccessToken: string;
  graphqlQuery: string;
  graphqlVariables: string;
  documentation: string;
  documentMode: "edit" | "preview";
  response?: ResponseData;
  loading: boolean;
  requestTab: "params" | "headers" | "auth" | "body" | "documentation";
  responseTab: "body" | "headers";
  savedRequestId?: string;
  savedCollectionId?: string;
  environmentMode: "follow" | "fixed";
  environmentId?: string;
}

type HistoryItem = HttpHistoryEntry;

type StoredRequestTab = Omit<RequestTab, "response" | "loading">;

interface StoredHttpState {
  version: 1 | 2 | 3 | 4 | 5 | 6;
  activeEnvId?: string;
  history: HistoryItem[];
  tabs?: StoredRequestTab[];
  activeTabId?: string;
}

const SIDEBAR_WIDTH_KEY = "app_http_sidebar_width";
const SIDEBAR_COLLAPSED_KEY = "app_http_sidebar_collapsed";
const REQUEST_HEIGHT_KEY = "app_http_request_height";
const { t, te } = useVueI18n();
const linkedNotes = useLinkedNotesDrawer();
const workspaceStateScope = useWorkspaceStateScope();
const workspaceScopeId = workspaceStateScope.capture().scopeId;
const MAX_PERSISTED_REQUEST_TABS = 50;
const MAX_PERSISTED_REQUEST_ROWS = 500;
let sequence = 0;

function id(prefix: string) {
  sequence += 1;
  return `${prefix}-${Date.now()}-${sequence}`;
}

function emptyRow(): KeyValueRow {
  return {
    id: id("kv"),
    key: "",
    value: "",
    enabled: true,
    valueType: "string",
    description: "",
  };
}

function emptyBodyField(): HttpBodyField {
  return { ...emptyRow(), fieldType: "text" };
}

function updateParameterType(row: KeyValueRow, value: HttpParameterType) {
  const switchesFileMode = (row.valueType === "file") !== (value === "file");
  row.valueType = value;
  if (switchesFileMode) {
    row.value = "";
    row.fileName = undefined;
  }
}

async function chooseParameterFile(row: KeyValueRow) {
  if (!canInvoke("http_send_request")) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  const selected = await chooseHttpRequestUploadFile(t("http.chooseUploadFile"));
  if (!selected) return;
  row.value = selected.path;
  row.fileName = selected.name;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function restoreRows(value: unknown) {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item): KeyValueRow[] => {
    if (!isRecord(item) || typeof item.key !== "string" || typeof item.value !== "string")
      return [];
    return [
      {
        id: typeof item.id === "string" && item.id ? item.id : id("kv"),
        key: item.key,
        value: item.value,
        enabled: item.enabled !== false,
        valueType:
          typeof item.valueType === "string" &&
          ["string", "integer", "number", "boolean", "array", "object", "file"].includes(
            item.valueType
          )
            ? (item.valueType as HttpParameterType)
            : "string",
        description: typeof item.description === "string" ? item.description : "",
        fileName: typeof item.fileName === "string" ? item.fileName : undefined,
      },
    ];
  });
}

function restoreHistory(value: unknown) {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item): HistoryItem[] => {
    if (
      !isRecord(item) ||
      typeof item.method !== "string" ||
      typeof item.url !== "string" ||
      typeof item.status !== "number" ||
      typeof item.elapsed !== "number" ||
      typeof item.createdAt !== "number"
    ) {
      return [];
    }
    return [
      {
        id: typeof item.id === "string" && item.id ? item.id : id("history"),
        method: item.method,
        url: item.url,
        ...(typeof item.name === "string" && item.name.trim() ? { name: item.name.slice(0, 200) } : {}),
        status: item.status,
        elapsed: item.elapsed,
        createdAt: item.createdAt,
      },
    ];
  });
}

function createTab(): RequestTab {
  return {
    id: id("request"),
    name: t("http.newRequest"),
    method: "GET",
    url: "",
    params: [emptyRow()],
    headers: [emptyRow()],
    body: "",
    bodyType: "none",
    formBody: [emptyBodyField()],
    binaryFilePath: "",
    binaryFileName: "",
    authType: "none",
    authToken: "",
    authUsername: "",
    authPassword: "",
    apiKeyName: "X-API-Key",
    apiKeyValue: "",
    apiKeyAddTo: "header",
    oauth2GrantType: "authorization-code",
    oauth2AuthUrl: "",
    oauth2TokenUrl: "",
    oauth2ClientId: "",
    oauth2ClientSecret: "",
    oauth2Scope: "",
    oauth2CallbackUrl: "",
    oauth2Username: "",
    oauth2Password: "",
    oauth2AccessToken: "",
    graphqlQuery: "",
    graphqlVariables: "",
    documentation: "",
    documentMode: "edit",
    loading: false,
    requestTab: "params",
    responseTab: "body",
    environmentMode: "follow",
  };
}

function boundedString(value: unknown, fallback = "", maxLength = 2_000_000) {
  return typeof value === "string" ? value.slice(0, maxLength) : fallback;
}

function restoreBodyFields(value: unknown): HttpBodyField[] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_PERSISTED_REQUEST_ROWS).flatMap((item): HttpBodyField[] => {
    if (!isRecord(item)) return [];
    const row = restoreRows([item])[0];
    if (!row) return [];
    return [
      {
        ...row,
        fieldType:
          typeof item.fieldType === "string" &&
          ["text", "integer", "number", "boolean", "array", "object", "file"].includes(
            item.fieldType
          )
            ? (item.fieldType as HttpBodyFieldType)
            : "text",
        fileName: boundedString(item.fileName, "", 1024),
        contentType: boundedString(item.contentType, "", 256),
      },
    ];
  });
}

function restoreRequestTabs(value: unknown): RequestTab[] {
  if (!Array.isArray(value)) return [];
  const restored: RequestTab[] = [];
  const ids = new Set<string>();
  for (const item of value.slice(0, MAX_PERSISTED_REQUEST_TABS)) {
    if (!isRecord(item)) continue;
    const restoredId = boundedString(item.id, "", 512).trim();
    if (!restoredId || ids.has(restoredId)) continue;
    ids.add(restoredId);
    const tab = createTab();
    Object.assign(tab, {
      id: restoredId,
      name: boundedString(item.name, t("http.newRequest"), 256) || t("http.newRequest"),
      method: boundedString(item.method, "GET", 32) || "GET",
      url: boundedString(item.url, "", 32_768),
      params: restoreRows(item.params).slice(0, MAX_PERSISTED_REQUEST_ROWS),
      headers: restoreRows(item.headers).slice(0, MAX_PERSISTED_REQUEST_ROWS),
      body: boundedString(item.body),
      bodyType: [
        "none",
        "json",
        "xml",
        "text",
        "html",
        "javascript",
        "form-data",
        "x-www-form-urlencoded",
        "graphql",
        "binary",
        "raw",
      ].includes(String(item.bodyType))
        ? (item.bodyType as HttpBodyType)
        : "none",
      formBody: restoreBodyFields(item.formBody),
      binaryFilePath: boundedString(item.binaryFilePath, "", 32_768),
      binaryFileName: boundedString(item.binaryFileName, "", 1024),
      authType: ["none", "bearer", "basic", "api-key", "oauth2"].includes(
        String(item.authType)
      )
        ? (item.authType as HttpAuthType)
        : "none",
      authToken: boundedString(item.authToken),
      authUsername: boundedString(item.authUsername, "", 32_768),
      authPassword: boundedString(item.authPassword),
      apiKeyName: boundedString(item.apiKeyName, "X-API-Key", 1024),
      apiKeyValue: boundedString(item.apiKeyValue),
      apiKeyAddTo: item.apiKeyAddTo === "query" ? "query" : "header",
      oauth2GrantType: [
        "authorization-code",
        "client-credentials",
        "password",
        "implicit",
      ].includes(String(item.oauth2GrantType))
        ? (item.oauth2GrantType as HttpOAuth2GrantType)
        : "authorization-code",
      oauth2AuthUrl: boundedString(item.oauth2AuthUrl, "", 32_768),
      oauth2TokenUrl: boundedString(item.oauth2TokenUrl, "", 32_768),
      oauth2ClientId: boundedString(item.oauth2ClientId, "", 32_768),
      oauth2ClientSecret: boundedString(item.oauth2ClientSecret),
      oauth2Scope: boundedString(item.oauth2Scope, "", 32_768),
      oauth2CallbackUrl: boundedString(item.oauth2CallbackUrl, "", 32_768),
      oauth2Username: boundedString(item.oauth2Username, "", 32_768),
      oauth2Password: boundedString(item.oauth2Password),
      oauth2AccessToken: boundedString(item.oauth2AccessToken),
      graphqlQuery: boundedString(item.graphqlQuery),
      graphqlVariables: boundedString(item.graphqlVariables),
      documentation: boundedString(item.documentation),
      documentMode: item.documentMode === "preview" ? "preview" : "edit",
      loading: false,
      requestTab: ["params", "headers", "auth", "body", "documentation"].includes(
        String(item.requestTab)
      )
        ? (item.requestTab as RequestTab["requestTab"])
        : "params",
      responseTab: item.responseTab === "headers" ? "headers" : "body",
      savedRequestId: boundedString(item.savedRequestId, "", 512) || undefined,
      savedCollectionId: boundedString(item.savedCollectionId, "", 512) || undefined,
      environmentMode:
        item.environmentMode === "fixed" ||
        (item.environmentMode === undefined && boundedString(item.environmentId, "", 512))
          ? "fixed"
          : "follow",
      environmentId: boundedString(item.environmentId, "", 512) || undefined,
    } satisfies Partial<RequestTab>);
    if (!tab.params.length) tab.params = [emptyRow()];
    if (!tab.headers.length) tab.headers = [emptyRow()];
    if (!tab.formBody.length) tab.formBody = [emptyBodyField()];
    restored.push(tab);
  }
  return restored;
}

// Reads only persisted keys so watchers do not track response/loading churn.
function storedRequestTab(tab: RequestTab): StoredRequestTab {
  const snapshot: Record<string, unknown> = {};
  for (const key of Object.keys(tab) as (keyof RequestTab)[]) {
    if (key !== "response" && key !== "loading") snapshot[key] = tab[key];
  }
  return snapshot as StoredRequestTab;
}

const tabs = ref<RequestTab[]>([createTab()]);
const activeTabId = ref(tabs.value[0]!.id);
const activeTab = computed(
  () => tabs.value.find((tab) => tab.id === activeTabId.value) ?? tabs.value[0]!
);
const httpResources = useHttpResourcesStore();
const { collections } = storeToRefs(httpResources);
function createDefaultEnvironment(): HttpEnvironment {
  return { id: id("environment"), name: t("http.defaultEnvironment"), vars: [emptyRow()] };
}
const activeTopLevelCollection = computed(() =>
  activeTab.value.savedCollectionId
    ? findTopLevelHttpCollection(collections.value, activeTab.value.savedCollectionId)
    : undefined
);
/** 未保存到集合的请求也可以固定使用任一集合的环境。 */
function findEnvironmentOwner(environmentId: string | undefined) {
  if (!environmentId) return undefined;
  return collections.value.find((collection) =>
    collection.environments?.some((environment) => environment.id === environmentId)
  );
}
const activeEnvironment = computed(() => {
  if (!activeTopLevelCollection.value) {
    if (activeTab.value.environmentMode !== "fixed") return undefined;
    return findEnvironmentOwner(activeTab.value.environmentId)?.environments?.find(
      (environment) => environment.id === activeTab.value.environmentId
    );
  }
  return resolveHttpRequestEnvironment(
    activeTopLevelCollection.value,
    activeTab.value.environmentId,
    activeTab.value.environmentMode
  );
});
/** 下拉里的可选环境：已保存的请求只列所属集合；未保存的请求按集合分组列出全部环境。 */
const environmentOptionGroups = computed(() =>
  (activeTopLevelCollection.value ? [activeTopLevelCollection.value] : collections.value)
    .filter((collection) => collection.environments?.length)
    .map((collection) => ({ id: collection.id, name: collection.name, environments: collection.environments ?? [] }))
);
const activeEnvironmentVariables = computed(() =>
  Object.fromEntries(
    (activeEnvironment.value?.vars ?? [])
      .filter((row) => row.enabled && row.key.trim())
      .map((row) => [row.key.trim(), row.value])
  )
);
const requestEnvironmentSelection = computed({
  get: () => activeTab.value.environmentMode === "fixed"
    ? activeTab.value.environmentId ?? "__follow__"
    : "__follow__",
  set: (value: string) => {
    activeTab.value.environmentMode = value === "__follow__" ? "follow" : "fixed";
    activeTab.value.environmentId = value === "__follow__" ? undefined : value;
    syncBoundRequest(activeTab.value);
  },
});
const envOpen = ref(false);
/** 跟随集合时在悬停提示里说明来源，按钮本身只显示环境名。 */
const environmentSelectionHint = computed(() => {
  if (!activeTopLevelCollection.value) {
    if (!environmentOptionGroups.value.length) return t("http.noEnvironmentsHint");
    return activeEnvironment.value
      ? t("http.unsavedEnvironmentHint", { collection: findEnvironmentOwner(activeEnvironment.value.id)?.name ?? "" })
      : t("http.unsavedNoEnvironmentHint");
  }
  return requestEnvironmentSelection.value === "__follow__"
    ? t("http.followCollectionEnvironmentHint", { collection: activeTopLevelCollection.value.name })
    : t("http.fixedEnvironmentHint");
});
/** 还没发过请求时响应区收成一行，把高度留给请求编辑。 */
const responseVisible = computed(() => Boolean(activeTab.value.response || activeTab.value.loading));
const sendShortcutKey = currentKeyboardPlatform() === "mac" ? "⌘" : "Ctrl";
const environmentManagerVisible = ref(false);
const environmentManagerCollection = ref<HttpRequestCollection>();
type HttpSidebarTab = "collections" | "history" | "environments";
const sidebarTab = ref<HttpSidebarTab>("collections");
const sidebarTabs = computed(() => [
  { value: "collections" as const, label: t("http.collectionsTab") },
  { value: "history" as const, label: t("http.history") },
  { value: "environments" as const, label: t("http.environments") },
]);
const collectionSearch = ref("");
// 纯过滤逻辑在 domain 层；搜索时强制展开命中链路，清空后回到用户自己的展开状态。
const collectionFilter = computed(() =>
  filterHttpCollections(collections.value, collectionSearch.value)
);
const visibleCollections = computed(() => collectionFilter.value.collections);
const visibleCollectionExpandedIds = computed(() =>
  collectionSearch.value.trim() ? collectionFilter.value.expandedIds : expandedCollectionIds.value
);
const history = ref<HistoryItem[]>([]);
const expandedCollectionIds = ref(new Set<string>());
interface CollectionTreeOption {
  value: string;
  label: string;
  children?: CollectionTreeOption[];
}

type SaveRequestMode = "save" | "save-as" | "move";

const saveRequestVisible = ref(false);
const saveRequestMode = ref<SaveRequestMode>("save");
const saveTargetCollectionId = ref("");
const saveRequestDialogTitle = computed(() =>
  saveRequestMode.value === "move" ? t("http.moveRequestTitle") : t("http.saveRequestTitle")
);
const collectionTreeOptions = computed(() => {
  const mapCollection = (collection: HttpRequestCollection): CollectionTreeOption => ({
    value: collection.id,
    label: collection.name,
    children: collection.children?.length ? collection.children.map(mapCollection) : undefined,
  });
  return collections.value.map(mapCollection);
});
const renamingCollectionId = ref("");
const renamingRequestId = ref("");
const renameValue = ref("");
const mockServerVisible = ref(false);
const curlImportVisible = ref(false);
const codeGeneratorVisible = ref(false);
const collectionRunnerVisible = ref(false);
const runningCollection = ref<HttpRequestCollection>();
const streamPanelVisible = ref(false);
const streamPanelKind = ref<"ws" | "sse">("ws");
const socketIOPanelVisible = ref(false);
const {
  size: sidebarWidth,
  collapsed: sidebarCollapsed,
  hydrate: hydrateSidebarLayout,
  persist: persistSidebarLayout,
} = useResizablePane({
  sizeKey: SIDEBAR_WIDTH_KEY,
  collapsedKey: SIDEBAR_COLLAPSED_KEY,
  defaultSize: 220,
  minSize: 150,
  maxSize: 380,
});

function persistSidebarWidth(value: number) {
  sidebarWidth.value = value;
  void persistSidebarLayout();
}

function updateSidebarCollapsed(value: boolean) {
  sidebarCollapsed.value = value;
  void persistSidebarLayout();
}
useToggleSidebarCommand(() => sidebarCollapsed.value, updateSidebarCollapsed);
const requestHeight = ref(Number(localStorage.getItem(REQUEST_HEIGHT_KEY)) || 50);

async function hydrateHttpState() {
  await httpResources.hydrate();
  const stored = await loadHttpState<StoredHttpState>(workspaceScopeId);
  expandedCollectionIds.value = new Set(collections.value.map((collection) => collection.id));
  if (!stored || ![1, 2, 3, 4, 5, 6].includes(stored.version)) return;

  history.value = normalizeHttpHistoryForPersistence(restoreHistory(stored.history));
  if (stored.version <= 5 && stored.activeEnvId) {
    for (const collection of collections.value) {
      if (collection.environments?.some((environment) => environment.id === stored.activeEnvId)) {
        collection.activeEnvironmentId = stored.activeEnvId;
      }
    }
  }
  const restoredTabs = restoreRequestTabs(stored.tabs).filter((tab, index, items) =>
    !tab.savedRequestId || items.findIndex((item) => item.savedRequestId === tab.savedRequestId) === index
  );
  if (restoredTabs.length) {
    tabs.value = restoredTabs;
    activeTabId.value = restoredTabs.some((tab) => tab.id === stored.activeTabId)
      ? stored.activeTabId!
      : restoredTabs[0]!.id;
  }
  await persistHttpState();
}

function persistHttpState() {
  const snapshot: StoredHttpState = {
    version: 6,
    history: normalizeHttpHistoryForPersistence(history.value),
    tabs: tabs.value.slice(0, MAX_PERSISTED_REQUEST_TABS).map(storedRequestTab),
    activeTabId: activeTabId.value,
  };
  return saveHttpState(workspaceScopeId, snapshot);
}

let httpPersistTimer: ReturnType<typeof setTimeout> | undefined;

function scheduleHttpStatePersist() {
  if (httpPersistTimer) clearTimeout(httpPersistTimer);
  httpPersistTimer = setTimeout(() => {
    httpPersistTimer = undefined;
    void persistHttpState();
  }, 300);
}

function flushBoundRequests() {
  if (boundRequestSyncTimer) clearTimeout(boundRequestSyncTimer);
  boundRequestSyncTimer = undefined;
  for (const tab of tabs.value) syncBoundRequest(tab);
}

function flushHttpWorkspaceState() {
  if (httpPersistTimer) clearTimeout(httpPersistTimer);
  httpPersistTimer = undefined;
  flushBoundRequests();
  return Promise.all([
    persistHttpState(),
    httpResources.flushPersistence(),
    savePersistedValue(REQUEST_HEIGHT_KEY, requestHeight.value),
  ]);
}

const unregisterHttpWorkspaceFlusher = registerUiSessionFlusher(flushHttpWorkspaceState);

watch(
  () => [history.value, tabs.value.map(storedRequestTab), activeTabId.value],
  scheduleHttpStatePersist,
  { deep: true }
);

// 大响应默认按原文显示；手动格式化的结果按原始 body 字符串缓存（同一引用比较为 O(1)）。
const manuallyFormattedBody = shallowRef<{ source: string; text: string } | null>(null);
const formattingLargeBody = ref(false);

const prettyBody = computed(() => {
  const body = activeTab.value.response?.body ?? "";
  if (!isLargeResponseBody(body)) return formatJsonResponseBody(body);
  const formatted = manuallyFormattedBody.value;
  return formatted?.source === body ? formatted.text : body;
});

const largeResponseUnformatted = computed(() => {
  const body = activeTab.value.response?.body ?? "";
  return (
    isLargeResponseBody(body) &&
    manuallyFormattedBody.value?.source !== body &&
    looksLikeJsonBody(body)
  );
});

async function formatLargeResponseBody() {
  const body = activeTab.value.response?.body ?? "";
  if (formattingLargeBody.value || !body) return;
  formattingLargeBody.value = true;
  try {
    // 在 Worker 中格式化：数 MB 的 JSON 同步格式化会让界面卡住近一秒。
    const text = await formatJsonOffThread(body);
    // 格式化期间响应可能已被替换，只缓存仍对应当前 body 的结果。
    if ((activeTab.value.response?.body ?? "") === body) manuallyFormattedBody.value = { source: body, text };
  } finally {
    formattingLargeBody.value = false;
  }
}

function addRequest() {
  const tab = createTab();
  tabs.value.push(tab);
  activeTabId.value = tab.id;
}

function closeRequest(tabId: string) {
  const closingTab = tabs.value.find((tab) => tab.id === tabId);
  if (closingTab) syncBoundRequest(closingTab);
  cancelSend(tabId);
  if (tabs.value.length === 1) {
    tabs.value = [createTab()];
    activeTabId.value = tabs.value[0]!.id;
    return;
  }
  const index = tabs.value.findIndex((tab) => tab.id === tabId);
  if (index < 0) return;
  tabs.value.splice(index, 1);
  autoTabNames.delete(tabId);
  if (activeTabId.value === tabId) {
    activeTabId.value = tabs.value[Math.max(0, index - 1)]!.id;
  }
}

/** 一次关闭多个标签：先同步已绑定的请求并取消在途请求；全部关闭时保留一个新的空标签。 */
function closeRequests(tabIds: readonly string[]) {
  const closing = new Set(tabIds);
  if (!closing.size) return;
  for (const tab of tabs.value) {
    if (!closing.has(tab.id)) continue;
    syncBoundRequest(tab);
    cancelSend(tab.id);
    autoTabNames.delete(tab.id);
  }
  const remaining = tabs.value.filter((tab) => !closing.has(tab.id));
  if (!remaining.length) {
    tabs.value = [createTab()];
    activeTabId.value = tabs.value[0]!.id;
    return;
  }
  if (closing.has(activeTabId.value)) {
    const activeIndex = tabs.value.findIndex((tab) => tab.id === activeTabId.value);
    const fallback =
      tabs.value.slice(0, activeIndex).reverse().find((tab) => !closing.has(tab.id)) ?? remaining[0]!;
    activeTabId.value = fallback.id;
  }
  tabs.value = remaining;
}

/** 复制标签：带上请求内容，但不绑定到原来保存的请求，也不带响应。 */
function duplicateRequestTab(source: RequestTab) {
  const copy: RequestTab = {
    ...(JSON.parse(JSON.stringify(storedRequestTab(source))) as StoredRequestTab),
    id: id("request"),
    name: `${source.name} ${t("tab.copySuffix")}`,
    loading: false,
    response: undefined,
    savedRequestId: undefined,
    savedCollectionId: undefined,
  };
  for (const key of ["params", "headers", "formBody"] as const) {
    const rows = copy[key] as Array<{ id: string }> | undefined;
    rows?.forEach((row) => (row.id = id(key === "formBody" ? "body" : "row")));
  }
  if (!source.savedCollectionId) {
    copy.environmentMode = source.environmentMode;
    copy.environmentId = source.environmentId;
  }
  const index = tabs.value.findIndex((tab) => tab.id === source.id);
  tabs.value.splice(index + 1, 0, copy);
  activeTabId.value = copy.id;
}

async function renameRequestTab(tab: RequestTab) {
  try {
    const { value } = await ElMessageBox.prompt(t("tab.renameTitle"), t("tab.rename"), {
      inputValue: tab.name,
      inputValidator: (name) => name.trim().length > 0 || t("http.collectionNameRequired"),
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    tab.name = value.trim().slice(0, 120);
    autoTabNames.delete(tab.id);
    syncBoundRequest(tab);
  } catch {
    // 用户取消
  }
}

async function copyRequestTabText(text: string, success: string) {
  try {
    await navigator.clipboard.writeText(text);
    ElMessage.success(success);
  } catch (error) {
    ElMessage.error(t("http.copyFailed", { message: String(error) }));
  }
}

const tabContextMenu = ref({ open: false, x: 0, y: 0, tabId: "" });
const contextTab = computed(() => tabs.value.find((tab) => tab.id === tabContextMenu.value.tabId));
const contextTabIndex = computed(() => tabs.value.findIndex((tab) => tab.id === tabContextMenu.value.tabId));

function openTabContextMenu(event: MouseEvent, tabId: string) {
  tabContextMenu.value = { open: true, x: event.clientX, y: event.clientY, tabId };
}

function closeTabContextMenu() {
  tabContextMenu.value.open = false;
}

function handleTabContextCommand(command: string) {
  const tab = contextTab.value;
  const index = contextTabIndex.value;
  closeTabContextMenu();
  if (!tab || index < 0) return;
  switch (command) {
    case "close":
      return closeRequest(tab.id);
    case "close-others":
      return closeRequests(tabs.value.filter((item) => item.id !== tab.id).map((item) => item.id));
    case "close-left":
      return closeRequests(tabs.value.slice(0, index).map((item) => item.id));
    case "close-right":
      return closeRequests(tabs.value.slice(index + 1).map((item) => item.id));
    case "close-all":
      return closeRequests(tabs.value.map((item) => item.id));
    case "duplicate":
      return duplicateRequestTab(tab);
    case "rename":
      return void renameRequestTab(tab);
    case "copy-name":
      return void copyRequestTabText(tab.name, t("http.tabNameCopied"));
    case "copy-url":
      return void copyRequestTabText(tab.url, t("http.urlCopied"));
    case "copy-curl":
      return void copyRequestTabText(serializeCurlCommand(tab), t("http.curlCopied"));
  }
}

// Look up the current row so the menu closes when its request disappears.
const requestMenu = ref<{ collectionId: string; requestId: string; x: number; y: number } | null>(null);
const requestMenuEntry = computed(() => {
  const menu = requestMenu.value;
  if (!menu || !requestRowVisible(menu.collectionId, menu.requestId)) return null;
  const collection = findHttpCollection(collections.value, menu.collectionId);
  const request = collection?.requests.find((item) => item.id === menu.requestId);
  return collection && request ? { collection, request } : null;
});

/** Whether the sidebar shows the request row now (search filter, collapsed collections, sidebar tab). */
function requestRowVisible(collectionId: string, requestId: string): boolean {
  if (sidebarTab.value !== "collections" || sidebarCollapsed.value) return false;
  const expanded = visibleCollectionExpandedIds.value;
  const shown = (nodes: HttpRequestCollection[]): boolean =>
    nodes.some(
      (node) =>
        expanded.has(node.id) &&
        ((node.id === collectionId && node.requests.some((item) => item.id === requestId)) || shown(node.children ?? []))
    );
  return shown(visibleCollections.value);
}
watch(requestMenuEntry, (entry) => {
  if (!entry) requestMenu.value = null;
});

function openRequestContextMenu(event: MouseEvent | KeyboardEvent, request: SavedHttpRequest, collection: HttpRequestCollection) {
  requestMenu.value = { collectionId: collection.id, requestId: request.id, ...contextMenuAnchor(event) };
}

function handleRequestMenuAction(action: "open" | "rename" | "delete" | "notes-new" | "notes-view") {
  const entry = requestMenuEntry.value;
  requestMenu.value = null;
  if (!entry) return;
  if (action === "notes-new" || action === "notes-view") {
    // 已保存请求按请求 id 关联，parentId 记录所在集合。
    void linkedNotes.open(
      { objectType: "http", objectId: entry.request.id, parentId: entry.collection.id, nameSnapshot: entry.request.name },
      { createNew: action === "notes-new" },
    );
  } else if (action === "open") openSavedRequest(entry.request, entry.collection);
  else if (action === "rename") void startRequestRename(entry.request);
  else void deleteSavedRequest(entry.collection, entry.request.id);
}

function addRow(target: "params" | "headers") {
  activeTab.value[target].push(emptyRow());
}

function addEnvironment() {
  const collection = environmentManagerCollection.value ?? activeTopLevelCollection.value;
  if (!collection) return;
  const environment = {
    id: id("environment"),
    name: `${t("http.environment")} ${(collection.environments?.length ?? 0) + 1}`,
    vars: [emptyRow()],
  };
  (collection.environments ??= []).push(environment);
  collection.activeEnvironmentId = environment.id;
}

async function deleteEnvironment(environment: HttpEnvironment) {
  const collection = environmentManagerCollection.value ?? activeTopLevelCollection.value;
  if (!collection || (collection.environments?.length ?? 0) <= 1) return false;
  try {
    await ElMessageBox.confirm(
      t("http.deleteEnvironmentConfirm", { name: environment.name }),
      t("http.deleteEnvironment"),
      {
        confirmButtonText: t("common.delete"),
        cancelButtonText: t("common.cancel"),
        type: "warning",
      }
    );
  } catch {
    return false;
  }
  collection.environments = collection.environments?.filter((item) => item.id !== environment.id);
  if (collection.activeEnvironmentId === environment.id) {
    collection.activeEnvironmentId = collection.environments?.[0]?.id;
  }
  for (const request of flattenCollectionRequests(collection)) {
    if (request.environmentId === environment.id) {
      request.environmentMode = "follow";
      request.environmentId = undefined;
    }
  }
  for (const tab of tabs.value) {
    const tabTopLevel = tab.savedCollectionId
      ? findTopLevelHttpCollection(collections.value, tab.savedCollectionId)
      : undefined;
    if (tabTopLevel?.id === collection.id && tab.environmentId === environment.id) {
      tab.environmentMode = "follow";
      tab.environmentId = undefined;
    }
  }
  return true;
}

function expandVariables(value: string) {
  const variableMap = new Map(
    (activeEnvironment.value?.vars ?? [])
      .filter((row) => row.enabled && row.key.trim())
      .map((row) => [row.key.trim(), row.value])
  );
  return value.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (match, key: string) => {
    return variableMap.get(key) ?? match;
  });
}

function importCurlRequest(request: ParsedCurlRequest) {
  const tab = activeTab.value;
  Object.assign(tab, {
    ...request,
    params: request.params.map((row) => ({ ...row, id: id("param") })),
    headers: request.headers.map((row) => ({ ...row, id: id("header") })),
    response: undefined,
    requestTab: request.bodyType !== "none" ? "body" : request.params.length ? "params" : "headers",
  });
  tab.name = requestUrlTabName(tab.url);
  if (!tab.savedRequestId) autoTabNames.set(tab.id, tab.name);
  ElMessage.success(t("http.curlImported"));
}

async function copyCurrentCurl() {
  if (!activeTab.value.url.trim()) return ElMessage.warning(t("http.urlRequired"));
  try {
    await navigator.clipboard.writeText(serializeCurlCommand(activeTab.value));
    ElMessage.success(t("http.curlCopied"));
  } catch (error) {
    ElMessage.error(t("http.copyFailed", { message: String(error) }));
  }
}

// Kept outside reactive state: one in-flight request per tab, aborted on cancel or close.
const pendingRequests = new Map<string, AbortController>();
// 由 URL 自动生成的标签名（按 tab id）。发送时只覆盖默认名或上次自动名，
// 绑定已保存请求的标签不改名，否则 syncBoundRequest 会把它写回集合里的请求名。
const autoTabNames = new Map<string, string>();

function requestUrlTabName(url: string) {
  try {
    const parsed = new URL(url);
    return parsed.pathname.split("/").filter(Boolean).pop() || parsed.host;
  } catch {
    return url || t("http.newRequest");
  }
}

function shouldAutoNameRequestTab(tab: RequestTab) {
  if (tab.savedRequestId) return false;
  return tab.name === t("http.newRequest") || tab.name === autoTabNames.get(tab.id);
}

function cancelSend(tabId = activeTabId.value) {
  pendingRequests.get(tabId)?.abort();
}

// 浏览器本地模式的 http_send_request 以 `http.browser.*` 文案键作为错误消息（见 services/browser-runtime/http.ts）。
function httpSendErrorMessage(error: unknown) {
  const message = error instanceof Error ? error.message : "";
  return message.startsWith("http.browser.") && te(message) ? t(message) : String(error);
}

const sentRequests = new WeakMap<ResponseData, { method: string; requestUrl: string; secrets: string[] }>();

/** Credential values that the response might echo. */
function requestSecrets(tab: RequestTab): string[] {
  const queryValues = (url: string) => {
    const query = url.indexOf("?");
    if (query < 0) return [];
    const hash = url.indexOf("#", query);
    const search = url.slice(query + 1, hash < 0 ? undefined : hash);
    // Decoded (URLSearchParams) and as written in the address (percent-encoded), since a header may echo either.
    const decoded = [...new URLSearchParams(search)].filter(([key]) => isSensitiveDataName(key)).map(([, value]) => value);
    const raw = search.split("&").flatMap((pair) => {
      const equals = pair.indexOf("=");
      if (equals < 0) return [];
      let key = pair.slice(0, equals);
      try {
        key = decodeURIComponent(key.replace(/\+/g, " "));
      } catch {
        // A malformed escape: match the key as written.
      }
      return isSensitiveDataName(key) ? [pair.slice(equals + 1)] : [];
    });
    return [...decoded, ...raw];
  };
  const values = [
    tab.authToken,
    tab.authPassword,
    tab.apiKeyValue,
    tab.oauth2ClientSecret,
    tab.oauth2Password,
    tab.oauth2AccessToken,
    ...tab.headers.filter((row) => row.enabled && isSensitiveDataName(row.key)).map((row) => row.value),
    ...tab.params.filter((row) => row.enabled && isSensitiveDataName(row.key)).map((row) => row.value),
  ];
  const expanded = values.flatMap((value) => [value, expandVariables(value)]);
  return [...new Set([...expanded, ...queryValues(tab.url), ...queryValues(expandVariables(tab.url))].filter(Boolean))];
}

function explainResponse() {
  const response = activeTab.value.response;
  if (response) void handoffHttpResponse(response, sentRequests.get(toRaw(response)));
}

async function send() {
  if (!activeTab.value.url.trim()) {
    ElMessage.warning(t("http.urlRequired"));
    return;
  }
  if (!canInvoke("http_send_request")) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }

  const tab = activeTab.value;
  pendingRequests.get(tab.id)?.abort();
  const controller = new AbortController();
  pendingRequests.set(tab.id, controller);
  tab.loading = true;
  try {
    const payload = buildNativeHttpRequestPayload(tab, expandVariables);
    const sent = { method: payload.method, requestUrl: payload.url, secrets: requestSecrets(tab) };
    const response = await invokeBackend<ResponseData>("http_send_request", {
      payload,
    }, { signal: controller.signal });
    sentRequests.set(response, sent);
    tab.response = response;
    if (shouldAutoNameRequestTab(tab)) {
      tab.name = requestUrlTabName(tab.url);
      autoTabNames.set(tab.id, tab.name);
    }
    history.value = [{
      id: id("history"),
      method: tab.method,
      url: tab.url,
      name: tab.name,
      status: tab.response.status,
      elapsed: tab.response.elapsed_ms,
      createdAt: Date.now(),
    }, ...history.value].slice(0, HTTP_HISTORY_MAX_ITEMS);
  } catch (error) {
    if (controller.signal.aborted) ElMessage.info(t("http.requestCancelled"));
    else ElMessage.error(httpSendErrorMessage(error));
  } finally {
    if (pendingRequests.get(tab.id) === controller) {
      pendingRequests.delete(tab.id);
      tab.loading = false;
    }
  }
}

function openHistory(item: HistoryItem) {
  const tab = createTab();
  tab.method = item.method;
  tab.url = item.url;
  tab.name = historyDisplayName(item, t("http.newRequest"));
  autoTabNames.set(tab.id, tab.name);
  tabs.value.push(tab);
  activeTabId.value = tab.id;
}

function clearHistory() {
  history.value = [];
}

function removeHistoryItem(historyId: string) {
  history.value = history.value.filter((item) => item.id !== historyId);
}

const historySearch = ref("");
const historyGroups = computed(() =>
  groupHttpHistory(filterHttpHistory(history.value, historySearch.value, t("http.newRequest")))
);

function historyGroupLabel(group: { day: string; date: number }) {
  if (group.day === "today") return t("http.historyToday");
  if (group.day === "yesterday") return t("http.historyYesterday");
  return new Date(group.date).toLocaleDateString(undefined, { month: "long", day: "numeric", weekday: "short" });
}

function historyTime(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function historyElapsed(ms: number) {
  return ms >= 1000 ? `${(ms / 1000).toFixed(ms >= 10_000 ? 0 : 1)} s` : `${ms} ms`;
}

/** 标签行只横向滚动：鼠标滚轮的纵向滚动换算成横向。 */
const requestTabsScrollbar = ref<ScrollbarInstance>();
function scrollRequestTabs(event: WheelEvent) {
  const wrap = requestTabsScrollbar.value?.wrapRef;
  if (!wrap || wrap.scrollWidth <= wrap.clientWidth) return;
  const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
  if (!delta) return;
  event.preventDefault();
  wrap.scrollLeft += delta;
}

/** 切换或新建标签后，把当前标签滚到可见位置。 */
watch(activeTabId, () => {
  void nextTick(() => {
    requestTabsScrollbar.value?.wrapRef
      ?.querySelector<HTMLElement>(".http-request-tab.is-active")
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
});

function runnableCollection(collection: HttpRequestCollection): HttpRequestCollection {
  return { ...collection, requests: flattenCollectionRequests(collection), children: [] };
}

function openCollectionRunner(collection: HttpRequestCollection) {
  const topLevel = findTopLevelHttpCollection(collections.value, collection.id);
  runningCollection.value = {
    ...runnableCollection(collection),
    environments: topLevel?.environments,
    activeEnvironmentId: topLevel?.activeEnvironmentId,
  };
  collectionRunnerVisible.value = true;
}

async function addCollection(parent?: HttpRequestCollection) {
  try {
    const { value } = await ElMessageBox.prompt(
      t("http.collectionNamePrompt"),
      t("http.newCollection"),
      {
        inputValidator: (name) => name.trim().length > 0 || t("http.collectionNameRequired"),
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      }
    );
    const collection: HttpRequestCollection = {
      id: id("collection"),
      name: value.trim(),
      requests: [],
      children: [],
      ...(parent
        ? {}
        : (() => {
            const environment = createDefaultEnvironment();
            return { environments: [environment], activeEnvironmentId: environment.id };
          })()),
    };
    if (parent) (parent.children ??= []).push(collection);
    else collections.value.push(collection);
    if (parent) expandedCollectionIds.value.add(parent.id);
    expandedCollectionIds.value.add(collection.id);
    return collection;
  } catch {
    // 用户取消创建集合。
  }
}

function addRequestToCollection(collection: HttpRequestCollection) {
  const tab = createTab();
  const saved = createSavedHttpRequest(tab, id("saved-request"));
  collection.requests.push(saved);
  tab.savedRequestId = saved.id;
  tab.savedCollectionId = collection.id;
  tabs.value.push(tab);
  activeTabId.value = tab.id;
  expandedCollectionIds.value.add(collection.id);
}

function syncBoundRequest(tab: RequestTab) {
  if (!tab.savedRequestId || !tab.savedCollectionId) return;
  const collection = findHttpCollection(collections.value, tab.savedCollectionId);
  const saved = collection?.requests.find((request) => request.id === tab.savedRequestId);
  if (saved) Object.assign(saved, createSavedHttpRequest(tab, saved.id));
}

let boundRequestSyncTimer: ReturnType<typeof setTimeout> | undefined;
watch(
  tabs,
  () => {
    if (boundRequestSyncTimer) clearTimeout(boundRequestSyncTimer);
    boundRequestSyncTimer = setTimeout(() => {
      boundRequestSyncTimer = undefined;
      for (const tab of tabs.value) syncBoundRequest(tab);
    }, 300);
  },
  { deep: true }
);

function openSaveRequestDialog(mode: SaveRequestMode = "save") {
  if (!activeTab.value.url.trim()) return ElMessage.warning(t("http.urlRequired"));
  if (activeTab.value.savedRequestId && mode === "save") {
    syncBoundRequest(activeTab.value);
    void httpResources.flushPersistence();
    ElMessage.success(t("http.requestUpdated"));
    return;
  }
  saveRequestMode.value = mode;
  saveTargetCollectionId.value = mode === "move"
    ? ""
    : activeTab.value.savedCollectionId ?? collections.value[0]?.id ?? "";
  saveRequestVisible.value = true;
}

function saveCurrentRequest() {
  const target = findHttpCollection(collections.value, saveTargetCollectionId.value);
  if (!target) return ElMessage.warning(t("http.selectCollection"));
  const tab = activeTab.value;
  if (saveRequestMode.value === "move" && tab.savedCollectionId === target.id) {
    return ElMessage.warning(t("http.selectDifferentCollection"));
  }
  const existing = saveRequestMode.value !== "save-as" && tab.savedRequestId
    ? flattenCollectionRequests({ id: "root", name: "", requests: [], children: collections.value })
        .find((request) => request.id === tab.savedRequestId)
    : undefined;
  const saved = createSavedHttpRequest(tab, existing?.id ?? id("saved-request"));
  const targetTopLevel = findTopLevelHttpCollection(collections.value, target.id);
  if (
    saved.environmentMode === "fixed" &&
    (!saved.environmentId ||
      !targetTopLevel?.environments?.some((environment) => environment.id === saved.environmentId))
  ) {
    saved.environmentMode = "follow";
    saved.environmentId = undefined;
  }
  tab.environmentMode = saved.environmentMode ?? "follow";
  tab.environmentId = saved.environmentId;
  if (existing) Object.assign(existing, saved);
  else target.requests.push(saved);
  if (!existing || tab.savedCollectionId !== target.id) {
    if (existing) moveSavedHttpRequest(collections.value, saved.id, target.id);
    tab.savedRequestId = saved.id;
    tab.savedCollectionId = target.id;
  }
  expandedCollectionIds.value.add(target.id);
  saveRequestVisible.value = false;
  const completedMode = saveRequestMode.value;
  saveRequestMode.value = "save";
  ElMessage.success(
    completedMode === "move"
      ? t("http.requestMoved")
      : existing
        ? t("http.requestUpdated")
        : t("http.requestSaved")
  );
}

async function startCollectionRename(collection: HttpRequestCollection) {
  renamingRequestId.value = "";
  renamingCollectionId.value = collection.id;
  renameValue.value = collection.name;
  await nextTick();
  document.querySelector<HTMLInputElement>(`[data-http-collection-rename="${collection.id}"] input`)?.select();
}

async function startRequestRename(request: SavedHttpRequest) {
  renamingCollectionId.value = "";
  renamingRequestId.value = request.id;
  renameValue.value = request.name;
  await nextTick();
  document.querySelector<HTMLInputElement>(`[data-http-request-rename="${request.id}"] input`)?.select();
}

function saveRename() {
  const name = renameValue.value.trim();
  if (name && renamingCollectionId.value) {
    const collection = findHttpCollection(collections.value, renamingCollectionId.value);
    if (collection) collection.name = name;
  } else if (name && renamingRequestId.value) {
    const request = collections.value
      .flatMap((collection) => flattenCollectionRequests(collection))
      .find((item) => item.id === renamingRequestId.value);
    if (request) {
      request.name = name;
      request.updatedAt = Date.now();
      for (const tab of tabs.value) {
        if (tab.savedRequestId === request.id) tab.name = name;
      }
    }
  }
  cancelRename();
}

function cancelRename() {
  renamingCollectionId.value = "";
  renamingRequestId.value = "";
  renameValue.value = "";
}

function openSavedRequest(saved: SavedHttpRequest, collection: HttpRequestCollection) {
  const matchingTabs = tabs.value.filter((tab) => tab.savedRequestId === saved.id);
  if (matchingTabs.length) {
    const existingTab =
      matchingTabs.find((tab) => tab.id === activeTabId.value) ?? matchingTabs[matchingTabs.length - 1]!;
    if (matchingTabs.length > 1) {
      tabs.value = tabs.value.filter(
        (tab) => tab.savedRequestId !== saved.id || tab.id === existingTab.id
      );
    }
    activeTabId.value = existingTab.id;
    return;
  }
  const tab = createTab();
  Object.assign(tab, {
    name: saved.name,
    method: saved.method,
    url: saved.url,
    params: saved.params.map((row) => ({ ...row, id: id("param") })),
    headers: saved.headers.map((row) => ({ ...row, id: id("header") })),
    body: saved.body,
    bodyType: saved.bodyType ?? (saved.body ? "raw" : "none"),
    formBody: saved.formBody?.map((row) => ({ ...row, id: id("body") })) ?? [emptyBodyField()],
    binaryFilePath: saved.binaryFilePath ?? "",
    binaryFileName: saved.binaryFileName ?? "",
    authType: saved.authType,
    authToken: saved.authToken ?? "",
    authUsername: saved.authUsername ?? "",
    authPassword: saved.authPassword ?? "",
    apiKeyName: saved.apiKeyName ?? "X-API-Key",
    apiKeyValue: saved.apiKeyValue ?? "",
    apiKeyAddTo: saved.apiKeyAddTo ?? "header",
    oauth2GrantType: saved.oauth2GrantType ?? "authorization-code",
    oauth2AuthUrl: saved.oauth2AuthUrl ?? "",
    oauth2TokenUrl: saved.oauth2TokenUrl ?? "",
    oauth2ClientId: saved.oauth2ClientId ?? "",
    oauth2ClientSecret: saved.oauth2ClientSecret ?? "",
    oauth2Scope: saved.oauth2Scope ?? "",
    oauth2CallbackUrl: saved.oauth2CallbackUrl ?? "",
    oauth2Username: saved.oauth2Username ?? "",
    oauth2Password: saved.oauth2Password ?? "",
    oauth2AccessToken: saved.oauth2AccessToken ?? "",
    graphqlQuery: saved.graphqlQuery ?? "",
    graphqlVariables: saved.graphqlVariables ?? "",
    documentation: saved.documentation,
    environmentMode: saved.environmentMode ?? (saved.environmentId ? "fixed" : "follow"),
    environmentId: saved.environmentId,
    savedRequestId: saved.id,
    savedCollectionId: collection.id,
  });
  tabs.value.push(tab);
  activeTabId.value = tab.id;
}

async function deleteSavedRequest(collection: HttpRequestCollection, requestId: string) {
  const request = collection.requests.find((item) => item.id === requestId);
  if (!request) return;
  try {
    await ElMessageBox.confirm(
      t("http.deleteRequestConfirm", { name: request.name }),
      t("http.deleteRequest"),
      {
        confirmButtonText: t("common.delete"),
        cancelButtonText: t("common.cancel"),
        type: "warning",
      }
    );
  } catch {
    return;
  }
  collection.requests = collection.requests.filter((item) => item.id !== requestId);
  for (const tab of tabs.value) {
    if (tab.savedRequestId === requestId) {
      tab.savedRequestId = undefined;
      tab.savedCollectionId = undefined;
      tab.environmentMode = "follow";
      tab.environmentId = undefined;
    }
  }
}

function deleteCollection(collectionId: string) {
  const deleted = findHttpCollection(collections.value, collectionId);
  if (!deleted) return;
  const deletedRequestIds = new Set(flattenCollectionRequests(deleted).map((request) => request.id));
  const deletedCollectionIds = new Set<string>();
  const collectIds = (collection: HttpRequestCollection) => {
    deletedCollectionIds.add(collection.id);
    for (const child of collection.children ?? []) collectIds(child);
  };
  collectIds(deleted);
  const remove = (items: HttpRequestCollection[]): boolean => {
    const index = items.findIndex((collection) => collection.id === collectionId);
    if (index >= 0) {
      items.splice(index, 1);
      return true;
    }
    return items.some((collection) => remove(collection.children ?? []));
  };
  remove(collections.value);
  for (const tab of tabs.value) {
    if (
      (tab.savedRequestId && deletedRequestIds.has(tab.savedRequestId)) ||
      (tab.savedCollectionId && deletedCollectionIds.has(tab.savedCollectionId))
    ) {
      tab.savedRequestId = undefined;
      tab.savedCollectionId = undefined;
      tab.environmentMode = "follow";
      tab.environmentId = undefined;
    }
  }
}

function apiDocumentErrorMessage(error: unknown) {
  const code = error instanceof Error ? error.message : String(error);
  const key = `http.apiImportError.${code}`;
  const translated = t(key as never);
  return translated === key ? code : translated;
}

async function importCollection(target?: HttpRequestCollection) {
  try {
    const file = await chooseApiDocumentFile(t("http.importApiDocument"));
    if (!file) return;
    const imported = importApiDocument(file.content, file.name, id, {
      environmentName: t("http.importedEnvironment"),
    });
    const topLevel = target ? findTopLevelHttpCollection(collections.value, target.id) ?? target : undefined;

    if (imported.kind === "environment") {
      // 环境文件只能并入已有集合；从侧边栏导入时并入当前请求所在集合。
      const owner = topLevel ?? activeTopLevelCollection.value;
      if (!owner) return ElMessage.warning(t("http.importEnvironmentNeedsCollection"));
      mergeImportedEnvironments(owner, [imported.environment]);
      ElMessage.success(t("http.importEnvironmentSuccess", { name: imported.environment.name, collection: owner.name }));
      return;
    }
    if (!imported.requestCount) return ElMessage.warning(t("http.importEmpty"));

    let mode: "new" | "merge" = "new";
    if (target) {
      const result = await ElMessageBox.confirm(
        t("http.importModeHint", { name: target.name }),
        t("http.importMode"),
        {
          confirmButtonText: t("http.mergeCurrentCollection"),
          cancelButtonText: t("http.createNewCollection"),
          distinguishCancelAndClose: true,
          type: "info",
        }
      )
        .then(() => "merge" as const)
        .catch((action) => {
          if (action === "cancel") return "new" as const;
          throw action;
        });
      mode = result;
    }

    if (mode === "merge" && target) {
      mergeImportedCollection(target, imported, id);
      if (topLevel) mergeImportedEnvironments(topLevel, imported.environments);
    } else {
      const collection = importedApiCollection(imported, id, createDefaultEnvironment);
      collection.name = uniqueCollectionName(collection.name, collections.value.map((item) => item.name));
      collections.value.push(collection);
      expandedCollectionIds.value.add(collection.id);
    }
    sidebarTab.value = "collections";
    ElMessage.success(
      t("http.importSuccessFrom", {
        count: imported.requestCount,
        source: t(`http.apiImportSource.${imported.source}` as never),
      })
    );
  } catch (error) {
    ElMessage.error(t("http.importFailed", { message: apiDocumentErrorMessage(error) }));
  }
}

/** 导出时用所属顶级集合的当前环境展开 {{baseUrl}} 等服务器地址。 */
function exportEnvironment(collection: HttpRequestCollection) {
  const topLevel = findTopLevelHttpCollection(collections.value, collection.id) ?? collection;
  return (
    topLevel.environments?.find((environment) => environment.id === topLevel.activeEnvironmentId) ??
    topLevel.environments?.[0]
  );
}

async function exportCollection(collection: HttpRequestCollection, format: ApiDocumentFormat) {
  try {
    const saved = await saveApiDocumentFile(
      t("http.exportApiDocument"),
      collection.name,
      format,
      serializeApiDocument(collection, format, exportEnvironment(collection))
    );
    if (saved) ElMessage.success(t("http.exportSuccess"));
  } catch (error) {
    ElMessage.error(t("http.exportFailed", { message: String(error) }));
  }
}

function handleCollectionCommand(collection: HttpRequestCollection, command: string) {
  if (command === "manage-environments") {
    environmentManagerCollection.value =
      findTopLevelHttpCollection(collections.value, collection.id) ?? collection;
    environmentManagerVisible.value = true;
    return;
  }
  if (command === "import") return void importCollection(collection);
  if (command === "delete") return deleteCollection(collection.id);
  return void exportCollection(collection, command as ApiDocumentFormat);
}

function enabledEnvironmentVarCount(environment: HttpEnvironment) {
  return environment.vars.filter((row) => row.enabled && row.key.trim()).length;
}

function toggleCollection(collectionId: string) {
  const next = new Set(expandedCollectionIds.value);
  if (next.has(collectionId)) next.delete(collectionId);
  else next.add(collectionId);
  expandedCollectionIds.value = next;
}

function dropOnCollection(event: DragEvent, target: HttpRequestCollection) {
  const requestId = event.dataTransfer?.getData("application/x-http-request");
  const collectionId = event.dataTransfer?.getData("application/x-http-collection");
  const moved = requestId
    ? moveSavedHttpRequest(collections.value, requestId, target.id)
    : collectionId
      ? moveHttpCollection(collections.value, collectionId, target.id)
      : false;
  if (!moved) return;
  expandedCollectionIds.value.add(target.id);
  if (requestId) {
    const targetTopLevel = findTopLevelHttpCollection(collections.value, target.id);
    const request = target.requests.find((item) => item.id === requestId);
    if (
      request?.environmentMode === "fixed" &&
      (!request.environmentId ||
        !targetTopLevel?.environments?.some((environment) => environment.id === request.environmentId))
    ) {
      request.environmentMode = "follow";
      request.environmentId = undefined;
    }
    for (const tab of tabs.value) {
      if (tab.savedRequestId !== requestId) continue;
      tab.savedCollectionId = target.id;
      tab.environmentMode = request?.environmentMode ?? "follow";
      tab.environmentId = request?.environmentId;
    }
  } else if (collectionId) {
    const movedCollection = findHttpCollection(collections.value, collectionId);
    const movedRequests = new Map(
      (movedCollection ? flattenCollectionRequests(movedCollection) : []).map((request) => [request.id, request])
    );
    for (const tab of tabs.value) {
      const saved = tab.savedRequestId ? movedRequests.get(tab.savedRequestId) : undefined;
      if (!saved) continue;
      tab.environmentMode = saved.environmentMode ?? "follow";
      tab.environmentId = saved.environmentId;
    }
  }
}

function openStreamPanel(kind: "ws" | "sse") {
  streamPanelKind.value = kind;
  streamPanelVisible.value = true;
}

function openSocketIOPanel() {
  socketIOPanelVisible.value = true;
}

function openConnectionTool(command: string | number | object) {
  if (command === "ws" || command === "sse") openStreamPanel(command);
  else if (command === "socketio") openSocketIOPanel();
  else if (command === "mock") openMockServer();
}

function handleSidebarCreate(command: string | number | object) {
  if (command === "collection") void addCollection();
  else addRequest();
}

function handleSendCommand(command: string | number | object) {
  if (command === "code") codeGeneratorVisible.value = true;
  else if (command === "copy-curl") void copyCurrentCurl();
}

function handleRequestMoreCommand(command: string | number | object) {
  if (command === "import-curl") curlImportVisible.value = true;
  else handleSendCommand(command);
}

/** 请求编辑区内 ⌘/Ctrl + Enter 发送（多行请求体里回车仍是换行）。 */
function handleRequestPanelKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || event.isComposing) return;
  if (!(sendShortcutKey === "⌘" ? event.metaKey : event.ctrlKey)) return;
  event.preventDefault();
  if (!activeTab.value.loading) void send();
}

function openMockServer() {
  mockServerVisible.value = true;
}

function collectionContainingRequest(items: HttpRequestCollection[], requestId: string): HttpRequestCollection | undefined {
  for (const collection of items) {
    if (collection.requests.some((request) => request.id === requestId)) return collection;
    const nested = collectionContainingRequest(collection.children ?? [], requestId);
    if (nested) return nested;
  }
}

function findSavedHttpRequest(collectionId: string, requestId: string) {
  const collection = findHttpCollection(collections.value, collectionId);
  const request = collection?.requests.find((item) => item.id === requestId);
  if (!collection || !request) throw new Error("The requested saved HTTP request was not found");
  return { collection, request };
}

function httpAssetsForAi() {
  const savedRequests = collections.value.flatMap((collection) =>
    flattenCollectionRequests(collection).map((request) => ({
      id: request.id,
      collectionId: collection.id,
      name: request.name,
      method: request.method,
      url: request.url,
    }))
  );
  return {
    tabs: tabs.value.map((tab) => ({ id: tab.id, name: tab.name, method: tab.method, url: tab.url, active: tab.id === activeTabId.value })),
    environments: collections.value.flatMap((collection) =>
      (collection.environments ?? []).map((environment) => ({
        id: environment.id,
        collectionId: collection.id,
        name: environment.name,
        active: environment.id === collection.activeEnvironmentId,
        variableNames: environment.vars
          .filter((row) => row.enabled && row.key.trim())
          .map((row) => row.key.trim()),
      }))
    ),
    collections: collections.value.map((collection) => ({ id: collection.id, name: collection.name })),
    savedRequests,
    history: history.value.map((item) => ({ id: item.id, method: item.method, url: item.url, name: historyDisplayName(item, t("http.newRequest")), status: item.status, elapsed: item.elapsed, createdAt: item.createdAt })),
  };
}

async function handleAiHttpWorkspaceAction(input: Record<string, unknown>) {
  if (input.action === "active-resource") return activeTab.value.savedRequestId || activeTab.value.id;
  if (input.action === "open-resource") {
    await httpWorkspaceHydration;
    const id = String(input.id ?? "");
    const tab = tabs.value.find((item) => item.id === id || item.savedRequestId === id);
    if (tab) {
      activeTabId.value = tab.id;
      return { id, tabId: tab.id };
    }
    const collection = collectionContainingRequest(collections.value, id);
    const request = collection?.requests.find((item) => item.id === id);
    if (!collection || !request) throw new Error(t("ai.navigation.resourceMissing", { id }));
    openSavedRequest(request, collection);
    return { id, tabId: activeTabId.value };
  }
  const action = typeof input.action === "string" ? input.action : "";
  switch (action) {
    case "list-assets":
      return httpAssetsForAi();
    case "new-request": {
      addRequest();
      const tab = activeTab.value;
      const method = typeof input.method === "string" ? input.method.toUpperCase() : "";
      if (["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].includes(method)) tab.method = method;
      if (typeof input.url === "string") tab.url = input.url.trim();
      if (typeof input.name === "string" && input.name.trim()) tab.name = input.name.trim();
      return { id: tab.id, method: tab.method, url: tab.url, name: tab.name };
    }
    case "close-request": {
      const tabId = typeof input.tabId === "string" && input.tabId ? input.tabId : activeTabId.value;
      if (!tabs.value.some((tab) => tab.id === tabId)) throw new Error("The requested HTTP request tab was not found");
      closeRequest(tabId);
      return { tabId, closed: true, activeTabId: activeTabId.value };
    }
    case "send-active": {
      await send();
      const response = activeTab.value.response;
      return response ? { status: response.status, statusText: response.status_text, elapsedMs: response.elapsed_ms } : { sent: false };
    }
    case "create-environment": {
      const name = typeof input.name === "string" ? input.name.trim() : "";
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      const collection = findTopLevelHttpCollection(collections.value, collectionId);
      if (!collection || !name) throw new Error("A top-level collection and environment name are required");
      const environment = { id: id("environment"), name, vars: [emptyRow()] };
      (collection.environments ??= []).push(environment);
      collection.activeEnvironmentId = environment.id;
      return { id: environment.id, collectionId: collection.id, name };
    }
    case "select-environment": {
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      const environmentId = typeof input.environmentId === "string" ? input.environmentId : "";
      const collection = findTopLevelHttpCollection(collections.value, collectionId);
      if (!collection?.environments?.some((environment) => environment.id === environmentId)) {
        throw new Error("The requested HTTP collection environment was not found");
      }
      collection.activeEnvironmentId = environmentId;
      return { collectionId: collection.id, environmentId, selected: true };
    }
    case "delete-environment": {
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      const environmentId = typeof input.environmentId === "string" ? input.environmentId : "";
      const collection = findTopLevelHttpCollection(collections.value, collectionId);
      const environment = collection?.environments?.find((item) => item.id === environmentId);
      if (!collection || !environment) throw new Error("The requested HTTP collection environment was not found");
      if ((collection.environments?.length ?? 0) <= 1) throw new Error("The final HTTP environment cannot be deleted");
      environmentManagerCollection.value = collection;
      const deleted = await deleteEnvironment(environment);
      return { collectionId: collection.id, environmentId, deleted };
    }
    case "create-collection": {
      const name = typeof input.name === "string" ? input.name.trim() : "";
      if (!name) throw new Error("A collection name is required");
      const parentId = typeof input.parentId === "string" ? input.parentId : "";
      const parent = parentId ? findHttpCollection(collections.value, parentId) : undefined;
      if (parentId && !parent) throw new Error("The parent HTTP collection was not found");
      const environment = parent ? undefined : createDefaultEnvironment();
      const collection: HttpRequestCollection = {
        id: id("collection"),
        name,
        requests: [],
        children: [],
        ...(environment ? { environments: [environment], activeEnvironmentId: environment.id } : {}),
      };
      if (parent) {
        (parent.children ??= []).push(collection);
        expandedCollectionIds.value.add(parent.id);
      } else collections.value.push(collection);
      expandedCollectionIds.value.add(collection.id);
      sidebarTab.value = "collections";
      return { id: collection.id, name, parentId: parentId || undefined };
    }
    case "rename-collection": {
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      const name = typeof input.name === "string" ? input.name.trim() : "";
      const collection = findHttpCollection(collections.value, collectionId);
      if (!collection || !name) throw new Error("The requested HTTP collection or name is invalid");
      collection.name = name;
      return { collectionId, name };
    }
    case "delete-collection": {
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      if (!findHttpCollection(collections.value, collectionId)) throw new Error("The requested HTTP collection was not found");
      deleteCollection(collectionId);
      return { collectionId, deleted: true };
    }
    case "open-saved-request": {
      const { collection, request } = findSavedHttpRequest(String(input.collectionId ?? ""), String(input.requestId ?? ""));
      openSavedRequest(request, collection);
      return { tabId: activeTab.value.id, requestId: request.id, collectionId: collection.id };
    }
    case "save-active-request": {
      const collectionId = typeof input.collectionId === "string" ? input.collectionId : "";
      const target = findHttpCollection(collections.value, collectionId);
      if (!target) throw new Error("The requested HTTP collection was not found");
      const tab = activeTab.value;
      if (!tab.url.trim()) throw new Error("The active HTTP request requires a URL before it can be saved");
      const existing = tab.savedRequestId
        ? flattenCollectionRequests({ id: "root", name: "", requests: [], children: collections.value }).find((request) => request.id === tab.savedRequestId)
        : undefined;
      const saved = createSavedHttpRequest(tab, existing?.id ?? id("saved-request"));
      const targetTopLevel = findTopLevelHttpCollection(collections.value, target.id);
      if (
        saved.environmentId &&
        !targetTopLevel?.environments?.some((environment) => environment.id === saved.environmentId)
      ) {
        saved.environmentId = undefined;
      }
      tab.environmentId = saved.environmentId;
      if (existing) Object.assign(existing, saved);
      else target.requests.push(saved);
      if (!existing || tab.savedCollectionId !== target.id) {
        if (existing) moveSavedHttpRequest(collections.value, saved.id, target.id);
        tab.savedRequestId = saved.id;
        tab.savedCollectionId = target.id;
      }
      expandedCollectionIds.value.add(target.id);
      return { requestId: saved.id, collectionId: target.id, updated: Boolean(existing) };
    }
    case "open-history": {
      const historyId = typeof input.historyId === "string" ? input.historyId : "";
      const item = history.value.find((entry) => entry.id === historyId);
      if (!item) throw new Error("The requested HTTP history item was not found");
      openHistory(item);
      return { tabId: activeTab.value.id, historyId };
    }
    case "clear-history":
      clearHistory();
      return { cleared: true };
    case "open-stream": {
      const kind = input.kind === "ws" || input.kind === "sse" ? input.kind : undefined;
      if (!kind) throw new Error("Stream kind must be ws or sse");
      openStreamPanel(kind);
      return { kind, opened: true };
    }
    case "open-mock-server":
      openMockServer();
      return { opened: true };
    default:
      throw new Error(`Unsupported HTTP workspace action: ${action || "unknown"}`);
  }
}

let unregisterAiHttpWorkspaceActions: (() => void) | undefined;
function registerAiHttpWorkspaceActions() {
  unregisterAiHttpWorkspaceActions?.();
  unregisterAiHttpWorkspaceActions = registerAiWorkspaceActionHandler(
    "http",
    handleAiHttpWorkspaceAction
  );
}
registerAiHttpWorkspaceActions();
onActivated(registerAiHttpWorkspaceActions);
onDeactivated(() => {
  unregisterAiHttpWorkspaceActions?.();
  unregisterAiHttpWorkspaceActions = undefined;
  // KeepAlive'd: leaving the view closes the request menu.
  requestMenu.value = null;
});

function persistRequestHeight(value: number) {
  requestHeight.value = value;
  return savePersistedValue(REQUEST_HEIGHT_KEY, value);
}

let httpWorkspaceHydration: Promise<void> | undefined;
onMounted(async () => {
  httpWorkspaceHydration = hydrateHttpState();
  const [height] = await Promise.all([
    loadPersistedValue<unknown>(REQUEST_HEIGHT_KEY),
    hydrateSidebarLayout(),
  ]);
  if (typeof height === "number" && Number.isFinite(height))
    requestHeight.value = Math.min(75, Math.max(28, height));
  await Promise.all([
    savePersistedValue(REQUEST_HEIGHT_KEY, requestHeight.value),
    httpWorkspaceHydration,
  ]);
});
onBeforeUnmount(() => {
  for (const controller of pendingRequests.values()) controller.abort();
  unregisterAiHttpWorkspaceActions?.();
  unregisterHttpWorkspaceFlusher();
  void flushHttpWorkspaceState();
});
</script>

<template>
  <section class="http-workspace">
    <AppSplitPane
      :model-value="sidebarWidth"
      :collapsed="sidebarCollapsed"
      side="first"
      :min="150"
      :max="380"
      :second-min="400"
      collapsible
      :collapse-label="t('http.collapseSidebar')"
      :expand-label="t('http.expandSidebar')"
      :aria-label="t('http.resizeSidebar')"
      @resize-end="persistSidebarWidth"
      @update:collapsed="updateSidebarCollapsed"
    >
      <template #first>
        <aside class="http-sidebar">
      <div class="http-sidebar-tabs" role="tablist" :aria-label="t('http.sidebarNavigation')">
        <button
          v-for="item in sidebarTabs"
          :key="item.value"
          type="button"
          role="tab"
          class="http-sidebar-tab"
          :class="{ 'is-active': sidebarTab === item.value }"
          :aria-selected="sidebarTab === item.value"
          @click="sidebarTab = item.value"
        >
          {{ item.label }}
        </button>
      </div>

      <section v-if="sidebarTab === 'collections'" class="http-sidebar-section http-sidebar-section--fills">
        <div class="http-collection-search">
          <AppSearchField
            v-model="collectionSearch"
            :placeholder="t('http.searchCollections')"
            :aria-label="t('http.searchCollections')"
          />
          <el-button
            native-type="button"
            class="http-sidebar-icon-button"
            :icon="Upload"
            :aria-label="t('http.importApiDocument')"
            :title="t('http.importApiDocument')"
            @click="importCollection()"
          />
          <el-dropdown trigger="click" @command="handleSidebarCreate">
            <el-button
              native-type="button"
              type="primary"
              class="http-sidebar-icon-button"
              :icon="Plus"
              :aria-label="t('http.createMenu')"
              :title="t('http.createMenu')"
            />
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="request">{{ t("http.newRequest") }}</el-dropdown-item>
                <el-dropdown-item command="collection">{{ t("http.newCollection") }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        <div v-if="collections.length" class="http-sidebar-section__header http-sidebar-section__header--static">
          <el-icon class="http-sidebar-section__icon"><FolderOpened /></el-icon>
          <span>{{ t("http.myCollections") }}</span>
          <b v-if="collections.length">{{ collections.length }}</b>
        </div>
        <el-scrollbar class="http-collection-list">
          <HttpCollectionTreeNode
            v-for="collection in visibleCollections"
            :key="collection.id"
            :collection="collection"
            :expanded-ids="visibleCollectionExpandedIds"
            :renaming-collection-id="renamingCollectionId"
            :renaming-request-id="renamingRequestId"
            :rename-value="renameValue"
            @toggle="toggleCollection"
            @update:rename-value="renameValue = $event"
            @rename-collection="startCollectionRename"
            @rename-request="startRequestRename"
            @save-rename="saveRename"
            @cancel-rename="cancelRename"
            @add-request="addRequestToCollection"
            @add-child="addCollection"
            @run="openCollectionRunner"
            @command="handleCollectionCommand"
            @open="openSavedRequest"
            @delete-request="deleteSavedRequest"
            @request-contextmenu="openRequestContextMenu"
            @drop="dropOnCollection"
          />
          <div v-if="visibleCollections.length === 0 && collectionSearch.trim()" class="http-sidebar-empty">
            {{ t("http.noSearchResults") }}
          </div>
          <div v-else-if="visibleCollections.length === 0" class="http-sidebar-onboarding">
            <strong>{{ t("http.collectionsEmptyTitle") }}</strong>
            <span>{{ t("http.collectionsEmpty") }}</span>
            <div>
              <el-button native-type="button" size="small" :icon="Plus" @click="addCollection()">
                {{ t("http.newCollection") }}
              </el-button>
              <el-button native-type="button" size="small" :icon="Upload" @click="importCollection()">
                {{ t("http.importShort") }}
              </el-button>
            </div>
          </div>
        </el-scrollbar>
      </section>

      <section v-else-if="sidebarTab === 'history'" class="http-sidebar-section http-sidebar-section--fills">
        <div class="http-collection-search">
          <AppSearchField
            v-model="historySearch"
            :placeholder="t('http.searchHistory')"
            :aria-label="t('http.searchHistory')"
          />
          <el-button
            native-type="button"
            class="http-sidebar-icon-button"
            :icon="Delete"
            :disabled="history.length === 0"
            :aria-label="t('http.clearHistory')"
            :title="t('http.clearHistory')"
            @click="clearHistory"
          />
        </div>
        <el-scrollbar class="http-history-list">
          <section v-for="group in historyGroups" :key="group.key" class="http-history-group">
            <header>
              <span>{{ historyGroupLabel(group) }}</span>
              <b>{{ group.items.length }}</b>
            </header>
            <div v-for="item in group.items" :key="item.id" class="http-history-item">
              <button
                type="button"
                class="http-history-item__open"
                :title="`${item.method} ${item.url}`"
                @click="openHistory(item)"
              >
                <span class="http-method" :class="`is-${item.method.toLowerCase()}`">{{ item.method }}</span>
                <span class="http-history-item__body">
                  <strong>{{ historyDisplayName(item, t("http.newRequest")) }}</strong>
                  <small>{{ historyUrlSummary(item.url) }}</small>
                </span>
                <span class="http-history-item__meta">
                  <i :class="`is-${historyStatusTone(item.status)}`">{{ item.status || "ERR" }}</i>
                  <small>{{ historyElapsed(item.elapsed) }} · {{ historyTime(item.createdAt) }}</small>
                </span>
              </button>
              <el-button
                text
                native-type="button"
                class="http-history-item__remove"
                :icon="Close"
                :aria-label="t('http.removeHistoryItem')"
                :title="t('http.removeHistoryItem')"
                @click="removeHistoryItem(item.id)"
              />
            </div>
          </section>
          <div v-if="history.length === 0" class="http-sidebar-empty">
            {{ t("http.historyEmpty") }}
          </div>
          <div v-else-if="historyGroups.length === 0" class="http-sidebar-empty">
            {{ t("http.noSearchResults") }}
          </div>
        </el-scrollbar>
      </section>

      <section v-else class="http-sidebar-section http-sidebar-section--fills">
        <el-scrollbar class="http-sidebar-env-list">
          <div v-for="collection in collections" :key="collection.id" class="http-sidebar-env-group">
            <header>
              <span>{{ collection.name }}</span>
              <el-button
                text
                circle
                native-type="button"
                :aria-label="t('http.manageCollectionEnvironments')"
                :title="t('http.manageCollectionEnvironments')"
                @click="handleCollectionCommand(collection, 'manage-environments')"
              >
                <el-icon><Setting /></el-icon>
              </el-button>
            </header>
            <button
              v-for="environment in collection.environments ?? []"
              :key="environment.id"
              type="button"
              class="http-sidebar-env"
              :class="{ 'is-active': environment.id === collection.activeEnvironmentId }"
              @click="collection.activeEnvironmentId = environment.id"
            >
              <i class="http-sidebar-env__dot" />
              <span class="http-sidebar-env__name">{{ environment.name }}</span>
              <small>{{ t("http.environmentVarCount", { count: enabledEnvironmentVarCount(environment) }) }}</small>
            </button>
          </div>
          <div v-if="collections.length === 0" class="http-sidebar-empty">
            {{ t("http.environmentsEmpty") }}
          </div>
        </el-scrollbar>
      </section>
        </aside>
      </template>
      <template #second>
        <main class="http-main-workspace">
      <div class="http-request-tabs-bar">
      <el-scrollbar ref="requestTabsScrollbar" class="http-request-tabs" @wheel="scrollRequestTabs">
        <div
          v-for="tab in tabs"
          :key="tab.id"
          class="http-request-tab"
          :class="{ 'is-active': activeTabId === tab.id }"
          @contextmenu.prevent="openTabContextMenu($event, tab.id)"
          @mousedown.middle.prevent="closeRequest(tab.id)"
        >
          <button
            type="button"
            class="http-request-tab__select"
            @click="activeTabId = tab.id"
          >
            <span class="http-method">{{ tab.method }}</span>
            <span>{{ tab.name }}</span>
            <i v-if="tab.loading" class="http-request-tab__status is-loading" />
            <i
              v-else-if="tab.response"
              class="http-request-tab__status"
              :class="tab.response.status < 400 ? 'is-ok' : 'is-error'"
            />
          </button>
          <el-button
            text
            native-type="button"
            class="http-request-tab__close"
            :aria-label="t('common.closeTab')"
            :title="t('common.closeTab')"
            @click="closeRequest(tab.id)"
          >
            <el-icon><Close /></el-icon>
          </el-button>
        </div>
      </el-scrollbar>
      <AppContextMenu
        :open="tabContextMenu.open"
        :x="tabContextMenu.x"
        :y="tabContextMenu.y"
        :width="196"
        @close="closeTabContextMenu"
      >
        <el-dropdown-item @click="handleTabContextCommand('rename')">{{ t("tab.rename") }}</el-dropdown-item>
        <el-dropdown-item @click="handleTabContextCommand('duplicate')">{{ t("tab.duplicate") }}</el-dropdown-item>
        <el-dropdown-item divided @click="handleTabContextCommand('copy-name')">{{ t("tab.copyName") }}</el-dropdown-item>
        <el-dropdown-item :disabled="!contextTab?.url.trim()" @click="handleTabContextCommand('copy-url')">
          {{ t("http.copyUrl") }}
        </el-dropdown-item>
        <el-dropdown-item :disabled="!contextTab?.url.trim()" @click="handleTabContextCommand('copy-curl')">
          {{ t("http.copyAsCurl") }}
        </el-dropdown-item>
        <el-dropdown-item divided @click="handleTabContextCommand('close')">{{ t("common.closeTab") }}</el-dropdown-item>
        <el-dropdown-item :disabled="tabs.length <= 1" @click="handleTabContextCommand('close-others')">
          {{ t("tab.closeOther") }}
        </el-dropdown-item>
        <el-dropdown-item :disabled="contextTabIndex <= 0" @click="handleTabContextCommand('close-left')">
          {{ t("tab.closeLeft") }}
        </el-dropdown-item>
        <el-dropdown-item :disabled="contextTabIndex >= tabs.length - 1" @click="handleTabContextCommand('close-right')">
          {{ t("tab.closeRight") }}
        </el-dropdown-item>
        <el-dropdown-item @click="handleTabContextCommand('close-all')">{{ t("tab.closeAll") }}</el-dropdown-item>
      </AppContextMenu>
      <AppContextMenu
        v-if="requestMenu"
        :open="true"
        :x="requestMenu.x"
        :y="requestMenu.y"
        :width="196"
        :aria-label="t('http.requestActions')"
        @close="requestMenu = null"
      >
        <el-dropdown-item @click="handleRequestMenuAction('open')">{{ t("http.openRequest") }}</el-dropdown-item>
        <el-dropdown-item @click="handleRequestMenuAction('rename')">{{ t("http.renameRequest") }}</el-dropdown-item>
        <el-dropdown-item divided @click="handleRequestMenuAction('notes-new')">{{ t("notes.link.newLinked") }}</el-dropdown-item>
        <el-dropdown-item @click="handleRequestMenuAction('notes-view')">
          {{ t("notes.link.viewLinked", { count: linkedNotes.countOf("http", requestMenu.requestId) }) }}
        </el-dropdown-item>
        <el-dropdown-item divided class="is-danger" @click="handleRequestMenuAction('delete')">
          {{ t("http.deleteRequest") }}
        </el-dropdown-item>
      </AppContextMenu>
        <el-button
          text
          native-type="button"
          class="http-request-tabs__add"
          :aria-label="t('http.newRequest')"
          :title="t('http.newRequest')"
          @click="addRequest()"
        >
          <el-icon><Plus /></el-icon>
        </el-button>
        <span class="http-request-tabs__tools">
          <el-dropdown trigger="click" @command="openConnectionTool">
            <el-button text native-type="button" class="http-request-tabs__connect">
              {{ t("http.connectionTools") }}
              <el-icon><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu class="http-connection-menu">
                <el-dropdown-item command="ws">
                  <span>WebSocket</span><small>WS</small>
                </el-dropdown-item>
                <el-dropdown-item command="sse">
                  <span>Server-Sent Events</span><small>SSE</small>
                </el-dropdown-item>
                <el-dropdown-item command="socketio">
                  <span>Socket.IO</span><small>v3/v4</small>
                </el-dropdown-item>
                <el-dropdown-item command="mock" divided>
                  <span>{{ t("http.mockServerTitle") }}</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <span class="http-tabs-env" :title="environmentSelectionHint">
            <el-select
              filterable
              v-model="requestEnvironmentSelection"
              size="small"
              class="http-tabs-env__select"
              :class="{ 'is-following': requestEnvironmentSelection === '__follow__' }"
              :disabled="!environmentOptionGroups.length"
              :placeholder="t('http.noEnvironment')"
              :aria-label="t('http.requestEnvironment')"
            >
              <template #prefix>
                <i class="http-tabs-env__dot" :class="{ 'is-empty': !activeEnvironment }" />
              </template>
              <template #label>
                <span class="http-tabs-env__label">{{ activeEnvironment?.name ?? t("http.noEnvironmentSelected") }}</span>
              </template>
              <el-option
                v-if="activeTopLevelCollection"
                :label="t('http.followCollectionEnvironment', { name: activeTopLevelCollection.environments?.find((item) => item.id === activeTopLevelCollection?.activeEnvironmentId)?.name ?? t('http.noEnvironment') })"
                value="__follow__"
              />
              <el-option v-else :label="t('http.noEnvironmentSelected')" value="__follow__" />
              <template v-if="activeTopLevelCollection">
                <el-option
                  v-for="environment in activeTopLevelCollection.environments ?? []"
                  :key="environment.id"
                  :label="environment.name"
                  :value="environment.id"
                />
              </template>
              <el-option-group
                v-for="group in activeTopLevelCollection ? [] : environmentOptionGroups"
                :key="group.id"
                :label="group.name"
              >
                <el-option
                  v-for="environment in group.environments"
                  :key="environment.id"
                  :label="environment.name"
                  :value="environment.id"
                />
              </el-option-group>
            </el-select>
            <el-popover
              v-model:visible="envOpen"
              placement="bottom-end"
              :width="380"
              popper-class="http-env-popover"
              :disabled="!activeEnvironment"
            >
              <template #reference>
                <el-button
                  text
                  circle
                  native-type="button"
                  class="http-tabs-env__vars"
                  :disabled="!activeEnvironment"
                  :aria-label="t('http.editResolvedEnvironment')"
                  :title="activeEnvironment ? t('http.editResolvedEnvironment') : environmentSelectionHint"
                >
                  <el-icon><Operation /></el-icon>
                  <b v-if="Object.keys(activeEnvironmentVariables).length">
                    {{ Object.keys(activeEnvironmentVariables).length }}
                  </b>
                </el-button>
              </template>
              <template v-if="activeEnvironment">
                <div class="http-env-popover__title">
                  <span>{{ t("http.resolvedEnvironment") }}</span>
                  <b>{{ activeEnvironment.name }}</b>
                </div>
                <el-scrollbar class="http-env-rows http-env-rows--popover">
                  <div v-for="row in activeEnvironment.vars" :key="row.id" class="http-env-row">
                    <el-checkbox v-model="row.enabled" />
                    <el-input v-model="row.key" :placeholder="t('http.variableKey')" />
                    <el-input v-model="row.value" :placeholder="t('http.variableValue')" />
                    <el-button
                      text
                      circle
                      native-type="button"
                      :aria-label="t('common.delete')"
                      :title="t('common.delete')"
                      @click="activeEnvironment.vars = activeEnvironment.vars.filter((item) => item.id !== row.id)"
                    >
                      <el-icon><Delete /></el-icon>
                    </el-button>
                  </div>
                  <el-button text native-type="button" class="http-add-row" @click="activeEnvironment.vars.push(emptyRow())">
                    <el-icon><Plus /></el-icon>{{ t("http.addVariable") }}
                  </el-button>
                </el-scrollbar>
              </template>
            </el-popover>
          </span>
        </span>
      </div>

      <AppSplitPane
        v-model="requestHeight"
        class="http-request-response"
        :class="{ 'is-response-pending': !responseVisible }"
        :resizable="responseVisible"
        layout="vertical"
        unit="percent"
        :min="28"
        :max="75"
        :second-min="responseVisible ? 120 : 44"
        :aria-label="t('http.resizeRequestResponse')"
        @resize-end="persistRequestHeight"
      >
        <template #first>
          <section class="http-request-panel" @keydown="handleRequestPanelKeydown">
          <div class="http-address-row">
            <div class="http-url-field">
              <el-select
                v-model="activeTab.method"
                class="http-method-select"
                :class="`is-${activeTab.method.toLowerCase()}`"
                :aria-label="t('http.method')"
              >
                <el-option
                  v-for="item in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']"
                  :key="item"
                  :value="item"
                />
              </el-select>
              <el-input
                v-model="activeTab.url"
                class="http-url-input"
                :placeholder="t('http.urlPlaceholder')"
                :aria-label="t('http.requestUrl')"
                @keyup.enter="send"
              />
            </div>
            <el-button
              v-if="activeTab.loading"
              class="http-send-button"
              :icon="CircleClose"
              @click="cancelSend()"
            >
              {{ t("http.cancelRequest") }}
            </el-button>
            <el-dropdown
              v-else
              split-button
              type="primary"
              class="http-send-button"
              :title="t('http.sendShortcut', { key: sendShortcutKey })"
              @click="send"
              @command="handleSendCommand"
            >
              <el-icon><Promotion /></el-icon>{{ t("http.send") }}
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="code" :disabled="!activeTab.url.trim()">
                    {{ t("http.generateCode") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="copy-curl" :disabled="!activeTab.url.trim()">
                    {{ t("http.copyAsCurl") }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-dropdown
              v-if="activeTab.savedRequestId"
              class="http-save-dropdown"
              :disabled="!activeTab.url.trim()"
              @command="openSaveRequestDialog($event as SaveRequestMode)"
            >
              <el-button class="http-address-action" :disabled="!activeTab.url.trim()">
                {{ t("http.saveRequest") }}
                <el-icon><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="save">{{ t("http.saveRequest") }}</el-dropdown-item>
                  <el-dropdown-item command="save-as">{{ t("http.saveAsRequest") }}</el-dropdown-item>
                  <el-dropdown-item command="move">{{ t("http.moveToCollection") }}</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-button
              v-else
              class="http-address-action"
              :disabled="!activeTab.url.trim()"
              @click="openSaveRequestDialog()"
            >
              {{ t("http.saveRequest") }}
            </el-button>
            <el-dropdown trigger="click" @command="handleRequestMoreCommand">
              <el-button
                class="http-address-action http-address-action--icon"
                :icon="MoreFilled"
                :aria-label="t('http.moreActions')"
                :title="t('http.moreActions')"
              />
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="import-curl" :icon="Upload">{{ t("http.importCurl") }}</el-dropdown-item>
                  <el-dropdown-item command="copy-curl" :icon="CopyDocument" :disabled="!activeTab.url.trim()">
                    {{ t("http.copyAsCurl") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="code" :icon="MagicStick" :disabled="!activeTab.url.trim()">
                    {{ t("http.generateCode") }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>

          <el-tabs v-model="activeTab.requestTab" class="http-config-tabs">
            <el-tab-pane
              v-for="tabName in ['params', 'headers', 'auth', 'body', 'documentation'] as const"
              :key="tabName"
              :name="tabName"
            >
              <template #label>
                {{ t(`http.${tabName}` as never) }}
                <small v-if="tabName === 'params' && activeTab.params.some((row) => row.key)">
                  {{ activeTab.params.filter((row) => row.key).length }}
                </small>
                <small v-if="tabName === 'headers' && activeTab.headers.some((row) => row.key)">
                  {{ activeTab.headers.filter((row) => row.key).length }}
                </small>
              </template>
            </el-tab-pane>
          </el-tabs>

          <el-scrollbar
            class="http-request-editor"
            :class="{
              'http-request-editor--fill':
                activeTab.requestTab === 'body' || activeTab.requestTab === 'documentation',
            }"
          >
            <template
              v-if="activeTab.requestTab === 'params' || activeTab.requestTab === 'headers'"
            >
              <div
                class="http-kv-head"
                :class="{ 'http-kv-head--params': activeTab.requestTab === 'params' }"
              >
                <span /><b>{{ t("http.enabled") }}</b
                ><b>{{ t("http.key") }}</b
                ><b v-if="activeTab.requestTab === 'params'">{{ t("http.parameterType") }}</b
                ><b>{{ t("http.value") }}</b
                ><b v-if="activeTab.requestTab === 'params'">{{ t("http.description") }}</b
                ><span />
              </div>
              <div
                v-for="(row, index) in activeTab[activeTab.requestTab]"
                :key="row.id"
                class="http-kv-row"
                :class="{ 'http-kv-row--params': activeTab.requestTab === 'params' }"
              >
                <el-checkbox v-model="row.enabled" />
                <el-select
                  v-if="activeTab.requestTab === 'headers'"
                  v-model="row.key"
                  filterable
                  allow-create
                  default-first-option
                  :placeholder="t('http.key')"
                >
                  <el-option
                    v-for="header in COMMON_HTTP_HEADER_NAMES"
                    :key="header"
                    :label="header"
                    :value="header"
                  />
                </el-select>
                <el-input v-else v-model="row.key" :placeholder="t('http.key')" />
                <el-select
                  v-if="activeTab.requestTab === 'params'"
                  :model-value="row.valueType"
                  :placeholder="t('http.parameterType')"
                  @update:model-value="updateParameterType(row, $event as HttpParameterType)"
                >
                  <el-option
                    v-for="parameterType in ['string', 'integer', 'number', 'boolean', 'array', 'object', 'file'] as const"
                    :key="parameterType"
                    :label="t(`http.parameterType.${parameterType}` as never)"
                    :value="parameterType"
                  />
                </el-select>
                <el-button
                  v-if="activeTab.requestTab === 'params' && row.valueType === 'file'"
                  native-type="button"
                  class="http-param-file"
                  :title="t('http.chooseUploadFile')"
                  @click="chooseParameterFile(row)"
                >
                  <el-icon><FolderOpened /></el-icon>
                  <span>{{ row.fileName || t("http.chooseFile") }}</span>
                </el-button>
                <el-input v-else v-model="row.value" :placeholder="t('http.value')" />
                <el-input
                  v-if="activeTab.requestTab === 'params'"
                  v-model="row.description"
                  :placeholder="t('http.description')"
                />
                <el-button
                  text
                  :icon="Delete"
                  :title="t('common.delete')"
                  @click="activeTab[activeTab.requestTab].splice(index, 1)"
                />
              </div>
              <el-button text native-type="button" class="http-add-row" @click="addRow(activeTab.requestTab)">
                <el-icon><Plus /></el-icon>{{ t("common.add") }}
              </el-button>
            </template>
            <div v-else-if="activeTab.requestTab === 'auth'" class="http-auth-editor">
              <el-select v-model="activeTab.authType" :placeholder="t('http.auth')">
                <el-option :label="t('http.noAuth')" value="none" />
                <el-option :label="t('http.bearerToken')" value="bearer" />
                <el-option :label="t('http.basicAuth')" value="basic" />
                <el-option :label="t('http.apiKey')" value="api-key" />
                <el-option :label="t('http.oauth2')" value="oauth2" />
              </el-select>
              <div v-if="activeTab.authType === 'none'" class="http-auth-editor__empty">
                {{ t("http.noAuthHint") }}
              </div>
              <el-input
                v-else-if="activeTab.authType === 'bearer'"
                v-model="activeTab.authToken"
                type="password"
                show-password
                :placeholder="t('http.token')"
              />
              <div v-else-if="activeTab.authType === 'basic'" class="http-auth-editor__fields">
                <el-input v-model="activeTab.authUsername" :placeholder="t('http.username')" />
                <el-input
                  v-model="activeTab.authPassword"
                  type="password"
                  show-password
                  :placeholder="t('http.password')"
                />
              </div>
              <div v-else-if="activeTab.authType === 'api-key'" class="http-auth-editor__fields">
                <el-input v-model="activeTab.apiKeyName" :placeholder="t('http.apiKeyName')" />
                <el-input
                  v-model="activeTab.apiKeyValue"
                  type="password"
                  show-password
                  :placeholder="t('http.apiKeyValue')"
                />
                <el-select v-model="activeTab.apiKeyAddTo">
                  <el-option :label="t('http.addToHeader')" value="header" />
                  <el-option :label="t('http.addToQuery')" value="query" />
                </el-select>
              </div>
              <div v-else class="http-auth-editor__oauth">
                <el-select v-model="activeTab.oauth2GrantType">
                  <el-option :label="t('http.oauth2.authorizationCode')" value="authorization-code" />
                  <el-option :label="t('http.oauth2.clientCredentials')" value="client-credentials" />
                  <el-option :label="t('http.oauth2.password')" value="password" />
                  <el-option :label="t('http.oauth2.implicit')" value="implicit" />
                </el-select>
                <el-input v-model="activeTab.oauth2AccessToken" type="password" show-password :placeholder="t('http.accessToken')" />
                <el-input v-model="activeTab.oauth2AuthUrl" :placeholder="t('http.authorizationUrl')" />
                <el-input v-model="activeTab.oauth2TokenUrl" :placeholder="t('http.tokenUrl')" />
                <el-input v-model="activeTab.oauth2ClientId" :placeholder="t('http.clientId')" />
                <el-input v-model="activeTab.oauth2ClientSecret" type="password" show-password :placeholder="t('http.clientSecret')" />
                <el-input v-model="activeTab.oauth2Scope" :placeholder="t('http.scope')" />
                <el-input v-model="activeTab.oauth2CallbackUrl" :placeholder="t('http.callbackUrl')" />
                <el-input v-if="activeTab.oauth2GrantType === 'password'" v-model="activeTab.oauth2Username" :placeholder="t('http.username')" />
                <el-input v-if="activeTab.oauth2GrantType === 'password'" v-model="activeTab.oauth2Password" type="password" show-password :placeholder="t('http.password')" />
                <small>{{ t("http.oauth2TokenHint") }}</small>
              </div>
            </div>
            <div v-else-if="activeTab.requestTab === 'documentation'" class="http-document-editor">
              <div class="http-document-editor__toolbar">
                <el-segmented
                  v-model="activeTab.documentMode"
                  :options="[
                    { label: t('http.documentEdit'), value: 'edit' },
                    { label: t('http.documentPreview'), value: 'preview' },
                  ]"
                />
              </div>
              <el-input
                v-if="activeTab.documentMode === 'edit'"
                v-model="activeTab.documentation"
                type="textarea"
                resize="none"
                :placeholder="t('http.documentPlaceholder')"
              />
              <MarkdownPreview
                v-else-if="activeTab.documentation"
                :content="activeTab.documentation"
              />
              <el-empty v-else :description="t('http.documentEmpty')" :image-size="42" />
            </div>
            <HttpBodyEditor
              v-else
              v-model:body-type="activeTab.bodyType"
              v-model:body="activeTab.body"
              v-model:form-body="activeTab.formBody"
              v-model:binary-file-path="activeTab.binaryFilePath"
              v-model:binary-file-name="activeTab.binaryFileName"
              v-model:graphql-query="activeTab.graphqlQuery"
              v-model:graphql-variables="activeTab.graphqlVariables"
            />
          </el-scrollbar>
          </section>
        </template>

        <template #second>
          <section class="http-response-panel">
          <div v-if="responseVisible" class="http-response-toolbar">
            <el-segmented
              v-model="activeTab.responseTab"
              class="http-response-tabs"
              :options="[
                { label: t('http.responseBody'), value: 'body' },
                { label: t('http.responseHeadersCount', { count: activeTab.response?.headers.length ?? 0 }), value: 'headers' },
              ]"
            />
            <div v-if="activeTab.response" class="http-response-meta">
              <span :class="activeTab.response.status < 400 ? 'is-ok' : 'is-error'">
                {{ activeTab.response.status }} {{ activeTab.response.status_text }}
              </span>
              <span>{{ activeTab.response.elapsed_ms }} ms</span>
              <span>{{ formatResponseSizeLabel(activeTab.response.size_bytes) }}</span>
              <el-button text size="small" :icon="MagicStick" @click="explainResponse">
                {{ t("ai.handoff.explainHttpResponse") }}
              </el-button>
            </div>
          </div>

          <template v-if="activeTab.response">
            <el-alert
              v-for="(note, index) in activeTab.response.browser_notes ?? []"
              :key="`browser-note-${index}`"
              class="http-response-browser-note"
              type="warning"
              :closable="false"
              show-icon
              :title="note"
            />
            <div
              v-if="activeTab.responseTab === 'body' && largeResponseUnformatted"
              class="http-response-large-notice"
            >
              <span>{{
                t("http.largeResponseUnformatted", {
                  size: formatResponseSizeLabel(activeTab.response.body.length),
                })
              }}</span>
              <el-button
                size="small"
                native-type="button"
                :loading="formattingLargeBody"
                @click="formatLargeResponseBody"
              >
                {{ t("http.formatResponse") }}
              </el-button>
            </div>
            <VirtualTextViewer
              v-if="activeTab.responseTab === 'body'"
              class="http-response-body"
              :text="prettyBody"
              :label="t('http.responseBody')"
            />
            <el-scrollbar v-else class="http-response-headers">
              <div class="http-response-headers__head">
                <b>{{ t("http.key") }}</b
                ><b>{{ t("http.value") }}</b>
              </div>
              <div
                v-for="([key, value], index) in activeTab.response.headers"
                :key="`${key}-${index}`"
              >
                <span>{{ key }}</span
                ><span>{{ value }}</span>
              </div>
            </el-scrollbar>
          </template>
          <div v-else-if="activeTab.loading" class="http-response-empty">
            <i class="http-request-tab__status is-loading" />
            <span>{{ t("http.responseLoading") }}</span>
          </div>
          <div v-else class="http-response-pending">
            <span>{{ t("http.noResponseHint") }}</span>
            <span class="http-response-pending__keys">
              {{ t("http.send") }}<kbd>{{ sendShortcutKey }}</kbd><kbd>↵</kbd>
            </span>
          </div>
          </section>
        </template>
      </AppSplitPane>
        </main>
      </template>
    </AppSplitPane>
  </section>
  <el-dialog
    v-model="environmentManagerVisible"
    :title="t('http.manageCollectionEnvironmentsTitle', { name: environmentManagerCollection?.name ?? '' })"
    width="min(720px, 94vw)"
    append-to-body
    top="4vh"
    class="http-environment-manager http-fit-dialog"
  >
    <div class="http-environment-manager__intro">
      <p class="http-environment-manager__hint">{{ t("http.collectionEnvironmentHint") }}</p>
      <el-button type="primary" plain :icon="Plus" @click="addEnvironment">
        {{ t("http.addEnvironment") }}
      </el-button>
    </div>
    <el-scrollbar class="http-environment-manager__list" max-height="min(560px, 60vh)">
      <section
        v-for="environment in environmentManagerCollection?.environments ?? []"
        :key="environment.id"
        class="http-environment-manager__item"
        :class="{ 'is-active': environment.id === environmentManagerCollection?.activeEnvironmentId }"
      >
        <header class="http-environment-manager__item-header">
          <el-input
            v-model="environment.name"
            class="http-environment-manager__name"
            :placeholder="t('http.environmentName')"
            :aria-label="t('http.environmentName')"
            maxlength="80"
          />
          <el-tag
            v-if="environment.id === environmentManagerCollection?.activeEnvironmentId"
            class="http-environment-manager__active-tag"
            type="primary"
            size="small"
            disable-transitions
          >
            {{ t("http.collectionActiveEnvironment") }}
          </el-tag>
          <el-button
            v-else
            class="http-environment-manager__set-active"
            text
            size="small"
            @click="environmentManagerCollection!.activeEnvironmentId = environment.id"
          >
            {{ t("http.setActiveEnvironment") }}
          </el-button>
          <el-button
            class="http-environment-manager__delete"
            text
            :icon="Delete"
            :disabled="(environmentManagerCollection?.environments?.length ?? 0) <= 1"
            :aria-label="t('common.delete')"
            :title="t('common.delete')"
            @click="deleteEnvironment(environment)"
          />
        </header>
        <div class="http-environment-manager__variables">
          <div v-if="environment.vars.length" class="http-env-row http-env-row--head">
            <span /><b>{{ t("http.variableKey") }}</b><b>{{ t("http.variableValue") }}</b><span />
          </div>
          <div v-for="row in environment.vars" :key="row.id" class="http-env-row">
            <el-checkbox v-model="row.enabled" />
            <el-input v-model="row.key" :placeholder="t('http.variableKey')" />
            <el-input v-model="row.value" :placeholder="t('http.variableValue')" />
            <el-button
              text
              :icon="Delete"
              :aria-label="t('common.delete')"
              :title="t('common.delete')"
              @click="environment.vars = environment.vars.filter((item) => item.id !== row.id)"
            />
          </div>
          <el-button text class="http-add-row" :icon="Plus" @click="environment.vars.push(emptyRow())">
            {{ t("http.addVariable") }}
          </el-button>
        </div>
      </section>
    </el-scrollbar>
    <template #footer>
      <el-button type="primary" @click="environmentManagerVisible = false">{{ t("common.confirm") }}</el-button>
    </template>
  </el-dialog>
  <el-dialog v-model="saveRequestVisible" :title="saveRequestDialogTitle" width="min(420px, 92vw)" append-to-body>
    <el-tree-select
      v-model="saveTargetCollectionId"
      class="http-save-request__select"
      :data="collectionTreeOptions"
      :placeholder="t('http.selectCollection')"
      check-strictly
      default-expand-all
    />
    <el-button text native-type="button" class="http-save-request__new" @click="addCollection().then((collection) => { if (collection) saveTargetCollectionId = collection.id })">
      <el-icon><Plus /></el-icon>{{ t("http.newCollection") }}
    </el-button>
    <template #footer>
      <el-button @click="saveRequestVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!saveTargetCollectionId" @click="saveCurrentRequest">
        {{ saveRequestMode === "move" ? t("http.moveToCollection") : t("common.save") }}
      </el-button>
    </template>
  </el-dialog>
  <HttpCurlImportDialog v-model="curlImportVisible" @import="importCurlRequest" />
  <HttpCodeGeneratorDialog v-model="codeGeneratorVisible" :request="activeTab" />
  <HttpCollectionRunnerDialog
    v-model="collectionRunnerVisible"
    :collection="runningCollection"
  />
  <HttpMockServerPanel v-model="mockServerVisible" />
  <HttpStreamPanel v-model="streamPanelVisible" :kind="streamPanelKind" />
  <HttpSocketIOPanel v-model="socketIOPanelVisible" />
</template>
