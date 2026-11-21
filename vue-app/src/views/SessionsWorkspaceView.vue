<script setup lang="ts">
import { createFrameMapBatcher } from "@/domain/async/frame-map-batcher";
import "@/styles/workspaces/sessions/workspace.css";
import { computed, defineAsyncComponent, getCurrentScope, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch, type Component, type ComputedRef } from "vue";
import {
  ChatLineSquare,
  Close,
  CopyDocument,
  Cpu,
  Delete,
  Document,
  EditPen,
  Finished,
  FolderOpened,
  MagicStick,
  Monitor,
  Moon,
  MoreFilled,
  Plus,
  Refresh,
  Search,
  Setting,
  Sunny,
  TopRight,
} from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox, type ScrollbarInstance } from "element-plus";
import { useRoute, useRouter } from "vue-router";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { parseSessionDeepLink, type SessionProviderId } from "@/domain/ai/session-deep-link";
import {
  buildSessionToolNewCommand,
  buildSessionToolResumeCommand,
  createDefaultSessionToolPrefs,
  customToolBackendConfig,
  enabledSessionToolIds,
  envPrefix,
  findCustomSessionTool,
  isBuiltinSessionToolId,
  quoteShellArgument,
  sessionToolClassSuffix,
  sessionToolEnv,
  type CustomSessionBackendConfig,
  type SessionToolPrefs,
} from "@/domain/ai/session-tools";
import {
  SESSION_TOOLS_EVENT,
  applySessionToolRoots,
  loadSessionToolPrefs,
} from "@/services/session-tool-preferences";
import {
  earlierAiMessageRenderStart,
  nextAiMessageRenderStart,
} from "@/domain/ai/message-render-window";
import {
  useWorkspaceScopedPersistence,
  type CapturedWorkspaceStateScope,
} from "@/composables/use-workspace-state-scope";
import SessionLiveTabActivity from "@/components/sessions/SessionLiveTabActivity.vue";
import TerminalAttentionMarker from "@/components/terminal/TerminalAttentionMarker.vue";
import { strongestAttention, withCodexNotifications } from "@/domain/terminal/terminal-attention";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { ROW_GROUP_ATTRIBUTE, useGroupedRowWindow } from "@/composables/use-grouped-row-window";
import { useVueI18n } from "@/i18n/messages";
import { chooseNativeDirectory, invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import {
  safeTerminalTaskLabel,
  shouldApplyTerminalActivity,
  type TerminalActivityPhase,
  type TerminalPaneActivity,
} from "@/domain/terminal/terminal-activity";
import type { LocalTerminalActivity } from "@/components/terminal/LocalTerminal.vue";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import {
  isTerminalLeafPane,
  type LocalTerminalPane,
  type TerminalPaneNode,
  type TerminalSplitDirection,
} from "@/domain/terminal/types";
import { replaceProfilePaneWithDefaultShell } from "@/domain/terminal/profiles";
import { deleteTerminalSnapshot } from "@/services/terminal-session-cache";
import type { PluginSessionMessage, PluginSessionSource, PluginSessionSummary } from "@/domain/plugins/session-sources";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { providerErrorText } from "@/services/plugin-provider-invoke";
import { loadPluginSessionMessages } from "@/services/plugin-session-sources";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";

// Markdown and live terminals are rendered on demand; loading them lazily keeps them out of this view's startup chunk.
const MarkdownPreview = defineAsyncComponent(() => import("@/components/ui/MarkdownPreview.vue"));
const TerminalPaneLayout = defineAsyncComponent(() => import("@/components/terminal/TerminalPaneLayout.vue"));
const SessionToolSettingsDialog = defineAsyncComponent(
  () => import("@/components/sessions/SessionToolSettingsDialog.vue"),
);
// Plugin session sources (contributes.sessionSources): rendered only when a plugin contributes one.
const PluginSessionSources = defineAsyncComponent(() => import("@/components/sessions/PluginSessionSources.vue"));
const PluginSessionTranscript = defineAsyncComponent(() => import("@/components/sessions/PluginSessionTranscript.vue"));

const SESSION_MESSAGE_LIMIT = 200;

interface ClaudeProject {
  encodedDir: string;
  decodedPath: string;
  sessionCount: number;
  lastAt: number;
}

interface Session {
  id?: string;
  uuid?: string;
  title: string;
  cwd?: string;
  decodedPath?: string;
  encodedDir?: string;
  lastAt: number;
  messageCount: number;
  model?: string;
  filePath?: string;
  status?: string;
  isNew?: boolean;
}

interface SessionMessage {
  role?: string;
  content?: string;
  model?: string;
  timestamp?: string;
  msgType?: string;
}

// 内置工具 id、`custom:<slug>` 形式的自定义工具 id，或 `plugin:<pluginId>/<sourceId>` 形式的插件来源。
type ProviderId = string;

const PLUGIN_PROVIDER_PREFIX = "plugin:";

interface ProviderDefinition {
  id: ProviderId;
  label: string;
  /** 内置工具通过列表接口的 adapter；自定义工具为 "custom"，claude/codex 走专用接口。 */
  adapter?: string;
  custom?: CustomSessionBackendConfig;
  mark: string;
  navIcon: Component;
}

const BUILTIN_PROVIDER_META: Record<
  SessionProviderId,
  { labelKey: `sessions.provider.${SessionProviderId}`; mark: string; navIcon: Component; adapter?: string }
> = {
  claude: { labelKey: "sessions.provider.claude", mark: "CC", navIcon: MagicStick },
  pi: { labelKey: "sessions.provider.pi", adapter: "pi", mark: "PI", navIcon: ChatLineSquare },
  codex: { labelKey: "sessions.provider.codex", mark: "CX", navIcon: Cpu },
  gemini: { labelKey: "sessions.provider.gemini", adapter: "gemini", mark: "GM", navIcon: Sunny },
  opencode: { labelKey: "sessions.provider.opencode", adapter: "opencode", mark: "OC", navIcon: Monitor },
  kimi: { labelKey: "sessions.provider.kimi", adapter: "kimi", mark: "KM", navIcon: Moon },
  qodercn: { labelKey: "sessions.provider.qodercn", adapter: "qodercn", mark: "QC", navIcon: ChatLineSquare },
  qoder: { labelKey: "sessions.provider.qoder", adapter: "qoder", mark: "QD", navIcon: Monitor },
  grok: { labelKey: "sessions.provider.grok", adapter: "grok", mark: "GK", navIcon: Cpu },
};

interface ProviderProjectGroup {
  key: string;
  path: string;
  sessions: Session[];
  lastAt: number;
}

interface ProjectContextMenuState {
  project: ClaudeProject;
  x: number;
  y: number;
}

interface SelectedSession {
  provider: ProviderId;
  session: Session;
}

interface ClaudeActivityEvent {
  paneId: string;
  phase: TerminalActivityPhase;
  source: "claude-hook";
  summary?: string;
  autoLabel?: string;
  updatedAt: number;
}

interface ClaudeActivityPreparation {
  enhanced: boolean;
  settingsPath?: string;
  extraEnv?: Record<string, string>;
}

interface PreparedLivePane {
  pane: LocalTerminalPane;
  command: string;
}

interface LiveSessionTerminal {
  key: string;
  terminalId: string;
  provider: ProviderId;
  session: Session;
  label: string;
  autoLabel: string;
  manualLabelOverride: boolean;
  createdAt: number;
  cwd?: string;
  initialCommand: string;
  paneRoot: TerminalPaneNode;
  activePaneId: string;
  paneActivities: Record<string, TerminalPaneActivity>;
}

type SessionPanel = "conversation" | "terminal";
/**
 * 终端标签的可序列化元数据。PTY 无法跨进程存活，重启后只恢复标签身份
 * （名称、图标、会话、工作目录），落回原目录的空白 shell。
 */
interface PersistedLiveTerminal {
  key: string;
  provider: ProviderId;
  session: Session;
  label: string;
  autoLabel: string;
  manualLabelOverride: boolean;
  createdAt: number;
  cwd?: string;
}

interface PersistedLiveTerminals {
  version: 1;
  activeKey: string;
  terminals: PersistedLiveTerminal[];
}

const { t, localeChain } = useVueI18n();
const route = useRoute();
const router = useRouter();
const workbench = useWorkbenchStore();
const terminalAttention = useTerminalAttentionStore();
const terminalWorkspace = useTerminalWorkspaceStore();
const assistant = useAiAssistantStore();
const liveTerminalsPersistence = useWorkspaceScopedPersistence<PersistedLiveTerminals>(
  "app_sessions_live_terminals_v1",
  { migrateLegacy: false }
);
const workspaceElement = ref<HTMLElement | null>(null);
const messageListElement = ref<ScrollbarInstance | null>(null);
const search = ref("");
const projects = ref<ClaudeProject[]>([]);
const sessionsByProvider = reactive<Record<ProviderId, Session[]>>({});
const projectSessions = reactive<Record<string, Session[]>>({});
// Project loads that land in one frame (several projects expanded in a row) commit together:
// every commit re-renders this whole view.
const projectSessionsBatch = createFrameMapBatcher<Session[]>((entries) => {
  for (const [encodedDir, sessions] of entries) projectSessions[encodedDir] = sessions;
});
const openProviders = reactive<Record<ProviderId, boolean>>({});
const openProjects = reactive<Record<string, boolean>>({});
const openProviderProjects = reactive<Record<string, boolean>>({});
const providerLoading = reactive<Record<ProviderId, boolean>>({});
const toolPrefs = ref<SessionToolPrefs>(createDefaultSessionToolPrefs());
// 后端展开 ~ 后的数据目录覆盖，用于给启动的 CLI 注入对应环境变量。
const resolvedRoots = ref<Record<string, string>>({});
const toolSettingsOpen = ref(false);
// 对话框首次打开后才挂载，之后保留实例以保证关闭动画。
const toolSettingsMounted = ref(false);
// 配置读到之前不渲染工具列表，避免已停用的工具闪现。
const toolPrefsLoaded = ref(false);
let toolPrefsSequence = 0;
let resolveToolPrefsReady: () => void = () => {};
const toolPrefsReady = new Promise<void>((resolve) => {
  resolveToolPrefsReady = resolve;
});
const providers = computed<ProviderDefinition[]>(() =>
  !toolPrefsLoaded.value ? [] : enabledSessionToolIds(toolPrefs.value).flatMap((id): ProviderDefinition[] => {
    if (isBuiltinSessionToolId(id)) {
      const meta = BUILTIN_PROVIDER_META[id];
      return [{ id, label: t(meta.labelKey), adapter: meta.adapter, mark: meta.mark, navIcon: meta.navIcon }];
    }
    const tool = findCustomSessionTool(toolPrefs.value, id);
    if (!tool) return [];
    return [{
      id,
      label: tool.name,
      adapter: "custom",
      custom: customToolBackendConfig(tool),
      mark: tool.mark,
      navIcon: Document,
    }];
  }),
);
const selected = ref<SelectedSession>();
const messages = ref<SessionMessage[]>([]);
const messageLoading = ref(false);
// Long transcripts render only their tail; earlier turns are revealed in batches.
const visibleMessageStart = ref(0);
const visibleMessages = computed(() =>
  messages.value.slice(visibleMessageStart.value).map((message, offset) => ({
    message,
    index: visibleMessageStart.value + offset,
  })),
);
const refreshing = ref(false);
const activePanel = ref<SessionPanel>("conversation");
const liveTerminals = ref<LiveSessionTerminal[]>([]);
const activeLiveTerminalKey = ref("");
const projectContextMenu = ref<ProjectContextMenuState | null>(null);
// One actions menu for every session row, anchored at the row's "⋯" button. A full
// el-dropdown per row (1000 rows with ten projects expanded) froze expanding for ~0.8 s.
const sessionMenu = ref<{ provider: ProviderId; session: Session; x: number; y: number } | null>(null);
const tabBarScrollEl = ref<HTMLElement | null>(null);
let messageLoadSequence = 0;
// Plugin sources: lists live in PluginSessionSources; the open transcript lives here, in memory only.
const pluginDataProviders = usePluginDataProvidersStore();
const pluginSessionSources = computed(() =>
  pluginDataProviders.sessionSources.filter((source) => source.activeInWorkspace),
);
const pluginRefreshToken = ref(0);
const pluginMessages = ref<PluginSessionMessage[]>([]);
const pluginMessagesTruncated = ref(false);
const pluginMessageError = ref("");
const selectedPluginSession = ref<PluginSessionSummary>();
let pluginMessageController: AbortController | undefined;
let reloadPluginMessagesOnActivate = false;
let liveTerminalSequence = 0;
// 终端标签写回的工作区作用域在恢复时固定，切换工作区不会把旧标签写进新作用域。
let liveTerminalsScope: CapturedWorkspaceStateScope | undefined;

const {
  size: navigatorWidth,
  collapsed: navigatorCollapsed,
  hydrate: hydrateNavigatorLayout,
  persist: persistNavigatorLayout,
} = useResizablePane({
  sizeKey: "app_sessions_sidebar_width",
  collapsedKey: "app_sessions_sidebar_collapsed",
  defaultSize: 380,
  minSize: 252,
  maxSize: (workspaceWidth) => Math.max(320, Math.min(440, workspaceWidth * 0.3)),
  containerRef: workspaceElement,
});

function persistNavigatorWidth(value: number) {
  navigatorWidth.value = value;
  void persistNavigatorLayout();
}

function updateNavigatorCollapsed(value: boolean) {
  navigatorCollapsed.value = value;
  void persistNavigatorLayout();
}
useToggleSidebarCommand(() => navigatorCollapsed.value, updateNavigatorCollapsed);

const normalizedQuery = computed(() => search.value.trim().toLocaleLowerCase());
const selectedPath = computed(
  () => selected.value?.session.decodedPath || selected.value?.session.cwd || ""
);
const selectedSessionId = computed(() => (selected.value ? sessionId(selected.value.session) : ""));
const selectedIsNew = computed(() => selected.value?.session.isNew === true);
const selectedLaunchCommand = computed(() =>
  selected.value
    ? buildLaunchCommand(selected.value.provider, selectedSessionId.value, selectedPath.value)
    : ""
);
// 复制到外部终端的命令需要带上数据目录环境变量；内嵌终端通过 extraEnv 注入。
const selectedCopyLaunchCommand = computed(() =>
  selected.value && selectedLaunchCommand.value
    ? `${envPrefix(providerEnv(selected.value.provider))}${selectedLaunchCommand.value}`
    : ""
);
const selectedFullCommand = computed(() =>
  selected.value ? buildFullCommand(selectedPath.value, selectedCopyLaunchCommand.value) : ""
);

function providerDefinition(provider: ProviderId) {
  return providers.value.find((item) => item.id === provider);
}

function isPluginProvider(provider: ProviderId) {
  return provider.startsWith(PLUGIN_PROVIDER_PREFIX);
}

function pluginSourceFor(provider: ProviderId) {
  return isPluginProvider(provider) ? pluginSessionSources.value.find((source) => source.key === provider) : undefined;
}

const selectedIsPlugin = computed(() => Boolean(selected.value && isPluginProvider(selected.value.provider)));
const selectedPluginMeta = computed(() => {
  const session = selectedPluginSession.value;
  if (!session) return "";
  const parts = [session.detail || session.model || ""];
  if (session.messageCount !== undefined) parts.push(`${session.messageCount} ${t("sessions.messageCount")}`);
  if (session.lastAt !== undefined) parts.push(formatTime(session.lastAt));
  return parts.filter(Boolean).join(" · ");
});

function isKnownProvider(provider: unknown): provider is ProviderId {
  return (
    typeof provider === "string" &&
    !isPluginProvider(provider) &&
    (isBuiltinSessionToolId(provider) || Boolean(findCustomSessionTool(toolPrefs.value, provider)))
  );
}

function providerLabel(provider: ProviderDefinition | ProviderId) {
  if (typeof provider !== "string") return provider.label;
  if (isBuiltinSessionToolId(provider)) return t(BUILTIN_PROVIDER_META[provider].labelKey);
  const pluginSource = pluginSourceFor(provider);
  if (pluginSource) return resolvePluginTitle(pluginSource, localeChain.value);
  return findCustomSessionTool(toolPrefs.value, provider)?.name ?? provider;
}

function providerMark(provider: ProviderId) {
  if (isBuiltinSessionToolId(provider)) return BUILTIN_PROVIDER_META[provider].mark;
  const pluginSource = pluginSourceFor(provider);
  if (pluginSource) return pluginSource.mark;
  return (
    findCustomSessionTool(toolPrefs.value, provider)?.mark ??
    provider.replace(/^custom:/, "").slice(0, 2).toUpperCase()
  );
}

function providerClass(provider: ProviderId) {
  return `is-${sessionToolClassSuffix(provider)}`;
}

function providerEnv(provider: ProviderId) {
  return sessionToolEnv(provider, resolvedRoots.value);
}

function providerBackendArgs(provider: ProviderId): { adapter: string; custom?: CustomSessionBackendConfig } {
  const tool = findCustomSessionTool(toolPrefs.value, provider);
  return tool ? { adapter: "custom", custom: customToolBackendConfig(tool) } : { adapter: provider };
}

function normalizeFilesystemPath(value: string | undefined) {
  const normalized = value?.trim().replace(/\\/g, "/").replace(/\/+$/, "") ?? "";
  if (!normalized) return "";
  return /^[A-Za-z]:\//.test(normalized) ? normalized.toLocaleLowerCase() : normalized;
}

function sessionMatches(session: Session) {
  if (!normalizedQuery.value) return true;
  return [session.title, session.cwd, session.decodedPath, session.model, session.id, session.uuid]
    .filter(Boolean)
    .join(" ")
    .toLocaleLowerCase()
    .includes(normalizedQuery.value);
}

function projectMatches(project: ClaudeProject) {
  return (
    (!normalizedQuery.value ||
      project.decodedPath.toLocaleLowerCase().includes(normalizedQuery.value) ||
      (projectSessions[project.encodedDir] ?? []).some(sessionMatches))
  );
}

function providerProjectGroups(provider: ProviderId) {
  const groups = new Map<string, ProviderProjectGroup>();
  for (const session of (sessionsByProvider[provider] ?? [])) {
    const path = (session.decodedPath || session.cwd || "").trim();
    const pathKey = normalizeFilesystemPath(path) || "__unknown__";
    const key = `${provider}:${pathKey}`;
    const existing = groups.get(key);
    if (existing) {
      existing.sessions.push(session);
      existing.lastAt = Math.max(existing.lastAt, session.lastAt);
    } else {
      groups.set(key, { key, path, sessions: [session], lastAt: session.lastAt });
    }
  }
  return [...groups.values()].sort((left, right) => right.lastAt - left.lastAt);
}

function providerProjectMatches(group: ProviderProjectGroup) {
  if (!normalizedQuery.value) return true;
  return (
    group.path.toLocaleLowerCase().includes(normalizedQuery.value) ||
    group.sessions.some(sessionMatches)
  );
}

function visibleProjectSessions(group: ProviderProjectGroup) {
  if (group.path.toLocaleLowerCase().includes(normalizedQuery.value)) return group.sessions;
  return group.sessions.filter(sessionMatches);
}

function providerProjectLabel(group: ProviderProjectGroup) {
  return shortPath(group.path) || t("sessions.unknownProject");
}

function providerSessionCount(id: ProviderId) {
  return id === "claude"
    ? projects.value.reduce((count, project) => count + project.sessionCount, 0)
    : (sessionsByProvider[id] ?? []).length;
}

// The sidebar's filtering and grouping are cached per provider so unrelated
// workspace updates do not re-normalize every session path on each render.
const providerSessionCounts = computed(() => Object.fromEntries(
  providers.value.map((provider) => [provider.id, providerSessionCount(provider.id)]),
) as Record<ProviderId, number>);
const visibleClaudeProjects = computed(() => projects.value.filter(projectMatches));
const visibleClaudeProjectSessions = computed(() => Object.fromEntries(
  visibleClaudeProjects.value.map((project) => [
    project.encodedDir,
    (projectSessions[project.encodedDir] ?? []).filter(sessionMatches),
  ]),
) as Record<string, Session[]>);
type VisibleProviderProjectGroup = ProviderProjectGroup & { visibleSessions: Session[] };
// 工具列表可配置，分组缓存按需为每个工具建立，并挂在组件作用域上随视图销毁。
const providerGroupScope = getCurrentScope();
const visibleProviderProjectGroupsById = new Map<ProviderId, ComputedRef<VisibleProviderProjectGroup[]>>();

function visibleProviderProjectGroups(id: ProviderId) {
  let groups = visibleProviderProjectGroupsById.get(id);
  if (!groups) {
    const create = () => computed(() => providerProjectGroups(id)
      .filter(providerProjectMatches)
      .map((group) => ({ ...group, visibleSessions: visibleProjectSessions(group) })));
    groups = providerGroupScope?.run(create) ?? create();
    visibleProviderProjectGroupsById.set(id, groups);
  }
  return groups.value;
}

// Many sessions render only the sidebar rows near the viewport (see use-grouped-row-window).
// Group keys, in render order: "claude:<encodedDir>" for Claude projects, then each other
// provider's "<provider>:<path>" project groups.
const sessionListScroller = ref<{ wrapRef?: HTMLElement }>();
const sessionRows = useGroupedRowWindow({
  scroller: () => sessionListScroller.value?.wrapRef,
  counts: () => {
    const counts: Record<string, number> = {};
    for (const provider of providers.value) {
      if (!openProviders[provider.id]) continue;
      if (provider.id === "claude") {
        for (const project of visibleClaudeProjects.value) {
          counts[`claude:${project.encodedDir}`] = openProjects[project.encodedDir] ? (visibleClaudeProjectSessions.value[project.encodedDir]?.length ?? 0) : 0;
        }
      } else {
        for (const group of visibleProviderProjectGroups(provider.id)) {
          counts[group.key] = openProviderProjects[group.key] ? group.visibleSessions.length : 0;
        }
      }
    }
    return counts;
  },
  rowSelector: ".sessions-session-row",
  estimatedRowHeight: 58,
});

function sessionId(session: Session) {
  return session.uuid ?? session.id ?? session.filePath ?? session.title;
}

function sessionKey(provider: ProviderId, session: Session) {
  return `${provider}:${sessionId(session)}`;
}

function liveTerminalKey(provider: ProviderId, session: Session) {
  return `${sessionKey(provider, session)}:${session.decodedPath || session.cwd || ""}`;
}

function nextLiveTerminalId() {
  // A session terminal survives view switching while it is mounted. Do not derive
  // its native PTY ID from the session key: after a route/HMR remount that would
  // attach to an old shell, and LocalTerminal correctly skips the initial resume
  // command for an attached shell. A fresh live entry must therefore have a fresh
  // PTY ID; the semantic key below still prevents duplicate terminals in this view.
  liveTerminalSequence += 1;
  return `cc-live-${Date.now().toString(36)}-${liveTerminalSequence.toString(36)}`;
}

function createLiveLocalPane(
  id: string,
  cwd?: string,
  initialCommand?: string,
  extraEnv?: Record<string, string>,
): LocalTerminalPane {
  return { id, type: "local", shellType: "system", initialCwd: cwd, initialCommand, extraEnv };
}

function withClaudeSettings(command: string, settingsPath?: string) {
  if (!settingsPath || !command.trim().startsWith("claude")) return command;
  const settings = quoteShellArgument(settingsPath);
  return command.replace(/^claude\b/, `claude --settings ${settings}`);
}

async function prepareLivePane(
  provider: ProviderId,
  id: string,
  cwd: string | undefined,
  requestedCommand: string,
): Promise<PreparedLivePane> {
  const env = providerEnv(provider);
  const toolEnv = Object.keys(env).length > 0 ? env : undefined;
  // Codex (built in or a custom tool running it) reports "done"/"needs approval" to the terminal.
  const initialCommand = withCodexNotifications(requestedCommand);
  if (provider !== "claude" || !isDesktopRuntime()) {
    return { pane: createLiveLocalPane(id, cwd, initialCommand, toolEnv), command: initialCommand };
  }
  try {
    const preparation = await invokeBackend<ClaudeActivityPreparation>(
      "terminal_activity_prepare_claude",
      { payload: { paneId: id } },
    );
    const command = preparation.enhanced
      ? withClaudeSettings(initialCommand, preparation.settingsPath)
      : initialCommand;
    const extraEnv = toolEnv || preparation.extraEnv ? { ...toolEnv, ...preparation.extraEnv } : undefined;
    return {
      pane: createLiveLocalPane(id, cwd, command, extraEnv),
      command,
    };
  } catch {
    return { pane: createLiveLocalPane(id, cwd, initialCommand, toolEnv), command: initialCommand };
  }
}

function splitPaneInTree(
  node: TerminalPaneNode,
  paneId: string,
  direction: TerminalSplitDirection,
  newPane: LocalTerminalPane
): TerminalPaneNode {
  if (isTerminalLeafPane(node)) {
    if (node.id !== paneId) return node;
    return { id: nextLiveTerminalId(), type: "split", direction, children: [node, newPane], sizes: [0.5, 0.5] };
  }
  const directIndex = node.children.findIndex((c) => isTerminalLeafPane(c) && c.id === paneId);
  if (node.direction === direction && directIndex >= 0) {
    const children = [...node.children];
    children.splice(directIndex + 1, 0, newPane);
    const raw = node.sizes.length === node.children.length ? [...node.sizes] : node.children.map(() => 1 / node.children.length);
    const half = raw[directIndex] / 2;
    raw.splice(directIndex, 1, half, half);
    const total = raw.reduce((a, b) => a + b, 0);
    return { ...node, children, sizes: raw.map((s) => s / total) };
  }
  const children = node.children.map((c) => splitPaneInTree(c, paneId, direction, newPane));
  return children.every((c, i) => c === node.children[i]) ? node : { ...node, children };
}

function removePaneFromTree(node: TerminalPaneNode, paneId: string): TerminalPaneNode | null {
  if (isTerminalLeafPane(node)) return node.id === paneId ? null : node;
  const results = node.children.map((c) => removePaneFromTree(c, paneId));
  const children = results.filter((c): c is TerminalPaneNode => c !== null);
  if (children.length === 0) return null;
  if (children.length === 1) return children[0];
  if (results.every((c, i) => c === node.children[i])) return node;
  const sizes = node.sizes.filter((_, i) => results[i] !== null);
  const total = sizes.reduce((a, b) => a + b, 0);
  return { ...node, children, sizes: sizes.map((s) => s / total) };
}

function collectLeafIds(node: TerminalPaneNode): string[] {
  return isTerminalLeafPane(node) ? [node.id] : node.children.flatMap(collectLeafIds);
}

function updateSplitSizes(node: TerminalPaneNode, splitId: string, sizes: number[]): TerminalPaneNode {
  if (isTerminalLeafPane(node)) return node;
  if (node.id === splitId) return { ...node, sizes };
  const children = node.children.map((c) => updateSplitSizes(c, splitId, sizes));
  return children.every((c, i) => c === node.children[i]) ? node : { ...node, children };
}

async function splitSessionPane(terminalKey: string, paneId: string, direction: TerminalSplitDirection) {
  const entry = liveTerminals.value.find((item) => item.key === terminalKey);
  if (!entry) return;
  const paneIdValue = nextLiveTerminalId();
  const prepared = await prepareLivePane(entry.provider, paneIdValue, entry.cwd, entry.initialCommand);
  if (!liveTerminals.value.some((item) => item.key === terminalKey)) {
    await unregisterLivePane(paneIdValue);
    return;
  }
  const newPane = prepared.pane;
  const latestEntry = liveTerminals.value.find((item) => item.key === terminalKey);
  if (!latestEntry) {
    await unregisterLivePane(paneIdValue);
    return;
  }
  const newRoot = splitPaneInTree(latestEntry.paneRoot, paneId, direction, newPane);
  if (newRoot === latestEntry.paneRoot) {
    await unregisterLivePane(paneIdValue);
    return;
  }
  liveTerminals.value = liveTerminals.value.map((item) =>
    item.key === terminalKey ? { ...item, paneRoot: newRoot, activePaneId: newPane.id } : item
  );
}

function persistLiveTerminals() {
  if (!liveTerminalsScope) return;
  void liveTerminalsPersistence.save(
    {
      version: 1,
      activeKey: activeLiveTerminalKey.value,
      terminals: liveTerminals.value.filter((entry) => !isPluginProvider(entry.provider)).map((entry) => ({
        key: entry.key,
        provider: entry.provider,
        session: entry.session,
        label: entry.label,
        autoLabel: entry.autoLabel,
        manualLabelOverride: entry.manualLabelOverride,
        createdAt: entry.createdAt,
        ...(entry.cwd ? { cwd: entry.cwd } : {}),
      })),
    },
    liveTerminalsScope,
  );
}

/**
 * 重启后重新挂起上次的终端标签。PTY 已随上次退出关闭，这里只重建标签身份，
 * 并进入原工作目录的空白 shell —— 不重放启动命令，避免自动开启新一轮 CLI 会话。
 */
async function hydrateLiveTerminals() {
  const captured = liveTerminalsPersistence.capture();
  // 深链明确指定了要打开的会话时以深链为准，不再恢复上次的标签集合。
  if (parseSessionDeepLink(route.query).kind !== "none") {
    liveTerminalsScope = captured;
    return;
  }
  // 自定义工具的标签需要先读到工具配置才能判断是否仍然有效。
  await toolPrefsReady;
  const persisted = await liveTerminalsPersistence.load(captured);
  if (!liveTerminalsPersistence.isCurrent(captured)) return;
  liveTerminalsScope = captured;
  const stored = persisted?.version === 1 ? persisted.terminals : [];
  if (!Array.isArray(stored) || stored.length === 0) return;
  // 深链或用户操作已经开出终端时不覆盖当前状态。
  if (liveTerminals.value.length > 0) return;

  const restored: LiveSessionTerminal[] = [];
  // 早期版本写入过 key 重复的标签，原样恢复会让标签栏的 v-for 撞 key，
  // 活动标签也指不准；这里按 key 去重，重复项不再占用 PTY。
  const restoredKeys = new Set<string>();
  for (const item of stored) {
    if (!item?.key || !isKnownProvider(item.provider)) continue;
    if (restoredKeys.has(item.key)) continue;
    restoredKeys.add(item.key);
    const terminalId = nextLiveTerminalId();
    const prepared = await prepareLivePane(item.provider, terminalId, item.cwd, "");
    restored.push({
      key: item.key,
      terminalId,
      provider: item.provider,
      session: item.session,
      label: item.label || item.autoLabel,
      autoLabel: item.autoLabel || item.label,
      manualLabelOverride: item.manualLabelOverride === true,
      createdAt: item.createdAt || Date.now(),
      cwd: item.cwd,
      initialCommand: "",
      paneRoot: prepared.pane,
      activePaneId: terminalId,
      paneActivities: {},
    });
  }
  if (restored.length === 0) return;
  if (!liveTerminalsPersistence.isCurrent(captured) || liveTerminals.value.length > 0) {
    for (const entry of restored) void unregisterLivePane(entry.terminalId);
    return;
  }

  liveTerminals.value = restored;
  const active = restored.find((entry) => entry.key === persisted?.activeKey) ?? restored[0];
  activeLiveTerminalKey.value = active.key;
  // 标签栏与终端面板都在 selected + terminal 面板下渲染，两者缺一标签仍不可见。
  selected.value = { provider: active.provider, session: active.session };
  activePanel.value = "terminal";
}

function unregisterLivePane(paneId: string) {
  if (!isDesktopRuntime()) return Promise.resolve();
  return invokeBackend<void>("terminal_activity_unregister", { payload: { paneId } }).catch(() => {});
}

function closeSessionPane(terminalKey: string, paneId: string) {
  const entry = liveTerminals.value.find((item) => item.key === terminalKey);
  if (!entry) return;
  if (isDesktopRuntime()) {
    void invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => {});
  }
  void unregisterLivePane(paneId);
  const newRoot = removePaneFromTree(entry.paneRoot, paneId);
  if (!newRoot) { void closeLiveTerminal(terminalKey); return; }
  const allIds = collectLeafIds(newRoot);
  const nextActive = paneId === entry.activePaneId ? (allIds[0] ?? "") : entry.activePaneId;
  liveTerminals.value = liveTerminals.value.map((item) => {
    if (item.key !== terminalKey) return item;
    const paneActivities = { ...item.paneActivities };
    delete paneActivities[paneId];
    return { ...item, paneRoot: newRoot, activePaneId: nextActive, paneActivities };
  });
}

// Explicit user choice from a blocked plugin-profile pane ("Open default shell"),
// as in the terminal workspace: close whatever the session left behind and start
// the default shell in the same pane.
async function useDefaultShellInSessionPane(terminalKey: string, paneId: string) {
  deleteTerminalSnapshot(paneId);
  if (isDesktopRuntime()) {
    await invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => {});
  }
  liveTerminals.value = liveTerminals.value.map((item) => {
    if (item.key !== terminalKey) return item;
    const paneRoot = replaceProfilePaneWithDefaultShell(item.paneRoot, paneId);
    return paneRoot === item.paneRoot ? item : { ...item, paneRoot };
  });
}

function selectSessionPane(terminalKey: string, paneId: string) {
  liveTerminals.value = liveTerminals.value.map((item) =>
    item.key === terminalKey ? { ...item, activePaneId: paneId } : item
  );
}

function updateLivePaneActivity(
  terminalKey: string,
  paneId: string,
  snapshot: LocalTerminalActivity,
) {
  // Mutate the single entry in place: activity arrives every few hundred ms per
  // busy pane, and replacing the array would re-render the whole workspace.
  const entry = liveTerminals.value.find((item) => item.key === terminalKey);
  if (!entry || !collectLeafIds(entry.paneRoot).includes(paneId)) return;
  const current = entry.paneActivities[paneId];
  const incoming: TerminalPaneActivity = {
    ...snapshot,
    source: "generic",
    startedAt: current?.startedAt || entry.createdAt,
  };
  if (!shouldApplyTerminalActivity(current, incoming)) {
    // 通用信号让位给 Claude 的精确状态，但输出时间戳要留下：它是判断
    // 精确状态是否已经过期的唯一依据，丢掉就会在长任务途中误判成静默。
    if (!current || !incoming.lastOutputAt || (current.lastOutputAt || 0) >= incoming.lastOutputAt) {
      return;
    }
    entry.paneActivities[paneId] = { ...current, lastOutputAt: incoming.lastOutputAt };
    return;
  }
  entry.paneActivities[paneId] = incoming;
}

function applyClaudeActivity(snapshot: ClaudeActivityEvent) {
  for (const entry of liveTerminals.value) {
    if (!collectLeafIds(entry.paneRoot).includes(snapshot.paneId)) continue;
    const current = entry.paneActivities[snapshot.paneId];
    const incoming: TerminalPaneActivity = {
      phase: snapshot.phase,
      source: "claude-hook",
      summary: safeTerminalTaskLabel(snapshot.summary || "", 120),
      startedAt: current?.startedAt || entry.createdAt,
      updatedAt: snapshot.updatedAt,
      lastOutputAt: current?.lastOutputAt,
      ...(snapshot.phase === "completed" || snapshot.phase === "failed"
        ? { endedAt: snapshot.updatedAt }
        : {}),
    };
    if (!shouldApplyTerminalActivity(current, incoming)) continue;
    const autoLabel = safeTerminalTaskLabel(snapshot.autoLabel || "");
    if (autoLabel && entry.autoLabel !== autoLabel) entry.autoLabel = autoLabel;
    if (autoLabel && !entry.manualLabelOverride && entry.label !== autoLabel) entry.label = autoLabel;
    entry.paneActivities[snapshot.paneId] = incoming;
  }
}

function resizeSessionSplit(terminalKey: string, splitId: string, sizes: number[]) {
  const entry = liveTerminals.value.find((item) => item.key === terminalKey);
  if (!entry) return;
  const newRoot = updateSplitSizes(entry.paneRoot, splitId, sizes);
  if (newRoot === entry.paneRoot) return;
  liveTerminals.value = liveTerminals.value.map((item) =>
    item.key === terminalKey ? { ...item, paneRoot: newRoot } : item
  );
}

function onTabBarWheel(event: WheelEvent) {
  event.preventDefault();
  const el = event.currentTarget as HTMLElement;
  el.scrollLeft += event.deltaY + event.deltaX;
}

function isSelected(provider: ProviderId, session: Session) {
  return Boolean(
    selected.value &&
    sessionKey(selected.value.provider, selected.value.session) === sessionKey(provider, session)
  );
}

function formatTime(value: number) {
  if (!value) return t("sessions.timeUnknown");
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(
    value
  );
}

function formatMessageTime(value?: string) {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf())
    ? value
    : new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(parsed);
}

function shortPath(path: string | undefined) {
  if (!path) return "";
  const parts = path
    .replace(/[\\/]+$/, "")
    .split(/[\\/]/)
    .filter(Boolean);
  return parts.slice(-2).join("/") || path;
}

function sessionRole(message: SessionMessage) {
  if (message.role === "user") return "user";
  if (message.role === "assistant") return "assistant";
  return "system";
}

function sessionRoleLabel(message: SessionMessage) {
  if (message.role === "user") return t("sessions.role.user");
  if (message.role === "assistant") return t("sessions.role.assistant");
  return message.role || message.msgType || t("sessions.message");
}

function sessionStatusLabel(status: string) {
  if (status === "completed") return t("sessions.status.completed");
  if (status === "in_progress") return t("sessions.status.in_progress");
  if (status === "failed") return t("sessions.status.failed");
  if (status === "interrupted") return t("sessions.status.interrupted");
  return status;
}

function buildNewSessionCommand(provider: ProviderId, cwd?: string) {
  return buildSessionToolNewCommand(toolPrefs.value, provider, cwd);
}

function buildLaunchCommand(provider: ProviderId, id: string, cwd?: string) {
  return buildSessionToolResumeCommand(toolPrefs.value, provider, id, cwd);
}

function buildCdCommand(cwd: string) {
  return cwd ? `cd ${quoteShellArgument(cwd)}` : "";
}

function buildFullCommand(cwd: string, command: string) {
  const cdCommand = buildCdCommand(cwd);
  return cdCommand ? `${cdCommand} && ${command}` : command;
}

async function loadClaudeProjects() {
  providerLoading.claude = true;
  try {
    projects.value = await invokeBackend<ClaudeProject[]>("list_claude_projects", {});
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    providerLoading.claude = false;
  }
}

async function loadProvider(provider: ProviderDefinition) {
  if (!isDesktopRuntime()) return;
  if (provider.id === "claude") {
    await loadClaudeProjects();
    return;
  }

  providerLoading[provider.id] = true;
  try {
    const sessions =
      provider.id === "codex"
        ? await invokeBackend<Session[]>("list_codex_sessions", {})
        : await invokeBackend<Session[]>("list_ai_cli_sessions", {
            adapter: provider.adapter,
            ...(provider.custom ? { custom: provider.custom } : {}),
          });
    // 加载期间工具可能已在设置里被移除，此时不再写回。
    if (providers.value.some((item) => item.id === provider.id)) {
      sessionsByProvider[provider.id] = sessions ?? [];
    }
  } catch (error) {
    ElMessage.error(provider.custom ? `${provider.label}: ${String(error)}` : String(error));
  } finally {
    providerLoading[provider.id] = false;
  }
}

async function loadClaudeProjectSessions(project: ClaudeProject) {
  const loaded = projectSessions[project.encodedDir] ?? projectSessionsBatch.get(project.encodedDir);
  if (loaded) return loaded;
  const sessions = await invokeBackend<Session[]>("get_project_sessions", {
    encodedDir: project.encodedDir,
  });
  projectSessionsBatch.set(project.encodedDir, sessions);
  return sessions;
}

async function toggleProject(project: ClaudeProject) {
  openProjects[project.encodedDir] = !openProjects[project.encodedDir];
  if (!openProjects[project.encodedDir] || projectSessions[project.encodedDir] || projectSessionsBatch.get(project.encodedDir)) return;

  try {
    await loadClaudeProjectSessions(project);
  } catch (error) {
    ElMessage.error(String(error));
  }
}

function onProjectContextMenu(event: MouseEvent, project: ClaudeProject) {
  event.preventDefault();
  const menuWidth = 218;
  const menuHeight = 48;
  projectContextMenu.value = {
    project,
    x: Math.max(8, Math.min(event.clientX, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, Math.min(event.clientY, window.innerHeight - menuHeight - 8)),
  };
}

function closeProjectContextMenu() {
  projectContextMenu.value = null;
}

function openSessionMenu(event: MouseEvent, provider: ProviderId, session: Session) {
  const anchor = (event.currentTarget as HTMLElement | null)?.getBoundingClientRect();
  const menuWidth = 180;
  const menuHeight = provider === "claude" ? 170 : 130;
  const x = anchor ? anchor.right - menuWidth : event.clientX;
  const y = anchor ? anchor.bottom + 4 : event.clientY;
  sessionMenu.value = {
    provider,
    session,
    x: Math.max(8, Math.min(x, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, Math.min(y, window.innerHeight - menuHeight - 8)),
  };
}

function runSessionMenuCommand(command: string) {
  const menu = sessionMenu.value;
  sessionMenu.value = null;
  if (menu) void handleSessionMenuCommand(menu.provider, menu.session, command);
}

function openProjectTerminal(project: ClaudeProject) {
  closeProjectContextMenu();
  void createProviderTerminal("claude", project.decodedPath);
}

let statusPollTimer: number | undefined;
let stopClaudeActivity: (() => void) | undefined;

// 轮询后端最新状态（转录推断 + 进程存活），让侧边栏圆点贴近 CLI 实际运行状态。
async function refreshSessionStatuses() {
  if (!isDesktopRuntime() || document.hidden) return;
  const tasks: Promise<void>[] = [];
  for (const [encodedDir, sessions] of Object.entries(projectSessions)) {
    if (!openProjects[encodedDir] || !sessions?.length) continue;
    tasks.push(
      invokeBackend<Record<string, string>>("claude_session_statuses", { encodedDir })
        .then((statuses) => {
          for (const session of sessions) {
            const status = statuses[session.uuid ?? ""];
            if (status !== undefined) session.status = status;
          }
        })
        .catch(() => {})
    );
  }
  const codexSessions = sessionsByProvider.codex;
  if (codexSessions?.length) {
    tasks.push(
      invokeBackend<Record<string, string>>("codex_session_statuses", {})
        .then((statuses) => {
          for (const session of codexSessions) {
            const status = statuses[session.id ?? ""];
            if (status !== undefined) session.status = status;
          }
        })
        .catch(() => {})
    );
  }
  await Promise.all(tasks);
}

async function refreshAll() {
  if (!isDesktopRuntime()) return;
  refreshing.value = true;
  try {
    // Built-in and custom tools only; plugin sources reload themselves when
    // expanded (collapsed ones are never called).
    await Promise.all(providers.value.map(loadProvider));
  } finally {
    refreshing.value = false;
    pluginRefreshToken.value += 1;
  }
}

/** 丢弃已停用或已删除工具的缓存，重新启用时会重新加载。插件来源由 PluginSessionSources 与插件快照管理。 */
function pruneInactiveProviders() {
  const active = new Set(providers.value.map((provider) => provider.id));
  for (const record of [sessionsByProvider, openProviders, providerLoading]) {
    for (const id of Object.keys(record)) {
      if (!active.has(id) && !isPluginProvider(id)) delete record[id];
    }
  }
  for (const id of [...visibleProviderProjectGroupsById.keys()]) {
    if (!active.has(id)) visibleProviderProjectGroupsById.delete(id);
  }
  for (const key of Object.keys(openProviderProjects)) {
    if (![...active].some((id) => key.startsWith(`${id}:`))) delete openProviderProjects[key];
  }
  if (selected.value && !isPluginProvider(selected.value.provider) && !isKnownProvider(selected.value.provider)) {
    selected.value = undefined;
    messages.value = [];
    activePanel.value = "conversation";
  }
}

/**
 * 应用工具配置：先把数据目录覆盖同步给后端（失败时保留旧目录并提示），
 * 返回 false 表示期间又有更新的配置，调用方应放弃后续刷新。
 */
async function applyToolPrefs(prefs: SessionToolPrefs) {
  const requestId = ++toolPrefsSequence;
  toolPrefs.value = prefs;
  pruneInactiveProviders();
  if (!isDesktopRuntime()) return true;
  try {
    const roots = await applySessionToolRoots(prefs);
    if (requestId !== toolPrefsSequence) return false;
    // Claude 的项目会话按目录缓存，数据目录变了之后旧缓存全部失效。
    if ((roots.claude ?? "") !== (resolvedRoots.value.claude ?? "")) {
      projectSessionsBatch.clear();
      for (const key of Object.keys(projectSessions)) delete projectSessions[key];
      for (const key of Object.keys(openProjects)) delete openProjects[key];
    }
    resolvedRoots.value = roots;
  } catch (error) {
    if (requestId !== toolPrefsSequence) return false;
    ElMessage.error(t("sessions.toolSettings.rootsFailed", { error: String(error) }));
  }
  return true;
}

async function initializeToolPrefs() {
  try {
    const prefs = await loadSessionToolPrefs();
    toolPrefsLoaded.value = true;
    await applyToolPrefs(prefs);
  } finally {
    toolPrefsLoaded.value = true;
    resolveToolPrefsReady();
  }
}

function openToolSettings() {
  toolSettingsMounted.value = true;
  toolSettingsOpen.value = true;
}

function onToolPrefsChanged(event: Event) {
  const detail = (event as CustomEvent<SessionToolPrefs | undefined>).detail;
  void (async () => {
    await toolPrefsReady;
    if (await applyToolPrefs(detail ?? (await loadSessionToolPrefs()))) await refreshAll();
  })();
}

async function createProviderTerminal(provider: ProviderId, projectPath?: string) {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }

  const cwd =
    projectPath ||
    (await chooseNativeDirectory(
      t("sessions.chooseTerminalFolder", { provider: providerLabel(provider) }),
    ));
  if (!cwd) return;

  const terminalId = nextLiveTerminalId();
  const command = buildNewSessionCommand(provider, cwd);
  const prepared = await prepareLivePane(provider, terminalId, cwd, command);
  const key = `new:${provider}:${terminalId}`;
  const session: Session = {
    id: key,
    title: t("sessions.newCliSession", { provider: providerLabel(provider) }),
    cwd,
    decodedPath: cwd,
    lastAt: Date.now(),
    messageCount: 0,
    isNew: true,
  };
  liveTerminals.value.push({
    key,
    terminalId,
    provider,
    session,
    label: providerLabel(provider),
    autoLabel: providerLabel(provider),
    manualLabelOverride: false,
    createdAt: Date.now(),
    cwd,
    initialCommand: command,
    paneRoot: prepared.pane,
    activePaneId: terminalId,
    paneActivities: {},
  });
  selected.value = { provider, session };
  messages.value = [];
  messageLoading.value = false;
  messageLoadSequence += 1;
  activeLiveTerminalKey.value = key;
  activePanel.value = "terminal";
  persistLiveTerminals();
  await nextTick();
}

/** Cancels the plugin transcript being read (the host request is cancelled too). */
function cancelPluginMessages() {
  const controller = pluginMessageController;
  pluginMessageController = undefined;
  controller?.abort();
}

function clearPluginTranscript() {
  pluginMessages.value = [];
  pluginMessagesTruncated.value = false;
  pluginMessageError.value = "";
}

function pluginSummaryToSession(summary: PluginSessionSummary): Session {
  return {
    id: summary.id,
    title: summary.title,
    ...(summary.model ? { model: summary.model } : {}),
    lastAt: summary.lastAt ?? 0,
    messageCount: summary.messageCount ?? 0,
  };
}

function openPluginSessionFromList(source: PluginSessionSource, summary: PluginSessionSummary) {
  selectedPluginSession.value = summary;
  void openSession(source.key, pluginSummaryToSession(summary));
}

/** Reads a plugin session's transcript: only when the user opens it, rendered as plain text. */
async function openPluginSession(requestId: number, provider: ProviderId, session: Session) {
  activePanel.value = "conversation";
  messages.value = [];
  clearPluginTranscript();
  const source = pluginSourceFor(provider);
  const id = sessionId(session);
  if (!source || !id) {
    messageLoading.value = false;
    return;
  }
  const controller = new AbortController();
  pluginMessageController = controller;
  messageLoading.value = true;
  try {
    const result = await loadPluginSessionMessages(source, id, controller.signal);
    if (requestId !== messageLoadSequence || controller.signal.aborted) return;
    if (!result.ok) {
      if (result.error.code !== "cancelled") pluginMessageError.value = providerErrorText(result.error);
      return;
    }
    pluginMessages.value = result.response.messages;
    pluginMessagesTruncated.value = result.response.truncated;
    await nextTick();
    if (requestId !== messageLoadSequence) return;
    const messageList = messageListElement.value;
    messageList?.setScrollTop(messageList.wrapRef?.scrollHeight ?? 0);
  } finally {
    if (pluginMessageController === controller) pluginMessageController = undefined;
    if (requestId === messageLoadSequence) messageLoading.value = false;
  }
}

function retryPluginMessages() {
  if (selected.value && selectedIsPlugin.value) void openSession(selected.value.provider, selected.value.session);
}

async function openSession(provider: ProviderId, session: Session) {
  const requestId = ++messageLoadSequence;
  cancelPluginMessages();
  reloadPluginMessagesOnActivate = false;
  selected.value = { provider, session };
  if (isPluginProvider(provider)) {
    await openPluginSession(requestId, provider, session);
    return;
  }
  selectedPluginSession.value = undefined;
  clearPluginTranscript();
  const existingTerminal = liveTerminals.value.find(
    (entry) =>
      entry.provider === provider && sessionId(entry.session) === sessionId(session),
  );
  activePanel.value = existingTerminal ? "terminal" : "conversation";
  if (existingTerminal) activeLiveTerminalKey.value = existingTerminal.key;
  messageLoading.value = true;
  messages.value = [];
  try {
    let loadedMessages: SessionMessage[];
    if (provider === "claude") {
      loadedMessages = await invokeBackend<SessionMessage[]>("get_session_messages", {
        encodedDir: session.encodedDir,
        uuid: session.uuid,
        limit: SESSION_MESSAGE_LIMIT,
      });
    } else if (provider === "codex") {
      // The Codex reader has no limit argument; keep the same tail as the other providers.
      loadedMessages = (await invokeBackend<SessionMessage[]>("get_codex_session_messages", {
        filePath: session.filePath,
      })).slice(-SESSION_MESSAGE_LIMIT);
    } else {
      loadedMessages = await invokeBackend<SessionMessage[]>("get_ai_cli_session_messages", {
        ...providerBackendArgs(provider),
        filePath: session.filePath,
        limit: SESSION_MESSAGE_LIMIT,
      });
    }
    if (requestId !== messageLoadSequence) return;
    messages.value = loadedMessages;
    await nextTick();
    if (requestId !== messageLoadSequence) return;
    const messageList = messageListElement.value;
    messageList?.setScrollTop(messageList.wrapRef?.scrollHeight ?? 0);
  } catch (error) {
    if (requestId === messageLoadSequence) ElMessage.error(String(error));
  } finally {
    if (requestId === messageLoadSequence) messageLoading.value = false;
  }
}

let sessionsReady = false;
let sessionDeepLinkSequence = 0;

async function openDeepLinkedSession() {
  const target = parseSessionDeepLink(route.query);
  if (target.kind === "none") return;
  if (target.kind === "invalid") {
    ElMessage.error(t("sessions.deepLinkInvalid"));
    return;
  }
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }

  const requestId = ++sessionDeepLinkSequence;
  let matchingSession: Session | undefined;
  if (target.provider === "claude") {
    for (const project of projects.value) {
      try {
        const sessions = await loadClaudeProjectSessions(project);
        if (requestId !== sessionDeepLinkSequence) return;
        matchingSession = sessions.find((session) => sessionId(session) === target.sessionId);
        if (matchingSession) {
          openProjects[project.encodedDir] = true;
          break;
        }
      } catch (error) {
        if (requestId === sessionDeepLinkSequence) ElMessage.error(String(error));
        return;
      }
    }
  } else {
    matchingSession = (sessionsByProvider[target.provider] ?? []).find(
      (session) => sessionId(session) === target.sessionId,
    );
    if (matchingSession) {
      const path = matchingSession.decodedPath || matchingSession.cwd || "";
      const pathKey = normalizeFilesystemPath(path) || "__unknown__";
      openProviderProjects[`${target.provider}:${pathKey}`] = true;
    }
  }

  if (requestId !== sessionDeepLinkSequence) return;
  if (!matchingSession) {
    ElMessage.error(t("sessions.deepLinkUnavailable", { provider: providerLabel(target.provider) }));
    return;
  }
  await openSession(target.provider, matchingSession);
}

async function renameClaudeSession(session: Session) {
  try {
    const { value } = await ElMessageBox.prompt(t("sessions.renamePrompt"), t("sessions.rename"), {
      inputValue: session.title,
      inputPlaceholder: t("sessions.renamePlaceholder"),
      inputValidator: (input) => {
        const title = input.trim();
        if (!title) return t("sessions.renameRequired");
        return Array.from(title).length <= 140 || t("sessions.renameTooLong");
      },
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const title = value.trim();
    if (!title || title === session.title) return;

    await invokeBackend("rename_claude_session", {
      encodedDir: session.encodedDir,
      uuid: session.uuid,
      title,
    });
    session.title = title;
    if (selected.value && isSelected("claude", session)) selected.value.session.title = title;
    ElMessage.success(t("sessions.renamed"));
  } catch (error) {
    const reason = String(error);
    if (reason !== "cancel" && reason !== "close") ElMessage.error(reason);
  }
}

async function openSessionInEmbeddedTerminal(provider: ProviderId, session: Session) {
  // Transcript loading is independent from PTY startup. Start it in the background
  // so a large local history cannot hold the terminal behind a loading mask.
  if (!isSelected(provider, session)) void openSession(provider, session);
  await openEmbeddedTerminal();
}

async function handleSessionMenuCommand(provider: ProviderId, session: Session, command: unknown) {
  switch (String(command)) {
    case "terminal":
      await openSessionInEmbeddedTerminal(provider, session);
      return;
    case "rename":
      if (provider === "claude") await renameClaudeSession(session);
      return;
    case "delete":
      await deleteSession(provider, session);
      return;
    default:
      await openSession(provider, session);
  }
}

async function deleteSession(provider: ProviderId, session: Session) {
  try {
    await ElMessageBox.confirm(t("sessions.deleteConfirm"), t("common.delete"), {
      type: "warning",
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    if (provider === "claude") {
      await invokeBackend("delete_claude_session", {
        encodedDir: session.encodedDir,
        uuid: session.uuid,
      });
    } else if (provider === "codex") {
      await invokeBackend("delete_codex_session", { filePath: session.filePath });
    } else {
      await invokeBackend("delete_ai_cli_session", {
        ...providerBackendArgs(provider),
        filePath: session.filePath,
      });
    }

    if (selected.value && isSelected(provider, session)) {
      selected.value = undefined;
      messages.value = [];
    }
    if (provider === "claude" && session.encodedDir) {
      projectSessionsBatch.delete(session.encodedDir);
      delete projectSessions[session.encodedDir];
      // 项目文件夹处于展开状态时，立刻重新加载剩余会话；
      // 否则缓存被清空后该文件夹下的其他会话会从界面消失。
      if (openProjects[session.encodedDir]) {
        const project = projects.value.find(
          (item) => item.encodedDir === session.encodedDir
        );
        if (project) await loadClaudeProjectSessions(project);
      }
    }
    const definition = providerDefinition(provider);
    if (definition) await loadProvider(definition);
    ElMessage.success(t("sessions.deleted"));
  } catch (error) {
    // User cancelled, or the backend declined deletion. Keep the current list intact.
    if (error !== "cancel" && error !== "close") ElMessage.error(String(error));
  }
}

async function copyPath(path: string) {
  if (!path) return;
  try {
    await navigator.clipboard.writeText(path);
    ElMessage.success(t("sessions.pathCopied"));
  } catch {
    ElMessage.error(t("sessions.copyFailed"));
  }
}

async function copyCommand(command: string) {
  if (!command) return;
  try {
    await navigator.clipboard.writeText(command);
    ElMessage.success(t("sessions.commandCopied"));
  } catch {
    ElMessage.error(t("sessions.copyFailed"));
  }
}

async function openEmbeddedTerminal() {
  if (selectedIsPlugin.value) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }

  const current = selected.value;
  const command = selectedLaunchCommand.value?.trim();
  const cwd = selectedPath.value.trim();
  if (!current || !command) {
    ElMessage.error(t("terminal.startFailed"));
    return;
  }

  const key = liveTerminalKey(current.provider, current.session);
  if (!liveTerminals.value.some((entry) => entry.key === key)) {
    const terminalId = nextLiveTerminalId();
    const prepared = await prepareLivePane(current.provider, terminalId, cwd || undefined, command);
    // 会话终端的 key 只由 provider + 会话 + 目录决定，连点两次会让两个调用都通过
    // await 之前的查重。准备完 PTY 后再确认一次，重复的那个直接回收，避免标签栏撞 key。
    if (liveTerminals.value.some((entry) => entry.key === key)) {
      void unregisterLivePane(terminalId);
    } else {
      const autoLabel = current.session.title || sessionId(current.session);
      liveTerminals.value.push({
        key,
        terminalId,
        provider: current.provider,
        session: current.session,
        label: autoLabel,
        autoLabel,
        manualLabelOverride: false,
        createdAt: Date.now(),
        cwd: cwd || undefined,
        initialCommand: command,
        paneRoot: prepared.pane,
        activePaneId: terminalId,
        paneActivities: {},
      });
    }
  }

  activeLiveTerminalKey.value = key;
  // Make the terminal panel visible before LocalTerminal mounts so xterm can
  // calculate a real viewport and the PTY receives the resume command reliably.
  activePanel.value = "terminal";
  persistLiveTerminals();
  await nextTick();
}

async function openNewEmbeddedTerminal() {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }

  const current = selected.value;
  const cwd = selectedPath.value.trim();
  if (!current) {
    ElMessage.error(t("terminal.startFailed"));
    return;
  }

  const terminalId = nextLiveTerminalId();
  const sessionTerminalKey = liveTerminalKey(current.provider, current.session);
  const terminalNumber =
    liveTerminals.value.filter(
      (entry) =>
        entry.provider === current.provider &&
        sessionId(entry.session) === sessionId(current.session),
    ).length + 1;
  const key = `${sessionTerminalKey}:${terminalId}`;
  const autoLabel = t("sessions.terminalTab", { number: terminalNumber });
  const prepared = await prepareLivePane(current.provider, terminalId, cwd || undefined, "");
  liveTerminals.value.push({
    key,
    terminalId,
    provider: current.provider,
    session: current.session,
    label: autoLabel,
    autoLabel,
    manualLabelOverride: false,
    createdAt: Date.now(),
    cwd: cwd || undefined,
    initialCommand: "",
    paneRoot: prepared.pane,
    activePaneId: terminalId,
    paneActivities: {},
  });
  activeLiveTerminalKey.value = key;
  activePanel.value = "terminal";
  persistLiveTerminals();
  await nextTick();
}

async function activateLiveTerminal(entry: LiveSessionTerminal) {
  terminalAttention.dismiss(collectLeafIds(entry.paneRoot));
  const current = selected.value;
  if (
    !entry.session.isNew &&
    (!current ||
      current.provider !== entry.provider ||
      sessionId(current.session) !== sessionId(entry.session))
  ) {
    await openSession(entry.provider, entry.session);
  } else if (entry.session.isNew) {
    selected.value = { provider: entry.provider, session: entry.session };
    messages.value = [];
  }
  if (!liveTerminals.value.some((terminal) => terminal.key === entry.key)) return;
  activeLiveTerminalKey.value = entry.key;
  activePanel.value = "terminal";
}

async function renameLiveTerminal(key: string) {
  const entry = liveTerminals.value.find((item) => item.key === key);
  if (!entry) return;
  try {
    const { value } = await ElMessageBox.prompt(
      t("sessions.renameTerminalPrompt"),
      t("sessions.renameTerminal"),
      {
        inputValue: entry.label,
        inputPlaceholder: t("sessions.renameTerminalPlaceholder"),
        inputValidator: (input) => {
          const label = input.trim();
          if (!label) return t("sessions.renameTerminalRequired");
          return Array.from(label).length <= 80 || t("sessions.renameTerminalTooLong");
        },
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      },
    );
    const label = value.trim();
    if (!label) return;
    liveTerminals.value = liveTerminals.value.map((terminal) =>
      terminal.key === key ? { ...terminal, label, manualLabelOverride: true } : terminal,
    );
    persistLiveTerminals();
  } catch (error) {
    const reason = String(error);
    if (reason !== "cancel" && reason !== "close") ElMessage.error(reason);
  }
}

function restoreAutomaticLiveTerminalLabel(key: string) {
  liveTerminals.value = liveTerminals.value.map((terminal) =>
    terminal.key === key
      ? { ...terminal, label: terminal.autoLabel, manualLabelOverride: false }
      : terminal,
  );
  persistLiveTerminals();
}

function handleLiveTerminalCommand(key: string, command: unknown) {
  if (command === "rename") void renameLiveTerminal(key);
  else if (command === "restore-auto-label") restoreAutomaticLiveTerminalLabel(key);
  else if (command === "close-left" || command === "close-right" || command === "close-others") {
    const index = liveTerminals.value.findIndex((item) => item.key === key);
    const closing = liveTerminals.value.filter((_, i) =>
      command === "close-left" ? i < index : command === "close-right" ? i > index : i !== index,
    );
    void closeLiveTerminals(closing.map((item) => item.key), key);
  }
  else if (command === "close-all") void closeLiveTerminals(liveTerminals.value.map((item) => item.key));
}

function closeLiveTerminal(key: string) {
  return closeLiveTerminals([key]);
}

// Closes the given tabs and their shells; `keep` becomes the active tab when
// the active one is closed (the last remaining tab otherwise).
async function closeLiveTerminals(keys: string[], keep?: string) {
  const closing = new Set(keys);
  if (closing.size === 0) return;
  if (isDesktopRuntime()) {
    for (const entry of liveTerminals.value) {
      if (!closing.has(entry.key)) continue;
      for (const paneId of collectLeafIds(entry.paneRoot)) {
        void invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => {});
        void unregisterLivePane(paneId);
      }
    }
  }
  const nextTabs = liveTerminals.value.filter((item) => !closing.has(item.key));
  liveTerminals.value = nextTabs;
  if (closing.has(activeLiveTerminalKey.value)) {
    const next = nextTabs.find((item) => item.key === keep) ?? nextTabs[nextTabs.length - 1];
    if (next) {
      await activateLiveTerminal(next);
    } else {
      activeLiveTerminalKey.value = "";
      activePanel.value = "conversation";
    }
  }
  persistLiveTerminals();
}

function showConversation() {
  activePanel.value = "conversation";
}

function sessionTranscript(limit = 48_000) {
  // Message adapters may expose structural turns (for example, tool calls) that
  // have no readable payload. Do not send placeholder labels to the assistant:
  // they are not conversation evidence and cause summary/TODO extraction to fail.
  const transcript = messages.value
    .flatMap((message) => {
      const content = message.content?.trim();
      if (!content) return [];
      return [`## ${sessionRoleLabel(message)}
${content}`];
    })
    .join("\n\n");
  return transcript.length > limit ? transcript.slice(-limit) : transcript;
}

function askAssistant(kind: "summary" | "todo") {
  if (!selected.value) return;
  const subject = `${providerLabel(selected.value.provider)} · ${selected.value.session.title || selectedSessionId.value}`;
  const instruction = t(
    kind === "summary" ? "sessions.assistantSummaryPrompt" : "sessions.assistantTodoPrompt"
  );
  const pathLine = selectedPath.value
    ? `\n${t("sessions.assistantWorkspace")}：${selectedPath.value}`
    : "";
  const transcript = sessionTranscript();
  assistant.setDraft(
    `${instruction}\n\n${t("sessions.assistantSession")}：${subject}${pathLine}\n\n--- ${t("sessions.assistantTranscript")} ---\n${transcript || t("sessions.noMessages")}`
  );
  assistant.show("agentic");
}

function resumeSelectedSession() {
  // Plugin sessions are read only: there is no CLI to resume them with.
  if (!selected.value || selectedIsPlugin.value || !selectedLaunchCommand.value) return;
  const terminalCount = workbench.workspaceTabs.filter((tab) => tab.view === "terminal").length;
  const title = `${providerLabel(selected.value.provider)} ${terminalCount + 1}`;
  const tab = workbench.createTab("terminal", title);
  if (!tab) return;

  const extraEnv = providerEnv(selected.value.provider);
  terminalWorkspace.activateTab(tab.id, {
    initialCwd: selectedPath.value || undefined,
    initialCommand: selectedLaunchCommand.value,
    ...(Object.keys(extraEnv).length > 0 ? { extraEnv } : {}),
  });
  void router.push({ name: "terminal" });
}

watch(
  () => messages.value.length,
  (length, previousLength) => {
    visibleMessageStart.value = nextAiMessageRenderStart(visibleMessageStart.value, previousLength, length);
  },
);

async function loadEarlierMessages() {
  // Keep the reader's current message in place while older turns are prepended above it.
  const wrap = messageListElement.value?.wrapRef;
  const distanceFromBottom = wrap ? wrap.scrollHeight - wrap.scrollTop : 0;
  visibleMessageStart.value = earlierAiMessageRenderStart(visibleMessageStart.value);
  await nextTick();
  if (wrap) messageListElement.value?.setScrollTop(wrap.scrollHeight - distanceFromBottom);
}

watch(
  () => [route.query.provider, route.query.sessionId] as const,
  () => {
    if (sessionsReady) void openDeepLinkedSession();
  },
);

function stopBackgroundRefresh() {
  if (statusPollTimer !== undefined) window.clearInterval(statusPollTimer);
  statusPollTimer = undefined;
}

function startBackgroundRefresh() {
  stopBackgroundRefresh();
  statusPollTimer = window.setInterval(() => {
    void refreshSessionStatuses();
  }, 4000);
  void refreshSessionStatuses();
}

onMounted(() => {
  void hydrateNavigatorLayout();
  void hydrateLiveTerminals();
  stopClaudeActivity = listenAppEvent<ClaudeActivityEvent>("terminal-activity", applyClaudeActivity);
  startBackgroundRefresh();
  window.addEventListener(SESSION_TOOLS_EVENT, onToolPrefsChanged);
  void initializeToolPrefs()
    .then(refreshAll)
    .then(async () => {
      sessionsReady = true;
      await openDeepLinkedSession();
    });
});

watch(tabBarScrollEl, (el, prev) => {
  prev?.removeEventListener("wheel", onTabBarWheel);
  el?.addEventListener("wheel", onTabBarWheel, { passive: false });
});

watch(activeLiveTerminalKey, async () => {
  persistLiveTerminals();
  await nextTick();
  const container = tabBarScrollEl.value;
  if (!container) return;
  const activeTab = container.querySelector(".sessions-live-tab.is-active") as HTMLElement | null;
  if (!activeTab) return;
  const tabLeft = activeTab.offsetLeft;
  const tabRight = tabLeft + activeTab.offsetWidth;
  const { scrollLeft, clientWidth } = container;
  if (tabLeft < scrollLeft) {
    container.scrollLeft = tabLeft;
  } else if (tabRight > scrollLeft + clientWidth) {
    container.scrollLeft = tabRight - clientWidth;
  }
});

onActivated(startBackgroundRefresh);
onDeactivated(stopBackgroundRefresh);

// Leaving the page drops a plugin transcript (memory only, like the lists)
// and cancels it while it is read; coming back reads it again.
onDeactivated(() => {
  if (!selectedIsPlugin.value) return;
  messageLoadSequence += 1;
  cancelPluginMessages();
  clearPluginTranscript();
  messageLoading.value = false;
  reloadPluginMessagesOnActivate = true;
});
onActivated(() => {
  if (!reloadPluginMessagesOnActivate) return;
  reloadPluginMessagesOnActivate = false;
  retryPluginMessages();
});

// Selecting anything else cancels a plugin transcript being read.
watch(selected, (next) => {
  if (!next || !isPluginProvider(next.provider)) cancelPluginMessages();
});

// A plugin disabled, uninstalled or inactive in this workspace takes its open session with it.
watch(pluginSessionSources, () => {
  const current = selected.value;
  if (!current || !isPluginProvider(current.provider) || pluginSourceFor(current.provider)) return;
  messageLoadSequence += 1;
  cancelPluginMessages();
  reloadPluginMessagesOnActivate = false;
  selected.value = undefined;
  selectedPluginSession.value = undefined;
  clearPluginTranscript();
  messageLoading.value = false;
  activePanel.value = "conversation";
});

// Terminal attention: which panes are ours (and their tab labels), which one is on
// screen, and focusing a pane when its system notification is clicked.
watch(
  () => liveTerminals.value.flatMap((entry) => collectLeafIds(entry.paneRoot).map((paneId) => [paneId, entry.label] as const)),
  (panes) => terminalAttention.setSessionPanes(panes),
  { immediate: true },
);
const visibleSessionPanes = computed(() => {
  if (workbench.activeTab?.view !== "sessions" || activePanel.value !== "terminal") return [];
  const entry = liveTerminals.value.find((item) => item.key === activeLiveTerminalKey.value);
  return entry ? collectLeafIds(entry.paneRoot) : [];
});
watch(visibleSessionPanes, (paneIds) => terminalAttention.setVisiblePanes("sessions", paneIds), { immediate: true });
watch(() => terminalAttention.focusRequest, (request) => {
  if (!request) return;
  const entry = liveTerminals.value.find((item) => collectLeafIds(item.paneRoot).includes(request.paneId));
  if (!entry) return;
  void activateLiveTerminal(entry).then(() => selectSessionPane(entry.key, request.paneId));
});
function liveTabAttention(entry: LiveSessionTerminal) {
  return terminalAttention.count ? strongestAttention(terminalAttention.items, collectLeafIds(entry.paneRoot)) : undefined;
}
/** The session list row shows the bell of the live terminal running that session. */
function sessionAttention(provider: ProviderId, session: Session) {
  if (!terminalAttention.count) return undefined;
  const id = sessionId(session);
  const entry = liveTerminals.value.find((item) => item.provider === provider && sessionId(item.session) === id);
  return entry ? liveTabAttention(entry) : undefined;
}

onBeforeUnmount(() => {
  projectSessionsBatch.dispose();
  cancelPluginMessages();
  window.removeEventListener(SESSION_TOOLS_EVENT, onToolPrefsChanged);
  tabBarScrollEl.value?.removeEventListener("wheel", onTabBarWheel);
  stopClaudeActivity?.();
  stopClaudeActivity = undefined;
  stopBackgroundRefresh();
  terminalAttention.setVisiblePanes("sessions", []);
  terminalAttention.setSessionPanes([]);
  // LocalTerminal keeps PTYs alive during ordinary pane/tab visibility changes.
  // When this sessions workspace itself is disposed, release them so reopening a
  // historical session always launches the requested resume command rather than
  // silently attaching to an orphaned shell from the previous view instance.
  if (!isDesktopRuntime()) return;
  for (const entry of liveTerminals.value) {
    for (const paneId of collectLeafIds(entry.paneRoot)) {
      void invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => {});
      void unregisterLivePane(paneId);
    }
  }
});
</script>

<template>
  <section ref="workspaceElement" class="sessions-workspace">
    <AppSplitPane
      :model-value="navigatorWidth"
      :collapsed="navigatorCollapsed"
      side="first"
      :min="252"
      :max="(width) => Math.max(320, Math.min(440, width * 0.3))"
      :second-min="360"
      collapsible
      :collapse-label="t('sessions.collapseSidebar')"
      :expand-label="t('sessions.expandSidebar')"
      :aria-label="t('sessions.resizeSidebar')"
      @resize-end="persistNavigatorWidth"
      @update:collapsed="updateNavigatorCollapsed"
    >
      <template #first>
        <aside class="sessions-navigator">
      <div class="sessions-navigator__header">
        <div class="sessions-navigator__title">
          <el-tag class="sessions-navigator__title-icon" effect="plain" disable-transitions>AI</el-tag>
          <strong>{{ t("sessions.groups") }}</strong>
        </div>
        <div class="sessions-navigator__actions">
          <el-button
            text
            class="sessions-icon-button"
            :icon="Setting"
            :title="t('sessions.toolSettings.open')"
            :aria-label="t('sessions.toolSettings.open')"
            @click="openToolSettings"
          />
          <el-button
            text
            class="sessions-icon-button"
            :icon="Refresh"
            :loading="refreshing"
            :title="t('common.refresh')"
            :aria-label="t('common.refresh')"
            @click="refreshAll"
          />
        </div>
      </div>

      <div class="sessions-search">
        <el-input v-model="search" clearable :placeholder="t('sessions.searchPlaceholder')">
          <template #prefix
            ><el-icon><Search /></el-icon
          ></template>
        </el-input>
      </div>

      <el-scrollbar ref="sessionListScroller" class="sessions-provider-list">
        <div v-if="toolPrefsLoaded && providers.length === 0" class="sessions-provider-list__empty">
          <p>{{ t("sessions.toolSettings.noTools") }}</p>
          <el-button size="small" :icon="Setting" @click="openToolSettings">
            {{ t("sessions.toolSettings.open") }}
          </el-button>
        </div>
        <section v-for="provider in providers" :key="provider.id" class="sessions-provider">
          <header class="sessions-provider__header">
            <button
              type="button"
              class="sessions-provider__toggle"
              @click="openProviders[provider.id] = !openProviders[provider.id]"
            >
              <AppChevron :direction="openProviders[provider.id] ? 'down' : 'right'" />
              <el-icon class="sessions-provider__nav-icon" :class="providerClass(provider.id)">
                <component :is="provider.navIcon" />
              </el-icon>
              <strong>{{ providerLabel(provider) }}</strong>
              <small>{{ providerSessionCounts[provider.id] }}</small>
            </button>
            <el-tooltip :content="t('sessions.newCliTerminalHint', { provider: providerLabel(provider) })" placement="top">
              <el-button
                text
                class="sessions-icon-button sessions-provider__new"
                :icon="Plus"
                :title="t('sessions.newCliTerminal', { provider: providerLabel(provider) })"
                :aria-label="t('sessions.newCliTerminal', { provider: providerLabel(provider) })"
                @click="createProviderTerminal(provider.id)"
              />
            </el-tooltip>
            <el-button
              text
              class="sessions-icon-button sessions-provider__refresh"
              :icon="Refresh"
              :loading="providerLoading[provider.id]"
              :title="t('sessions.refreshGroup')"
              :aria-label="t('sessions.refreshGroup')"
              @click="loadProvider(provider)"
            />
          </header>

          <div v-if="openProviders[provider.id]" class="sessions-provider__content">
            <template v-if="provider.id === 'claude'">
              <div
                v-for="project in visibleClaudeProjects"
                :key="project.encodedDir"
                class="sessions-project"
              >
                <div
                  class="sessions-project__row"
                  @contextmenu="onProjectContextMenu($event, project)"
                >
                  <button
                    type="button"
                    class="sessions-project__toggle"
                    @click="toggleProject(project)"
                  >
                    <AppChevron :direction="openProjects[project.encodedDir] ? 'down' : 'right'" />
                    <el-icon><FolderOpened /></el-icon>
                    <span>
                      <strong>{{ shortPath(project.decodedPath) }}</strong>
                      <small :title="project.decodedPath">{{ project.decodedPath }}</small>
                    </span>
                    <em>{{ project.sessionCount }}</em>
                  </button>
                  <el-tooltip
                    :content="t('sessions.newProjectCliTerminalHint', {
                      provider: providerLabel(provider),
                    })"
                    placement="top"
                  >
                    <el-button
                      text
                      class="sessions-icon-button sessions-project__new-terminal"
                      :icon="Plus"
                      :aria-label="t('sessions.newProjectCliTerminal', {
                        provider: providerLabel(provider),
                      })"
                      @click="createProviderTerminal(provider.id, project.decodedPath)"
                    />
                  </el-tooltip>
                </div>

                <div v-if="openProjects[project.encodedDir]" class="sessions-project__sessions" v-bind="{ [ROW_GROUP_ATTRIBUTE]: `claude:${project.encodedDir}` }">
                  <div
                    v-if="sessionRows.windowOf(`claude:${project.encodedDir}`, visibleClaudeProjectSessions[project.encodedDir] ?? []).paddingTop"
                    aria-hidden="true"
                    :style="{ height: `${sessionRows.windowOf(`claude:${project.encodedDir}`, visibleClaudeProjectSessions[project.encodedDir] ?? []).paddingTop}px` }"
                  />
                  <article
                    v-for="session in sessionRows.windowOf(`claude:${project.encodedDir}`, visibleClaudeProjectSessions[project.encodedDir] ?? []).rows"
                    :key="sessionId(session)"
                    class="sessions-session-row"
                    :class="{ 'is-active': isSelected('claude', session) }"
                  >
                    <button
                      type="button"
                      class="sessions-session-row__main"
                      @click="openSessionInEmbeddedTerminal('claude', session)"
                    >
                      <span class="sessions-session-row__title">
                        <span
                          v-if="session.status"
                          class="sessions-status-dot"
                          :class="`is-${session.status}`"
                          role="img"
                          :title="sessionStatusLabel(session.status)"
                          :aria-label="sessionStatusLabel(session.status)"
                        />
                        <strong :title="session.title">{{
                          session.title || sessionId(session)
                        }}</strong>
                        <TerminalAttentionMarker
                          v-if="sessionAttention('claude', session)"
                          :attention="sessionAttention('claude', session)!"
                        />
                      </span>
                      <small>{{
                        session.model ||
                        shortPath(session.decodedPath) ||
                        t("sessions.unknownModel")
                      }}</small>
                      <span
                        >{{ session.messageCount }} {{ t("sessions.messageCount") }} ·
                        {{ formatTime(session.lastAt) }}</span
                      >
                    </button>
                    <button
                      type="button"
                      class="sessions-row-menu"
                      :aria-label="t('sessions.sessionActions')"
                      aria-haspopup="menu"
                      @click.stop="openSessionMenu($event, 'claude', session)"
                    ><el-icon><MoreFilled /></el-icon></button>
                  </article>
                  <div
                    v-if="sessionRows.windowOf(`claude:${project.encodedDir}`, visibleClaudeProjectSessions[project.encodedDir] ?? []).paddingBottom"
                    aria-hidden="true"
                    :style="{ height: `${sessionRows.windowOf(`claude:${project.encodedDir}`, visibleClaudeProjectSessions[project.encodedDir] ?? []).paddingBottom}px` }"
                  />
                </div>
              </div>
              <p v-if="!visibleClaudeProjects.length" class="sessions-provider-empty">
                {{ t("sessions.noSessions") }}
              </p>
            </template>

            <template v-else>
              <div
                v-for="group in visibleProviderProjectGroups(provider.id)"
                :key="group.key"
                class="sessions-project"
              >
                <div class="sessions-project__row">
                  <button
                    type="button"
                    class="sessions-project__toggle"
                    @click="openProviderProjects[group.key] = !openProviderProjects[group.key]"
                  >
                    <AppChevron :direction="openProviderProjects[group.key] ? 'down' : 'right'" />
                    <el-icon><FolderOpened /></el-icon>
                    <span>
                      <strong>{{ providerProjectLabel(group) }}</strong>
                      <small :title="group.path">{{ group.path || t("sessions.unknownProject") }}</small>
                    </span>
                    <em>{{ group.sessions.length }}</em>
                  </button>
                  <el-tooltip
                    v-if="group.path"
                    :content="t('sessions.newProjectCliTerminalHint', {
                      provider: providerLabel(provider),
                    })"
                    placement="top"
                  >
                    <el-button
                      text
                      class="sessions-icon-button sessions-project__new-terminal"
                      :icon="Plus"
                      :aria-label="t('sessions.newProjectCliTerminal', {
                        provider: providerLabel(provider),
                      })"
                      @click="createProviderTerminal(provider.id, group.path)"
                    />
                  </el-tooltip>
                </div>

                <div v-if="openProviderProjects[group.key]" class="sessions-project__sessions" v-bind="{ [ROW_GROUP_ATTRIBUTE]: group.key }">
                  <div
                    v-if="sessionRows.windowOf(group.key, group.visibleSessions).paddingTop"
                    aria-hidden="true"
                    :style="{ height: `${sessionRows.windowOf(group.key, group.visibleSessions).paddingTop}px` }"
                  />
                  <article
                    v-for="session in sessionRows.windowOf(group.key, group.visibleSessions).rows"
                    :key="sessionId(session)"
                    class="sessions-session-row"
                    :class="{ 'is-active': isSelected(provider.id, session) }"
                  >
                    <button
                      type="button"
                      class="sessions-session-row__main"
                      @click="openSessionInEmbeddedTerminal(provider.id, session)"
                    >
                      <span class="sessions-session-row__title">
                        <span
                          v-if="session.status"
                          class="sessions-status-dot"
                          :class="`is-${session.status}`"
                          role="img"
                          :title="sessionStatusLabel(session.status)"
                          :aria-label="sessionStatusLabel(session.status)"
                        />
                        <strong :title="session.title">{{ session.title || sessionId(session) }}</strong>
                        <TerminalAttentionMarker
                          v-if="sessionAttention(provider.id, session)"
                          :attention="sessionAttention(provider.id, session)!"
                        />
                      </span>
                      <small>{{ session.model || t("sessions.unknownModel") }}</small>
                      <span
                        >{{ session.messageCount }} {{ t("sessions.messageCount") }} ·
                        {{ formatTime(session.lastAt) }}</span
                      >
                    </button>
                    <button
                      type="button"
                      class="sessions-row-menu"
                      :aria-label="t('sessions.sessionActions')"
                      aria-haspopup="menu"
                      @click.stop="openSessionMenu($event, provider.id, session)"
                    ><el-icon><MoreFilled /></el-icon></button>
                  </article>
                  <div
                    v-if="sessionRows.windowOf(group.key, group.visibleSessions).paddingBottom"
                    aria-hidden="true"
                    :style="{ height: `${sessionRows.windowOf(group.key, group.visibleSessions).paddingBottom}px` }"
                  />
                </div>
              </div>
              <p
                v-if="!visibleProviderProjectGroups(provider.id).length"
                class="sessions-provider-empty"
              >
                {{ t("sessions.noSessions") }}
              </p>
            </template>
          </div>
        </section>
        <PluginSessionSources
          v-if="pluginSessionSources.length"
          :sources="pluginSessionSources"
          scope="all"
          :selected-key="selected?.provider"
          :selected-session-id="selectedIsPlugin ? selectedSessionId : undefined"
          :refresh-token="pluginRefreshToken"
          :query="search"
          @open="openPluginSessionFromList"
        />
      </el-scrollbar>
        </aside>
      </template>
      <template #second>
        <main class="sessions-conversation">
      <template v-if="selected">
        <header class="sessions-conversation__header">
          <div class="sessions-conversation__identity">
            <el-tag
              class="sessions-provider__mark"
              :class="providerClass(selected.provider)"
              effect="plain"
              disable-transitions
            >{{ providerMark(selected.provider) }}</el-tag>
            <div>
              <div class="sessions-conversation__title-line">
                <strong :title="selected.session.title">{{
                  selected.session.title || selectedSessionId
                }}</strong>
                <el-tag class="sessions-provider-badge" effect="plain" disable-transitions>
                  {{ providerLabel(selected.provider) }}
                </el-tag>
                <el-tag
                  v-if="selectedIsPlugin"
                  class="sessions-provider-badge"
                  effect="plain"
                  type="info"
                  disable-transitions
                  :title="t('sessions.pluginSessionReadOnly')"
                >{{ t("sessions.pluginSourceBadge") }}</el-tag>
              </div>
              <div class="sessions-conversation__subline">
                <span
                  v-if="selectedPath"
                  class="sessions-conversation__path"
                  :title="selectedPath"
                  >{{ selectedPath }}</span
                >
                <span v-if="selectedIsNew" class="sessions-conversation__meta">
                  {{ t("sessions.newCliSessionHint") }}
                </span>
                <span v-else-if="selectedIsPlugin" class="sessions-conversation__meta">
                  {{ selectedPluginMeta }}
                </span>
                <span v-else class="sessions-conversation__meta">
                  {{ selected.session.model || t("sessions.unknownModel") }} ·
                  {{ selected.session.messageCount }} {{ t("sessions.messageCount") }} ·
                  {{ formatTime(selected.session.lastAt) }}
                </span>
              </div>
            </div>
          </div>
          <div class="sessions-conversation__actions">
            <!-- Plugin sessions are read only: no resume, terminal, copy-command or AI actions. -->
            <template v-if="!selectedIsNew && !selectedIsPlugin">
              <el-tooltip :content="t('sessions.summary')" placement="bottom">
                <el-button
                  text
                  class="sessions-icon-action"
                  :icon="Document"
                  :aria-label="t('sessions.summary')"
                  @click="askAssistant('summary')"
                />
              </el-tooltip>
              <el-tooltip :content="t('sessions.todo')" placement="bottom">
                <el-button
                  text
                  class="sessions-icon-action"
                  :icon="Finished"
                  :aria-label="t('sessions.todo')"
                  @click="askAssistant('todo')"
                />
              </el-tooltip>
              <el-dropdown trigger="click" placement="bottom-end">
                <el-button text class="sessions-secondary-button" :icon="CopyDocument">
                  <span>{{ t("sessions.copyCommand") }}</span>
                </el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item :disabled="!selectedPath" @click="copyPath(selectedPath)">
                      {{ t("sessions.copyPath") }}
                    </el-dropdown-item>
                    <el-dropdown-item
                      :disabled="!selectedPath"
                      @click="copyCommand(buildCdCommand(selectedPath))"
                      >{{ t("sessions.copyCdCommand") }}</el-dropdown-item
                    >
                    <el-dropdown-item @click="copyCommand(selectedCopyLaunchCommand)">{{
                      t("sessions.copyLaunchCommand")
                    }}</el-dropdown-item>
                    <el-dropdown-item @click="copyCommand(selectedFullCommand)">{{
                      t("sessions.copyFullCommand")
                    }}</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
              <el-tooltip :content="t('sessions.openInNewTerminal')" placement="bottom">
                <el-button
                  text
                  class="sessions-icon-action"
                  :icon="TopRight"
                  :aria-label="t('sessions.openInNewTerminal')"
                  @click="resumeSelectedSession"
                />
              </el-tooltip>
              <el-tooltip
                :content="
                  activePanel === 'terminal' && liveTerminals.length > 0
                    ? t('sessions.showConversation')
                    : t('sessions.openTerminalHint', {
                        provider: providerLabel(selected.provider),
                      })
                "
                placement="bottom"
              >
                <el-button
                  v-if="activePanel === 'terminal' && liveTerminals.length > 0"
                  type="primary"
                  plain
                  class="sessions-primary-button"
                  :icon="Document"
                  @click="showConversation"
                >
                  <span>{{ t("sessions.showConversation") }}</span>
                </el-button>
                <el-button
                  v-else
                  type="primary"
                  plain
                  class="sessions-primary-button"
                  :icon="Monitor"
                  @click="openEmbeddedTerminal"
                >
                  <span>{{ t("sessions.openTerminal") }}</span>
                </el-button>
              </el-tooltip>
            </template>
          </div>
        </header>

        <el-scrollbar
          v-show="activePanel === 'conversation'"
          ref="messageListElement"
          class="sessions-conversation__messages"
        >
          <div
            v-if="messageLoading"
            class="sessions-conversation-loading"
            role="status"
            aria-live="polite"
            :aria-label="t('common.loading')"
          >
            <el-skeleton animated :rows="6" />
          </div>
          <template v-else-if="selectedIsPlugin">
            <div v-if="pluginMessageError" class="app-plugin-session-error" role="alert">
              <span>{{ t("sessions.pluginMessagesError", { error: pluginMessageError }) }}</span>
              <el-button size="small" @click="retryPluginMessages">{{ t("sessions.pluginSourceRetry") }}</el-button>
            </div>
            <!-- Untrusted plugin text: plain text only, never MarkdownPreview. -->
            <PluginSessionTranscript v-else :messages="pluginMessages" :truncated="pluginMessagesTruncated" />
          </template>
          <template v-else>
            <div class="sessions-message-stream">
              <div v-if="visibleMessageStart > 0" class="sessions-message-stream__earlier">
                <el-button text size="small" @click="loadEarlierMessages">
                  {{ t("ai.loadEarlierMessages") }}
                </el-button>
              </div>
              <article
                v-for="{ message, index } in visibleMessages"
                :key="`${message.timestamp || index}-${message.role || message.msgType || 'message'}`"
                class="sessions-message"
                :class="`is-${sessionRole(message)}`"
              >
                <header>
                  <span>{{ sessionRoleLabel(message) }}</span>
                  <small>{{ message.model || formatMessageTime(message.timestamp) }}</small>
                </header>
                <MarkdownPreview v-if="message.content" :content="message.content" inline />
                <p v-else class="sessions-message__empty">{{ t("sessions.emptyMessage") }}</p>
              </article>
            </div>
            <div v-if="!messages.length" class="sessions-conversation-empty is-inline">
              <el-icon :size="34"><Document /></el-icon>
              <strong>{{ t("sessions.noMessages") }}</strong>
            </div>
          </template>
        </el-scrollbar>

        <!-- 已打开的终端始终保持挂载，仅隐藏非活动实例，避免切换会话时销毁 xterm / PTY。 -->
        <section v-show="activePanel === 'terminal'" class="sessions-live-terminal">
          <div v-if="liveTerminals.length > 0" class="sessions-live-tab-bar">
            <div ref="tabBarScrollEl" class="sessions-live-tab-bar__scroll">
              <el-dropdown
                v-for="entry in liveTerminals"
                :key="entry.key"
                class="sessions-live-tab-menu"
                trigger="contextmenu"
                placement="bottom-start"
                @command="handleLiveTerminalCommand(entry.key, $event)"
              >
                <div
                  class="sessions-live-tab"
                  :class="{ 'is-active': entry.key === activeLiveTerminalKey }"
                >
                  <button
                    type="button"
                    class="sessions-live-tab__select"
                    @click="activateLiveTerminal(entry)"
                    @dblclick.stop="renameLiveTerminal(entry.key)"
                  >
                    <el-tag
                      class="sessions-live-tab__mark"
                      :class="providerClass(entry.provider)"
                      effect="plain"
                      size="small"
                      disable-transitions
                    >{{ providerMark(entry.provider) }}</el-tag>
                    <span class="sessions-live-tab__label">{{ entry.label }}</span>
                  </button>
                  <TerminalAttentionMarker v-if="liveTabAttention(entry)" :attention="liveTabAttention(entry)!" />
                  <SessionLiveTabActivity
                    :activities="entry.paneActivities"
                    :created-at="entry.createdAt"
                  />
                  <button
                    type="button"
                    class="sessions-live-tab__close"
                    :aria-label="t('common.close')"
                    @click="closeLiveTerminal(entry.key)"
                  ><el-icon><Close /></el-icon></button>
                </div>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="rename">
                      <el-icon><EditPen /></el-icon>{{ t("sessions.renameTerminal") }}
                    </el-dropdown-item>
                    <el-dropdown-item
                      command="restore-auto-label"
                      :disabled="!entry.manualLabelOverride"
                    >
                      <el-icon><Refresh /></el-icon>{{ t("sessions.restoreAutomaticTerminalName") }}
                    </el-dropdown-item>
                    <el-dropdown-item command="close-left" divided :disabled="entry.key === liveTerminals[0]?.key">
                      {{ t("tab.closeLeft") }}
                    </el-dropdown-item>
                    <el-dropdown-item command="close-right" :disabled="entry.key === liveTerminals[liveTerminals.length - 1]?.key">
                      {{ t("tab.closeRight") }}
                    </el-dropdown-item>
                    <el-dropdown-item command="close-others" :disabled="liveTerminals.length < 2">
                      {{ t("tab.closeOther") }}
                    </el-dropdown-item>
                    <el-dropdown-item command="close-all">
                      {{ t("tab.closeAll") }}
                    </el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
            <el-tooltip :content="t('sessions.newTerminalHint')" placement="bottom">
              <el-button
                text
                class="sessions-live-tab-bar__new"
                :icon="Plus"
                :aria-label="t('sessions.newTerminal')"
                @click="openNewEmbeddedTerminal"
              />
            </el-tooltip>
          </div>
          <div class="sessions-live-terminal__viewport">
            <div
              v-for="entry in liveTerminals"
              :key="entry.key"
              v-show="entry.key === activeLiveTerminalKey"
              class="sessions-live-terminal__pane"
            >
              <TerminalPaneLayout
                :node="entry.paneRoot"
                :active-pane-id="activePanel === 'terminal' && entry.key === activeLiveTerminalKey ? entry.activePaneId : ''"
                :can-close="collectLeafIds(entry.paneRoot).length > 1"
                :can-split="true"
                @select="selectSessionPane(entry.key, $event)"
                @close="closeSessionPane(entry.key, $event)"
                @use-default-shell="useDefaultShellInSessionPane(entry.key, $event)"
                @split="(paneId, direction) => splitSessionPane(entry.key, paneId, direction)"
                @resize="(splitId, sizes) => resizeSessionSplit(entry.key, splitId, sizes)"
                @resize-end="() => {}"
                @activity="(paneId, snapshot) => updateLivePaneActivity(entry.key, paneId, snapshot)"
              />
            </div>
          </div>
        </section>
      </template>

      <div v-else class="sessions-conversation-empty">
        <span class="sessions-conversation-empty__icon"><Document /></span>
        <strong>{{ t("sessions.selectHint") }}</strong>
        <p>{{ t("sessions.selectHintDetail") }}</p>
      </div>
        </main>
      </template>
    </AppSplitPane>

    <AppContextMenu
      v-if="projectContextMenu"
      :open="true"
      :x="projectContextMenu.x"
      :y="projectContextMenu.y"
      @close="closeProjectContextMenu"
    >
      <el-dropdown-item @click="openProjectTerminal(projectContextMenu.project)">
        <el-icon><Monitor /></el-icon>{{ t("sessions.openProjectTerminal") }}
      </el-dropdown-item>
    </AppContextMenu>

    <AppContextMenu
      v-if="sessionMenu"
      :open="true"
      :x="sessionMenu.x"
      :y="sessionMenu.y"
      @close="sessionMenu = null"
    >
      <el-dropdown-item data-session-command="terminal" @click="runSessionMenuCommand('terminal')">
        <el-icon><Monitor /></el-icon>{{ t("sessions.openTerminal") }}
      </el-dropdown-item>
      <el-dropdown-item data-session-command="open" @click="runSessionMenuCommand('open')">
        <el-icon><Document /></el-icon>{{ t("sessions.openPreview") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="sessionMenu.provider === 'claude'" data-session-command="rename" @click="runSessionMenuCommand('rename')">
        <el-icon><EditPen /></el-icon>{{ t("sessions.rename") }}
      </el-dropdown-item>
      <el-dropdown-item divided data-session-command="delete" @click="runSessionMenuCommand('delete')">
        <el-icon><Delete /></el-icon>{{ t("common.delete") }}
      </el-dropdown-item>
    </AppContextMenu>

    <SessionToolSettingsDialog v-if="toolSettingsMounted" v-model="toolSettingsOpen" />
  </section>
</template>

<style scoped>
.sessions-message-stream__earlier {
  display: flex;
  justify-content: center;
  margin-bottom: 13px;
}
</style>
