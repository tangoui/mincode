<script setup lang="ts">
import AgentControlPanel from "@/components/settings/AgentControlPanel.vue";
import { defaultProfile, defaultProviderPreset, defaultSettings, providerPresets, type AcpAgent, type AiProfile, type AiProvider, type AiProviderPreset, type AiRule, type McpServer, type VueSettings } from "@/services/settings-model";
import "@/styles/workspaces/settings/workspace.css";
import "@/styles/workspaces/settings/dialog.css";
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import MinCodeMark from "@/assets/brand/mincode-mark.svg";
import AiConversationSidebarPosition from "@/components/ai/AiConversationSidebarPosition.vue";
import SettingsNavigation from "@/components/settings/SettingsNavigation.vue";
import GeneralSettingsPanel from "@/components/settings/GeneralSettingsPanel.vue";
import TerminalSettingsPanel from "@/components/settings/TerminalSettingsPanel.vue";
import DataStorageUsagePanel from "@/components/settings/DataStorageUsagePanel.vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import {
  ArrowDown,
  ArrowRight,
  Check,
  CircleCheck,
  Close,
  CopyDocument,
  DataAnalysis,
  DataBoard,
  Delete,
  Document,
  Download,
  EditPen,
  FullScreen,
  Lock,
  Postcard,
  MagicStick,
  Monitor,
  Plus,
  Reading,
  Refresh,
  ScaleToOriginal,
  Setting,
  TopRight,
  Warning,
} from "@element-plus/icons-vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { settingsNavigationRequest } from "@/services/settings-navigation";
import { useCommandPaletteStore } from "@/stores/command-palette";
import {
  canInvoke,
  chooseNativeFile,
  isBrowserLocalMode,
  isDesktopRuntime,
  nativeErrorMessage,
  openExternalUrl,
} from "@/services/desktop-bridge";
import { notifyApp } from "@/services/notifications";
import { openSystemBrowser } from "@/services/web-automation-service";
import {
  chooseSettingsDirectory,
  chooseSettingsJsonFile,
  connectMcpServer,
  disconnectMcpServer,
  fetchAiModels,
  testAiConnection,
  getAppVersion,
  getRuntimePlatform,
  getAutostartEnabled,
  getUserHomeDirectory,
  loadProtocolStatuses,
  readSettingsTextFile,
  setAutostart,
  setMinimizeToTray,
  type ProtocolStatus as McpStatus,
  type UpdateInfo,
} from "@/services/settings-service";
import {
  findMcpPathPlaceholder,
  getBuiltinMcpCatalog,
  keepSyncMetadata,
  loadMcpRegistryEntry,
  mergeMcpServersById,
  normalizeDocumentationUrl,
  normalizeStoredMcpServer,
  parseMcpConfig,
  parseEditableMcpConfig,
  resolveMcpPathPlaceholder,
  serializeEditableMcpConfig,
  searchMcpRegistry,
  withoutSyncMetadata,
  type RegistryEntry,
} from "@/domain/ai/settings-integrations";
import { normalizeAiApiFormat, type AiApiFormat } from "@/domain/ai/api-format";
import { normalizeAnthropicAuth } from "@/domain/ai/anthropic-auth";
import {
  applicableAiRules,
  type AiRuleScope,
} from "@/domain/ai/rules";
import { DEFAULT_EDITOR_PREFERENCES } from "@/domain/projects/types";
import {
    AI_REASONING_EFFORT_CHOICES,
  removeAiProfileModel,
  withoutRetiredAiModels,
  type AiReasoningEffortChoice,
} from "@/domain/ai/model-selection";
import {
  MAX_AI_MAX_LOOPS,
  MIN_AI_MAX_LOOPS,
  normalizeAiMaxLoops,
} from "@/domain/ai/agent-loop-limit";
import { validateAiProviderAuth } from "@/domain/ai/provider-auth";
import { ACP_BUILTIN_AGENTS, acpBuiltinAgentCommandLine } from "@/domain/ai/acp-agents";
import { entryAvatarInitial, entryAvatarTone, type EntryAvatarTone } from "@/domain/settings/entry-avatar";
import {
  DEFAULT_AI_APPROVAL_MODE,
  normalizeAiApprovalMode,
  type AiApprovalMode,
} from "@/domain/ai/approval-policy";
import type { GlobalToolAuthorization } from "@/domain/ai/tool-authorization";
import {
  aiApprovalMode,
  hydrateAiApprovalMode,
  saveAiApprovalMode,
} from "@/services/ai-approval-policy";
import {
  clearGlobalToolAuthorizations,
  globalToolAuthorizations,
  loadGlobalToolAuthorizations,
  revokeGlobalToolAuthorization,
} from "@/services/tool-authorization-persistence";
import {
  applyConfigImport,
  buildConfigExport,
  parseConfigImport,
  type ParsedConfigImport,
} from "@/services/config-portability";
import { useAppUpdateStore } from "@/stores/app-update";
import AppUpdateNotes from "@/components/settings/AppUpdateNotes.vue";
import {
  confirmSecretExport,
  confirmSecretImport,
} from "@/composables/use-secret-export-confirm";
import {
  hydrateSettingsSnapshot,
  settingsToolChange,
  persistSettingsSnapshot,
  readSettingsSnapshot,
} from "@/services/settings-persistence";
import { saveTextFileToDisk } from "@/services/file-download";
import {
  normalizeWorkspaceCacheLimit,
} from "@/domain/workspace/state-scope";
import {
  resolveRuntimePerformanceSettings,
} from "@/domain/performance/runtime-limits";
import {
  createDefaultSidebarVisibility,
  loadSidebarVisibility,
  saveSidebarVisibility,
  type SidebarItemId,
  type SidebarVisibility,
} from "@/services/sidebar-preferences";
import { applyTerminalColorScheme } from "@/services/terminal-theme";
import { applyEditorThemePreference } from "@/services/editor-theme";
import { normalizeEditorThemePreference } from "@/domain/themes/editor-theme-id";
import { normalizeCustomEditorTheme } from "@/domain/themes/editor-theme-data";
import {
  applyUiAppearance,
  legacyFontSizeToUiScale,
  normalizeUiFontFamily,
  normalizeUiScale,
} from "@/services/ui-appearance";
import { applyUiTheme, readStoredUiTheme } from "@/services/ui-theme";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { useAiAssistantStore, type AiAssistantPlacement } from "@/stores/ai-assistant";
import {
  useAiConversationPreferencesStore,
  type AiConversationScopeMode,
} from "@/stores/ai-conversation-preferences";

// Monaco is several MB; load it only when one of the JSON/Skill editor dialogs opens.
const MonacoTextEditor = defineAsyncComponent(() => import("@/components/ui/MonacoTextEditor.vue"));
// The keybinding table (resolver, presets, recorder) loads only when the shortcuts page opens.
const KeybindingSettingsPanel = defineAsyncComponent(() => import("@/components/settings/KeybindingSettingsPanel.vue"));
const DatabaseSafetySettingsSection = defineAsyncComponent(() => import("@/components/settings/DatabaseSafetySettingsSection.vue"));

type Category =
  | "general"
  | "terminal"
  | "shortcuts"
  | "ai"
  | "database"
  | "data"
  | "about";
type AiTab = "connection" | "permissions" | "mcp" | "rules" | "local-agent";
type AiRegistrySubTab = "installed" | "discover";

const builtinMcpTemplates = getBuiltinMcpCatalog();

const defaults = { ...defaultSettings, uiTheme: readStoredUiTheme() };
const props = defineProps<{ modelValue: boolean; initialCategory?: Category; updateRequest?: number }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const fullscreen = ref(false);
const importInput = ref<HTMLInputElement>();
const { t, locale, setLocale, intlLocale } = useVueI18n();
const commandPalette = useCommandPaletteStore();
const aiAssistant = useAiAssistantStore();
const aiConversationPreferences = useAiConversationPreferencesStore();
void aiConversationPreferences.hydrate();
void commandPalette.hydrate();
const workbench = useWorkbenchStore();
const workspaceStatePreferences = useWorkspaceStatePreferencesStore();
void workspaceStatePreferences.hydrate();
const active = ref<Category>("general");
function selectCategory(category: Category) {
  active.value = category;
}
const aiTab = ref<AiTab>("connection");
const approvalMode = ref<AiApprovalMode>(DEFAULT_AI_APPROVAL_MODE);
const approvalSaving = ref(false);
const authorizationUpdating = ref(false);
const version = ref("—");
const platform = ref(isDesktopRuntime() ? "—" : "Web");

const appUpdate = useAppUpdateStore();
void appUpdate.hydrate();
const updateInfo = computed<UpdateInfo | undefined>(() => appUpdate.info);
const updateChecking = computed(() => appUpdate.checking);
const updateDownloading = computed(() => appUpdate.downloading);
const updateError = ref("");
const mcpStatuses = ref<McpStatus[]>([]);
const acpStatuses = ref<McpStatus[]>([]);
const mcpSubTab = ref<AiRegistrySubTab>("installed");
const mcpAdvanced = ref(false);
const aiAdvanced = ref(false);
const mcpJsonEditorOpen = ref(false);
const mcpJsonText = ref("");
const mcpSearch = ref("");
const mcpSearchResults = ref<RegistryEntry[]>([]);
const mcpSearching = ref(false);
const installingMcpId = ref<string>();
let mcpSearchController: AbortController | undefined;
const ruleScope = ref<AiRuleScope>("global");
const ruleWorkspaceId = ref(workbench.activeWorkspaceId);
async function setAiConversationScopeMode(value: AiConversationScopeMode) {
  await aiConversationPreferences.setScopeMode(value);
}

const ruleScopeOptions = computed(() => [
  { label: t("settings.rules.global"), value: "global" },
  { label: t("settings.rules.workspace"), value: "workspace" },
]);
// 连接器面板把「已安装」与「可发现」拆成内部子 tab，避免已安装列表
// 和注册表搜索结果混在一屏里难以区分。
const installedSubTabOptions = computed(() => [
  { label: t("settings.ai.subtabInstalled"), value: "installed" },
  { label: t("settings.ai.subtabDiscover"), value: "discover" },
]);
const REGISTRY_SOURCE_LABELS: Record<RegistryEntry["source"], MessageKey> = {
  builtin: "settings.registry.source.builtin",
  git: "settings.registry.source.git",
  registry: "settings.registry.source.registry",
};
function registrySourceText(entry: RegistryEntry) {
  return t(REGISTRY_SOURCE_LABELS[entry.source]);
}
const ruleWorkspaceOptions = computed(() =>
  workbench.workspaces.map((workspace) => ({
    label: workspace.name,
    value: workspace.id,
  })),
);
function availableRuleWorkspaceId(workspaceId?: string) {
  if (workspaceId && workbench.workspaces.some((workspace) => workspace.id === workspaceId)) {
    return workspaceId;
  }
  if (workbench.workspaces.some((workspace) => workspace.id === ruleWorkspaceId.value)) {
    return ruleWorkspaceId.value;
  }
  return workbench.activeWorkspaceId || workbench.workspaces[0]?.id || "";
}
function formatUpdateDate(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(intlLocale.value, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}
function formatUpdateSize(value?: number) {
  if (value === undefined) return "—";
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB"];
  let size = value / 1024;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  return `${size.toFixed(size >= 100 ? 0 : size >= 10 ? 1 : 2)} ${units[unitIndex]}`;
}

const ruleEditorOpen = ref(false);
const editingRuleId = ref<string>();
const manualModel = ref("");
const fetchingModels = ref(false);
const testingAiConnection = ref(false);
/** Browser-local mode: AI keys stay in this tab (sessionStorage) and requests go straight to the provider. */
const browserLocalMode = isBrowserLocalMode();
const ruleDraft = reactive({
  title: "",
  content: "",
});
const newAgent = reactive({ name: "", command: "", args: "" });
const newMcp = reactive<McpServer>({
  id: "",
  transport: "stdio",
  command: "",
  args: "",
  url: "",
  docsUrl: "",
  enabled: true,
  env: {},
  headers: {},
});

const aiApiFormatOptions: Array<{ value: AiApiFormat; label: MessageKey }> = [
  { value: "openai", label: "settings.ai.apiFormat.openai" },
  { value: "openai-responses", label: "settings.ai.apiFormat.responses" },
  { value: "anthropic", label: "settings.ai.apiFormat.anthropic" },
];

function createId(prefix: string) {
  return typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}
/** Models that replace a saved profile's retired ones: its provider preset, else the default preset. */
function retiredModelReplacement(provider: unknown) {
  const preset = providerPresets.find((item) => item.value === provider);
  return preset?.model ? preset : defaultProviderPreset;
}
function normalizeSettings(raw: Partial<VueSettings> & { fontSize?: unknown }): VueSettings {
  const { fontSize: legacyFontSize, ...settingsWithoutLegacyFontSize } = raw;
  return {
    ...defaults,
    ...settingsWithoutLegacyFontSize,
    uiScale: normalizeUiScale(
      raw.uiScale === undefined ? legacyFontSizeToUiScale(legacyFontSize) : raw.uiScale
    ),
    uiFontFamily: normalizeUiFontFamily(raw.uiFontFamily),
    aiMaxLoops: normalizeAiMaxLoops(raw.aiMaxLoops),
    aiProfiles:
      Array.isArray(raw.aiProfiles) && raw.aiProfiles.length
        ? raw.aiProfiles.map((profile) => withoutRetiredAiModels({
            ...defaultProfile,
            ...profile,
            apiFormat: normalizeAiApiFormat(profile.apiFormat, defaultProfile.apiFormat),
            anthropicAuth: normalizeAnthropicAuth(profile.anthropicAuth),
            openaiStreamUsage: ["auto", "always", "never"].includes(profile.openaiStreamUsage)
              ? profile.openaiStreamUsage
              : "auto",
            commonModels: Array.isArray(profile.commonModels)
              ? profile.commonModels.filter((model): model is string => typeof model === "string" && Boolean(model.trim()))
              : [profile.model || defaultProfile.model],
            failoverEnabled: profile.failoverEnabled === true,
            subagentModel:
              typeof profile.subagentModel === "string" ? profile.subagentModel.trim() : "",
            failoverModels: Array.isArray(profile.failoverModels)
              ? profile.failoverModels
                .filter((model): model is string => typeof model === "string" && Boolean(model.trim()))
                .map((model) => model.trim())
              : [],
            noToolModels: Array.isArray(profile.noToolModels)
              ? profile.noToolModels.filter((model): model is string => typeof model === "string" && Boolean(model.trim()))
              : [],
          }, retiredModelReplacement(profile.provider)))
        : [{ ...defaultProfile, commonModels: [...defaultProfile.commonModels] }],
    mcpServers: Array.isArray(raw.mcpServers)
      ? raw.mcpServers.map(normalizeStoredMcpServer)
      : [],
    aiRules: Array.isArray(raw.aiRules) ? raw.aiRules : [],
    aiReadProjectInstructions: raw.aiReadProjectInstructions !== false,
    aiSystemNotifications: raw.aiSystemNotifications !== false,
    acpAgents: Array.isArray(raw.acpAgents) ? raw.acpAgents : [],
    workspaceCacheLimit: normalizeWorkspaceCacheLimit(raw.workspaceCacheLimit),
    editorColorTheme: normalizeEditorThemePreference(raw.editorColorTheme),
    editorCustomTheme: normalizeCustomEditorTheme(raw.editorCustomTheme),
    ...resolveRuntimePerformanceSettings(raw),
  };
}
function loadSettings(): VueSettings {
  try {
    return normalizeSettings(
      readSettingsSnapshot<Partial<VueSettings> & Record<string, unknown>>()
    );
  } catch {
    return structuredClone(defaults);
  }
}
const settings = reactive<VueSettings>(loadSettings());
const visibleAiRules = computed(() =>
  applicableAiRules(settings.aiRules, availableRuleWorkspaceId(), ruleScope.value)
);
function ruleScopeChipLabel(rule: AiRule) {
  if (rule.scope !== "workspace") return t("settings.rules.global");
  return (
    ruleWorkspaceOptions.value.find((workspace) => workspace.value === rule.workspaceId)?.label ??
    t("settings.rules.workspace")
  );
}
const settingsHydrated = ref(false);
const sidebarVisibility = ref<SidebarVisibility>(createDefaultSidebarVisibility());
const activeProfile = computed(
  () =>
    settings.aiProfiles.find((profile) => profile.id === settings.activeAiProfileId) ??
    settings.aiProfiles[0]
);
const categoryList: Array<{ id: Category; label: MessageKey; icon: typeof Setting }> = [
  { id: "general", label: "settings.cat.general", icon: Setting },
  { id: "terminal", label: "settings.cat.terminal", icon: Monitor },
  { id: "shortcuts", label: "settings.cat.shortcuts", icon: Document },
  { id: "ai", label: "settings.cat.ai", icon: MagicStick },
  { id: "database", label: "settings.cat.database", icon: DataAnalysis },
  { id: "data", label: "settings.cat.data", icon: DataBoard },
  { id: "about", label: "settings.cat.about", icon: Postcard },
];
const categories = computed(() =>
  categoryList
    .map((item) => (item.id === "about" && appUpdate.pendingVersion ? { ...item, badge: true } : item))
);
async function skipUpdateVersion(version: string) {
  await appUpdate.skipVersion(version);
  ElMessage.success(t("settings.update.skipped", { version }));
}
// 用穷尽的 Record 而不是拼接键名：新增档位时这里会直接编译失败，而不是运行期落到缺失的文案。
const REASONING_EFFORT_LABELS: Record<AiReasoningEffortChoice, MessageKey> = {
  low: "settings.ai.reasoningLow",
  medium: "settings.ai.reasoningMedium",
  high: "settings.ai.reasoningHigh",
  xhigh: "settings.ai.reasoningXHigh",
  max: "settings.ai.reasoningMax",
  ultra: "settings.ai.reasoningUltra",
};
const reasoningEffortOptions = computed(() =>
  AI_REASONING_EFFORT_CHOICES.map((effort) => ({
    value: effort,
    label: t(REASONING_EFFORT_LABELS[effort]),
  }))
);
const aiTabs: Array<{ id: AiTab; label: MessageKey }> = [
  { id: "connection", label: "settings.ai.connection" },
  { id: "permissions", label: "settings.ai.permissions" },
  { id: "mcp", label: "settings.ai.mcp" },
  { id: "rules", label: "settings.ai.rules" },
  { id: "local-agent", label: "settings.ai.localAgent" },
];
function close() {
  emit("update:modelValue", false);
}
// 关闭设置时退出全屏，下次打开恢复默认悬浮尺寸。
watch(
  () => props.modelValue,
  (open) => {
    if (!open) fullscreen.value = false;
  }
);
let persistTimer: ReturnType<typeof setTimeout> | undefined;

function persist() {
  if (!settingsHydrated.value) return;
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = undefined;
  void persistSettingsSnapshot({ ...settings, locale: locale.value });
}
function schedulePersist() {
  if (!settingsHydrated.value) return;
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = setTimeout(persist, 300);
}
async function hydrateSettings() {
  const storedSidebarVisibility = await loadSidebarVisibility();
  sidebarVisibility.value = storedSidebarVisibility;
  let stored: unknown;
  try {
    let storedApprovalMode: Awaited<ReturnType<typeof hydrateAiApprovalMode>>;
    [stored, storedApprovalMode] = await Promise.all([
      hydrateSettingsSnapshot(),
      hydrateAiApprovalMode(),
      loadGlobalToolAuthorizations(),
    ]);
    if (stored && typeof stored === "object" && !Array.isArray(stored))
      Object.assign(settings, normalizeSettings(stored as Partial<VueSettings>));
    approvalMode.value = storedApprovalMode;
  } catch {
    settingsHydrated.value = true;
    ElMessage.error(t("settings.ai.credentialsLoadFailed"));
    return;
  }
  settings.locale = locale.value;
  // Let the deep settings watcher see the hydration writes while persistence is still off.
  await nextTick();
  settingsHydrated.value = true;
  const next = { ...settings };
  if (JSON.stringify(stored) !== JSON.stringify(next)) await persistSettingsSnapshot(next);
}
let hydration: Promise<void> | undefined;
function ensureHydrated() {
  hydration ??= hydrateSettings();
  return hydration;
}
async function updateWorkspaceStateIsolation(enabled: boolean) {
  await workspaceStatePreferences.setIsolationEnabled(enabled, workbench.activeWorkspaceId);
}
async function updateSidebarVisibility(id: SidebarItemId, visible: boolean) {
  const next = { ...sidebarVisibility.value, [id]: visible };
  sidebarVisibility.value = next;
  await saveSidebarVisibility(next);
}
let applyingToolSettings = false;
watch(settingsToolChange, async (keys) => {
  if (!settingsHydrated.value) return;
  const snapshot = normalizeSettings(readSettingsSnapshot());
  applyingToolSettings = true;
  for (const key of Object.keys(snapshot) as Array<keyof VueSettings>) {
    if (keys.includes(key)) Object.assign(settings, { [key]: snapshot[key] });
  }
  await nextTick();
  applyingToolSettings = false;
});
watch(settings, () => {
  if (!applyingToolSettings) schedulePersist();
}, { deep: true });
watch(locale, schedulePersist);
watch(aiApprovalMode, (mode) => {
  approvalMode.value = mode;
}, { immediate: true });
watch(() => settings.uiTheme, (theme) => applyUiTheme(theme), { immediate: true });
watch(
  [() => settings.uiScale, () => settings.uiFontFamily],
  ([uiScale, uiFontFamily]) => applyUiAppearance({ uiScale, uiFontFamily }),
  { immediate: true }
);
watch(() => settings.terminalColorScheme, applyTerminalColorScheme);
watch(
  [() => settings.editorColorTheme, () => settings.editorCustomTheme],
  ([theme, custom]) => applyEditorThemePreference(theme, custom),
  { deep: true }
);
watch(
  () => settings.minimizeToTray,
  (value) => {
    if (isDesktopRuntime()) void setMinimizeToTray(value);
  }
);
watch(
  () => settings.autostart,
  (value) => {
    if (isDesktopRuntime()) void setAutostart(value);
  }
);
watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      if (props.initialCategory) selectCategory(props.initialCategory);
      void ensureHydrated().catch(() => undefined).then(init);
    }
  },
  { immediate: true }
);
watch(
  () => props.initialCategory,
  (category) => {
    if (props.modelValue && category) selectCategory(category);
  }
);
watch(settingsNavigationRequest, (request) => {
  if (!props.modelValue || !request.category) return;
  selectCategory(request.category);
});
async function init() {
  if (settings.locale !== locale.value) setLocale(settings.locale);

  if (!isDesktopRuntime()) return;
  [version.value, platform.value] = await Promise.all([
    getAppVersion().catch(() => "—"),
    getRuntimePlatform().then(({ os, arch }) => `${os === "darwin" ? "macOS" : os} / ${arch}`).catch(() => "—"),
  ]);
  settings.autostart = await getAutostartEnabled().catch(() => settings.autostart);
  await loadProtocolStatus();
}
async function loadProtocolStatus() {
  if (!isDesktopRuntime()) return;
  const [mcp, acp] = await loadProtocolStatuses();
  mcpStatuses.value = mcp;
  acpStatuses.value = acp;
}
function configuredProfileModels(profile: AiProfile) {
  return [...new Set([profile.model, ...profile.commonModels]
    .filter((model): model is string => typeof model === "string")
    .map((model) => model.trim())
    .filter(Boolean))];
}
function fallbackModelOptions(profile: AiProfile) {
  return configuredProfileModels(profile);
}

function addAiProfile() {
  const profile: AiProfile = {
    ...defaultProfile,
    commonModels: [...defaultProfile.commonModels],
    failoverModels: [...defaultProfile.failoverModels],
    noToolModels: [],
    id: createId("profile"),
    name: `Profile ${settings.aiProfiles.length + 1}`,
  };
  settings.aiProfiles.push(profile);
  settings.activeAiProfileId = profile.id;
}
function selectAiProfile(id: string) {
  settings.activeAiProfileId = id;
}
function profileProviderLabel(profile: AiProfile) {
  if (profile.provider === "custom") return t("settings.ai.custom");
  return (
    providerPresets.find((item) => item.value === profile.provider)?.label ?? profile.provider
  );
}
function profileSubtitle(profile: AiProfile) {
  return profile.model || profile.baseUrl || profileProviderLabel(profile);
}
/** 紧凑档案卡只有一行副标题：模型与服务商合并显示。 */
function profileCompactSubtitle(profile: AiProfile) {
  const provider = profileProviderLabel(profile);
  const primary = profileSubtitle(profile);
  return primary === provider ? provider : `${primary} · ${provider}`;
}
function duplicateAiProfile(source: AiProfile) {
  const profile = {
    ...withoutSyncMetadata(source),
    commonModels: [...source.commonModels],
    failoverModels: [...source.failoverModels],
    noToolModels: [...source.noToolModels],
    id: createId("profile"),
    name: `${source.name} Copy`,
  };
  settings.aiProfiles.push(profile);
  settings.activeAiProfileId = profile.id;
}
function deleteAiProfile(profile: AiProfile) {
  if (settings.aiProfiles.length <= 1) return;
  const index = settings.aiProfiles.findIndex((item) => item.id === profile.id);
  if (index < 0) return;
  settings.aiProfiles.splice(index, 1);
  if (settings.activeAiProfileId === profile.id) {
    settings.activeAiProfileId = settings.aiProfiles[Math.max(0, index - 1)]!.id;
  }
}
function selectProvider(provider: AiProviderPreset["value"]) {
  const profile = activeProfile.value;
  if (!profile || !providerPresets.some((item) => item.value === provider)) return;
  profile.provider = provider;
  profile.noToolModels = [];
  applyProviderPreset(provider);
}
function modelOptionLabel(model: string) {
  return activeProfile.value?.noToolModels.includes(model)
    ? model + " · " + t("settings.ai.pluginProvider.chatOnly")
    : model;
}
function applyProviderPreset(provider: AiProvider) {
  if (!activeProfile.value) return;
  const preset = providerPresets.find((item) => item.value === provider);
  if (!preset || provider === "custom") return;
  Object.assign(activeProfile.value, {
    baseUrl: preset.baseUrl,
    apiKey: "",
    model: preset.model,
    apiFormat: preset.apiFormat,
    commonModels: [...preset.commonModels],
    failoverEnabled: false,
    failoverModels: [],
    subagentModel: "",
    contextWindow: preset.contextWindow,
    maxInputTokens: preset.maxInputTokens,
    maxOutputTokens: preset.maxOutputTokens,
  });
}
/** Preset models of the active profile's provider that are not in its list yet. */
const addableCommonModels = computed(() => {
  const profile = activeProfile.value;
  const preset = profile && providerPresets.find((item) => item.value === profile.provider);
  const models = preset?.commonModels ?? [];
  return profile ? models.filter((model) => !profile.commonModels.includes(model)) : [];
});
function addCommonModel(value?: string) {
  if (!activeProfile.value) return;
  const model = (value ?? manualModel.value).trim();
  if (!model || activeProfile.value.commonModels.includes(model)) return;
  activeProfile.value.commonModels.push(model);
  manualModel.value = "";
}
function removeCommonModel(model: string) {
  const profile = activeProfile.value;
  if (!profile) return;
  const next = removeAiProfileModel(profile, model);
  if (!next) {
    ElMessage.warning(t("settings.ai.testMissingModel"));
    return;
  }
  profile.model = next.model;
  profile.commonModels = next.commonModels;
  profile.failoverModels = next.failoverModels;
  if (profile.subagentModel === model) profile.subagentModel = "";
}
function activeAiAuthIsValid(profile: AiProfile) {
  const auth = validateAiProviderAuth(profile);
  if (auth.valid) return true;
  ElMessage.warning(t(auth.reason === "unresolved_base_url" ? "ai.unresolvedBaseUrl" : "settings.ai.missingApiKey"));
  return false;
}

async function fetchAvailableModels() {
  const profile = activeProfile.value;
  if (!profile) return;
  if (!profile.baseUrl.trim()) {
    ElMessage.warning(t("settings.ai.fetchModelsMissingBaseUrl"));
    return;
  }
  if (!activeAiAuthIsValid(profile)) return;
  if (!canInvoke("ai_list_models")) {
    ElMessage.warning(t("settings.ai.fetchModelsDesktopOnly"));
    return;
  }

  fetchingModels.value = true;
  try {
    const models = await fetchAiModels({
      baseUrl: profile.baseUrl,
      apiKey: profile.apiKey,
      apiFormat: profile.apiFormat,
      anthropicAuth: profile.anthropicAuth,
    });
    if (!models.length) {
      ElMessage.warning(t("settings.ai.fetchModelsEmpty"));
      return;
    }

    profile.commonModels = models;
    if (!models.includes(profile.model)) {
      profile.model = models[0]!;
    }
    profile.failoverModels = profile.failoverModels.filter((model) => models.includes(model));
    if (profile.subagentModel && !models.includes(profile.subagentModel)) {
      profile.subagentModel = "";
    }
    ElMessage.success(t("settings.ai.fetchModelsSuccess", { count: models.length }));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    ElMessage.error(t("settings.ai.fetchModelsFailed", { message }));
  } finally {
    fetchingModels.value = false;
  }
}

async function testActiveAiConnection() {
  const profile = activeProfile.value;
  if (!profile) return;
  if (!profile.baseUrl.trim()) {
    ElMessage.warning(t("settings.ai.fetchModelsMissingBaseUrl"));
    return;
  }
  if (!profile.model.trim()) {
    ElMessage.warning(t("settings.ai.testMissingModel"));
    return;
  }
  if (!activeAiAuthIsValid(profile)) return;
  if (!canInvoke("ai_test_connection")) {
    ElMessage.warning(t("settings.ai.fetchModelsDesktopOnly"));
    return;
  }

  testingAiConnection.value = true;
  try {
    await testAiConnection({
      baseUrl: profile.baseUrl,
      apiKey: profile.apiKey,
      apiFormat: profile.apiFormat,
      anthropicAuth: profile.anthropicAuth,
      model: profile.model,
    });
    ElMessage.success(t("settings.ai.connected"));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    ElMessage.error(t("settings.ai.testFailed", { message }));
  } finally {
    testingAiConnection.value = false;
  }
}

async function saveAiSettings() {
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = undefined;
  try {
    await persistSettingsSnapshot({ ...settings, locale: locale.value }, { strict: true });
    ElMessage.success(t("settings.ai.saved"));
  } catch (error) {
    ElMessage.error(t("settings.ai.saveFailed", { message: nativeErrorMessage(error) }));
  }
}
async function updateApprovalMode(value: unknown) {
  const mode = normalizeAiApprovalMode(value);
  if (approvalSaving.value || mode === aiApprovalMode.value) return;
  approvalSaving.value = true;
  try {
    await saveAiApprovalMode(mode);
    approvalMode.value = mode;
    ElMessage.success(t("settings.ai.permissionsSaved"));
  } catch (error) {
    approvalMode.value = aiApprovalMode.value;
    ElMessage.error(t("settings.ai.permissionsSaveFailed", { message: String(error) }));
  } finally {
    approvalSaving.value = false;
  }
}
function authorizationKey(authorization: GlobalToolAuthorization) {
  return `${authorization.toolName}:${authorization.resourceId}:${authorization.parameterFingerprint}:${authorization.risk}`;
}
function authorizationRiskLabel(risk: GlobalToolAuthorization["risk"]) {
  if (risk === "network") return t("ai.agentic.risk.network");
  if (risk === "write") return t("ai.agentic.risk.write");
  return t("ai.agentic.risk.read");
}
function authorizationRiskTone(risk: GlobalToolAuthorization["risk"]): EntryAvatarTone {
  if (risk === "network") return "warning";
  if (risk === "write") return "danger";
  return "success";
}
async function removeGlobalAuthorization(authorization: GlobalToolAuthorization) {
  if (authorizationUpdating.value) return;
  authorizationUpdating.value = true;
  try {
    await revokeGlobalToolAuthorization(authorization);
    ElMessage.success(t("settings.ai.authorizationRevoked"));
  } catch (error) {
    ElMessage.error(t("settings.ai.authorizationUpdateFailed", { message: String(error) }));
  } finally {
    authorizationUpdating.value = false;
  }
}
async function clearAllGlobalAuthorizations() {
  if (authorizationUpdating.value) return;
  authorizationUpdating.value = true;
  try {
    await clearGlobalToolAuthorizations();
    ElMessage.success(t("settings.ai.authorizationsCleared"));
  } catch (error) {
    ElMessage.error(t("settings.ai.authorizationUpdateFailed", { message: String(error) }));
  } finally {
    authorizationUpdating.value = false;
  }
}
function mergeMcpServers(servers: McpServer[]) {
  const existingIds = new Set(settings.mcpServers.map((server) => server.id));
  settings.mcpServers = mergeMcpServersById(settings.mcpServers, servers);
  return servers.filter((server) => !existingIds.has(server.id)).length;
}
async function readSelectedText(path: string) {
  return readSettingsTextFile(path);
}
function openMcpJsonEditor() {
  mcpJsonText.value = serializeEditableMcpConfig(settings.mcpServers);
  mcpJsonEditorOpen.value = true;
}
function formatMcpJson() {
  try {
    mcpJsonText.value = JSON.stringify(JSON.parse(mcpJsonText.value), null, 2);
  } catch (error) {
    ElMessage.error(t("settings.mcp.jsonInvalid", { message: nativeErrorMessage(error) }));
  }
}
async function saveMcpJson() {
  let servers: McpServer[];
  try {
    // The JSON holds only editable fields; the sync source of each connector is kept by id.
    servers = keepSyncMetadata(parseEditableMcpConfig(mcpJsonText.value), settings.mcpServers);
  } catch (error) {
    ElMessage.error(t("settings.mcp.jsonInvalid", { message: nativeErrorMessage(error) }));
    return;
  }

  const previousServers = settings.mcpServers;
  settings.mcpServers = servers;
  mcpJsonText.value = serializeEditableMcpConfig(servers);
  await nextTick();
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = undefined;
  try {
    await persistSettingsSnapshot({ ...settings, locale: locale.value }, { strict: true });
    ElMessage.success(t("settings.mcp.jsonSaved"));
    mcpJsonEditorOpen.value = false;
  } catch (error) {
    settings.mcpServers = previousServers;
    await nextTick();
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = undefined;
    ElMessage.error(t("settings.mcp.jsonSaveFailed", { message: nativeErrorMessage(error) }));
  }
}

async function importCursorMcp() {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  try {
    const home = await getUserHomeDirectory();
    const servers = parseMcpConfig(await readSelectedText(`${home}/.cursor/mcp.json`));
    ElMessage.success(t("settings.mcp.imported", { count: mergeMcpServers(servers) }));
  } catch (error) {
    ElMessage.error(t("settings.mcp.importFailed", { message: String(error) }));
  }
}
async function importMcpFile() {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  try {
    const path = await chooseSettingsJsonFile(t("settings.mcp.chooseJson"));
    if (!path) return;
    const servers = parseMcpConfig(await readSelectedText(path));
    ElMessage.success(t("settings.mcp.imported", { count: mergeMcpServers(servers) }));
  } catch (error) {
    ElMessage.error(t("settings.mcp.importFailed", { message: String(error) }));
  }
}
async function searchMcpServers() {
  mcpSearchController?.abort();
  const controller = new AbortController();
  mcpSearchController = controller;
  mcpSearching.value = true;
  try {
    const results = await searchMcpRegistry(mcpSearch.value);
    if (controller.signal.aborted) return;
    mcpSearchResults.value = results;
    if (!mcpSearchResults.value.length) ElMessage.info(t("settings.registry.empty"));
  } catch (error) {
    if (!controller.signal.aborted) ElMessage.error(t("settings.registry.searchFailed", { message: String(error) }));
  } finally {
    if (!controller.signal.aborted) mcpSearching.value = false;
  }
}
async function installMcpEntry(entry: RegistryEntry) {
  installingMcpId.value = entry.id;
  try {
    const servers = await loadMcpRegistryEntry(entry);
    const configured: McpServer[] = [];
    for (const server of servers) {
      const placeholder = findMcpPathPlaceholder(server);
      if (!placeholder) {
        configured.push(server);
        continue;
      }
      const selected = await chooseSettingsDirectory(
        t(placeholder === "<repository_path>" ? "settings.mcp.chooseRepository" : "settings.mcp.chooseDirectory")
      );
      if (!selected) return;
      configured.push(resolveMcpPathPlaceholder(server, selected));
    }
    const added = mergeMcpServers(configured);
    ElMessage.success(t("settings.mcp.imported", { count: added }));
  } catch (error) {
    ElMessage.error(t("settings.registry.installFailed", { message: String(error) }));
  } finally {
    installingMcpId.value = undefined;
  }
}
async function openDocumentation(url: string) {
  const safeUrl = normalizeDocumentationUrl(url);
  if (!safeUrl) return ElMessage.warning(t("settings.documentation.invalid"));
  try {
    if (isDesktopRuntime()) await openSystemBrowser(safeUrl);
    else window.open(safeUrl, "_blank", "noopener,noreferrer");
  } catch (error) {
    ElMessage.error(t("settings.documentation.openFailed", { message: String(error) }));
  }
}
function addMcpServer() {
  if (!newMcp.id.trim()) return ElMessage.warning(t("settings.mcp.idRequired"));
  const docsUrl = normalizeDocumentationUrl(newMcp.docsUrl);
  if (newMcp.docsUrl?.trim() && !docsUrl) {
    return ElMessage.warning(t("settings.documentation.invalid"));
  }
  settings.mcpServers = mergeMcpServersById(settings.mcpServers, [
    { ...newMcp, id: newMcp.id.trim(), docsUrl },
  ]);
  Object.assign(newMcp, {
    id: "",
    transport: "stdio",
    command: "",
    args: "",
    url: "",
    docsUrl: "",
    enabled: true,
    env: {},
    headers: {},
  });
}
async function connectMcp(server: McpServer) {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  try {
    await connectMcpServer(server);
    await loadProtocolStatus();
    ElMessage.success(t("settings.mcp.connected"));
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}
function mcpStatusText(server: McpServer) {
  const status = mcpStatus(server.id);
  return status?.error || status?.state || t("settings.mcp.notConnected");
}
async function disconnectMcp(server: McpServer) {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  try {
    await disconnectMcpServer(server.id);
    await loadProtocolStatus();
  } catch (error) {
    ElMessage.error(String(error));
  }
}
async function removeMcpServer(server: McpServer) {
  if (mcpStatus(server.id)?.state === "connected") await disconnectMcp(server);
  const index = settings.mcpServers.indexOf(server);
  if (index >= 0) settings.mcpServers.splice(index, 1);
}
function mcpStatus(id: string) {
  return mcpStatuses.value.find((item) => item.id === id);
}
function resetRuleDraft() {
  editingRuleId.value = undefined;
  ruleDraft.title = "";
  ruleDraft.content = "";
}
function openNewRuleEditor() {
  resetRuleDraft();
  ruleEditorOpen.value = true;
}
function openRuleEditor(rule: AiRule) {
  editingRuleId.value = rule.id;
  ruleDraft.title = rule.title;
  ruleDraft.content = rule.content;
  ruleEditorOpen.value = true;
}
function closeRuleEditor() {
  ruleEditorOpen.value = false;
  resetRuleDraft();
}
function saveRule() {
  const title = ruleDraft.title.trim();
  const content = ruleDraft.content.trim();
  if (!title || !content) return;
  // 规则作用域由顶部筛选器决定，编辑器内不再重复提供作用域控件。
  const workspaceId = ruleScope.value === "workspace" ? availableRuleWorkspaceId() : undefined;
  if (ruleScope.value === "workspace" && !workspaceId) return;

  if (editingRuleId.value) {
    const rule = settings.aiRules.find((item) => item.id === editingRuleId.value);
    if (!rule) {
      closeRuleEditor();
      return;
    }
    rule.title = title;
    rule.content = content;
    rule.scope = ruleScope.value;
    rule.workspaceId = workspaceId;
  } else {
    settings.aiRules.unshift({
      id: createId("rule"),
      title,
      content,
      enabled: true,
      scope: ruleScope.value,
      workspaceId,
    });
  }
  closeRuleEditor();
}
function removeRule(rule: AiRule) {
  const index = settings.aiRules.indexOf(rule);
  if (index >= 0) settings.aiRules.splice(index, 1);
  if (editingRuleId.value === rule.id) closeRuleEditor();
}
function addAgent() {
  if (!newAgent.name.trim() || !newAgent.command.trim()) return;
  const agent = {
    id: createId("agent"),
    name: newAgent.name.trim(),
    command: newAgent.command.trim(),
    args: newAgent.args.trim(),
  };
  settings.acpAgents.push(agent);
  settings.acpAdapterId = `custom:${agent.id}`;
  Object.assign(newAgent, { name: "", command: "", args: "" });
}
function removeAgent(agent: AcpAgent) {
  settings.acpAgents.splice(settings.acpAgents.indexOf(agent), 1);
  if (settings.acpAdapterId === `custom:${agent.id}`) {
    settings.acpAdapterId = ACP_BUILTIN_AGENTS[0]!.id;
  }
}
function selectAcpAdapter(id: string) {
  settings.acpAdapterId = id;
}
async function checkUpdate() {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  updateError.value = "";
  try {
    const result = await appUpdate.checkNow();
    const title = result.available
      ? t("settings.update.available", { version: result.version })
      : t("settings.update.latest");
    ElMessage.success(title);
    notifyApp({ source: "update", level: "success", title });
  } catch (error) {
    const message = nativeErrorMessage(error);
    updateError.value = message;
    ElMessage.error(t("settings.update.failed", { message }));
    notifyApp({
      source: "update",
      level: "error",
      title: t("notifications.update.checkFailed"),
      message,
    });
  }
}
watch(
  () => props.updateRequest,
  (request) => {
    if (!request || !props.modelValue) return;
    selectCategory("about");
    void checkUpdate();
  },
  { immediate: true },
);
async function downloadUpdate() {
  if (!isDesktopRuntime()) return ElMessage.warning(t("common.desktopRequired"));
  if (!updateInfo.value?.available || updateDownloading.value || updateChecking.value) return;
  updateError.value = "";
  try {
    const result = await appUpdate.stageUpdate(updateInfo.value.version);
    if (!result.ready) return;
    const title = t("settings.update.ready", { version: result.version });
    ElMessage.success(title);
    notifyApp({ source: "update", level: "success", title });
  } catch (error) {
    const message = nativeErrorMessage(error);
    ElMessage.error(t("settings.update.downloadFailed", { message }));
    notifyApp({
      source: "update",
      level: "error",
      title: t("notifications.update.downloadFailed"),
      message,
    });
  }
}

async function openUpdateRelease() {
  if (!updateInfo.value?.releaseURL) return;
  try {
    await openExternalUrl(updateInfo.value.releaseURL);
  } catch (error) {
    ElMessage.error(t("settings.update.openReleaseFailed", { message: nativeErrorMessage(error) }));
  }
}

async function restartAfterUpdate() {
  try {
    await appUpdate.restart();
  } catch (error) {
    ElMessage.error(t("settings.update.restartFailed", { message: nativeErrorMessage(error) }));
  }
}

async function resetDefaults() {
  try {
    await ElMessageBox.confirm(t("settings.resetHint"), t("settings.resetDefaults"), {
      type: "warning",
      confirmButtonText: t("settings.resetConfirm"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  Object.assign(settings, structuredClone(defaults), { locale: locale.value });
  sidebarVisibility.value = createDefaultSidebarVisibility();
  try {
    await Promise.all([
      saveSidebarVisibility(sidebarVisibility.value),
      saveAiApprovalMode(DEFAULT_AI_APPROVAL_MODE),
      clearGlobalToolAuthorizations(),
    ]);
  } catch (error) {
    // Approval mode / grants keep their persisted values; show them, not the defaults.
    approvalMode.value = aiApprovalMode.value;
    ElMessage.error(t("settings.resetFailed", { message: nativeErrorMessage(error) }));
    return;
  }
  approvalMode.value = DEFAULT_AI_APPROVAL_MODE;
  active.value = "general";
}
async function exportConfig() {
  const choice = await confirmSecretExport();
  if (!choice) return;
  const includeSecrets = choice === "with-secrets";
  try {
    const payload = await buildConfigExport({ includeSecrets });
    const filename = `mincode-config-${payload.exportedAt.slice(0, 10)}.json`;
    const filters = [{ DisplayName: "JSON", Pattern: "*.json" }];
    const saved = await saveTextFileToDisk({
      title: t("settings.data.export"),
      filename,
      content: JSON.stringify(payload, null, 2),
      filters,
      browserMimeType: "application/json",
    });
    if (saved === false) return;
    ElMessage.success(
      t(includeSecrets
        ? "security.secretExport.doneWithSecrets"
        : "security.secretExport.doneWithoutSecrets")
    );
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}
async function startImportConfig() {
  if (!isDesktopRuntime()) {
    importInput.value?.click();
    return;
  }
  try {
    const path = await chooseNativeFile(t("settings.data.import"), [
      { DisplayName: "JSON", Pattern: "*.json" },
    ]);
    if (!path) return;
    const parsed = parseConfigImport(JSON.parse(await readSelectedText(path)), t("settings.data.importInvalid"));
    if (parsed.includesSecrets && !(await confirmSecretImport())) return;
    await applyConfigImport(parsed);
    reportConfigImport(parsed);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  }
}
async function importConfig(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  try {
    const parsed = parseConfigImport(
      JSON.parse(await file.text()) as unknown,
      t("settings.data.importInvalid")
    );
    if (parsed.includesSecrets && !(await confirmSecretImport())) return;
    await applyConfigImport(parsed);
    reportConfigImport(parsed);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    input.value = "";
  }
}
function reportConfigImport(parsed: ParsedConfigImport) {
  if (Object.keys(parsed.values).length) ElMessage.success(t("settings.data.imported"));
  if (parsed.skippedKeys.length) {
    ElMessage.warning(t("settings.data.importSkipped", { count: parsed.skippedKeys.length }));
  }
}
async function clearAiHistory() {
  try {
    await ElMessageBox.confirm(
      t("settings.data.aiHistoryClearConfirm"),
      t("settings.data.aiHistory"),
      { type: "warning" }
    );
    await aiAssistant.clearConversationHistory();
    ElMessage.success(t("settings.data.cleared"));
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    const message = error instanceof Error && error.message === "ai_conversation_running"
      ? t("settings.data.aiHistoryRunning")
      : error instanceof Error ? error.message : String(error);
    ElMessage.error(message);
  }
}

onMounted(() => void ensureHydrated());
onBeforeUnmount(() => {
  mcpSearchController?.abort();
  if (persistTimer) persist();
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    class="settings-dialog"
    :width="fullscreen ? '100%' : 'min(826px, calc(100vw - 48px))'"
    :fullscreen="fullscreen"
    append-to-body
    :show-close="false"
    :close-on-click-modal="true"
    :destroy-on-close="false"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <header class="settings-dialog__header">
        <div class="settings-dialog__brand">
          <span class="settings-dialog__logo"><img :src="MinCodeMark" alt="" /></span>
          <h2>{{ t("settings.title") }}</h2>
        </div>
        <div class="settings-dialog__header-actions">
          <el-button
            text
            native-type="button"
            class="settings-dialog__fullscreen"
            :icon="fullscreen ? ScaleToOriginal : FullScreen"
            :aria-label="t(fullscreen ? 'settings.exitFullscreen' : 'settings.enterFullscreen')"
            :title="t(fullscreen ? 'settings.exitFullscreen' : 'settings.enterFullscreen')"
            :aria-pressed="fullscreen"
            @click="fullscreen = !fullscreen"
          />
          <el-button
            text
            native-type="button"
            class="settings-dialog__close"
            :icon="Close"
            :aria-label="t('common.close')"
            @click="close"
          />
        </div>
      </header>
    </template>

    <div class="settings-dialog__shell">
      <SettingsNavigation
        :model-value="active"
        :items="categories"
        @update:model-value="selectCategory($event as Category)"
        @reset="resetDefaults"
      />

      <el-scrollbar class="settings-dialog__content">
        <main class="settings-dialog__content-inner">
        <GeneralSettingsPanel
          v-if="active === 'general'"
          :settings="settings"
          :locale="locale"
          :sidebar-visibility="sidebarVisibility"
          :workspace-state-isolation-enabled="workspaceStatePreferences.isolationEnabled"
          @change="Object.assign(settings, $event)"
          @change-locale="setLocale"
          @change-sidebar-visibility="updateSidebarVisibility"
          @change-workspace-state-isolation="updateWorkspaceStateIsolation"
        />

        <TerminalSettingsPanel
          v-else-if="active === 'terminal'"
          :settings="settings"
          @change="Object.assign(settings, $event)"
        />

        <KeybindingSettingsPanel
          v-else-if="active === 'shortcuts'"
          :global-shortcut-enabled="settings.globalShortcutEnabled"
        />

        <section v-else-if="active === 'ai'" class="settings-flat-page settings-ai-page">
          <el-segmented
            v-model="aiTab"
            class="settings-ai-tabs"
            :options="aiTabs.map((item) => ({ label: t(item.label), value: item.id }))"
          />

          <div v-if="aiTab === 'connection' && activeProfile" class="settings-ai-panel">
            <section class="settings-ai-card settings-ai-interface-card">
              <strong>{{ t("settings.ai.interface") }}</strong>
              <div class="settings-ai-interface-row">
                <span class="app-inline-label"
                  >{{ t("settings.ai.conversationScope")
                  }}<AppHintIcon :text="t('settings.ai.conversationScopeHint')"
                /></span>
                <el-segmented
                  class="settings-ai-scope-tabs"
                  :model-value="aiConversationPreferences.scopeMode"
                  :options="[
                    { label: t('settings.ai.conversationScopeWorkspace'), value: 'workspace' },
                    { label: t('settings.ai.conversationScopeGlobal'), value: 'global' },
                  ]"
                  :aria-label="t('settings.ai.conversationScope')"
                  @update:model-value="setAiConversationScopeMode($event as AiConversationScopeMode)"
                />
              </div>
              <div class="settings-ai-interface-row">
                <span class="app-inline-label"
                  >{{ t("settings.ai.placement")
                  }}<AppHintIcon :text="t('settings.ai.placementHint')"
                /></span>
                <el-segmented
                  class="settings-ai-scope-tabs"
                  :model-value="aiAssistant.placement"
                  :options="[
                    { label: t('settings.ai.placementDrawer'), value: 'drawer' },
                    { label: t('settings.ai.placementTab'), value: 'tab' },
                  ]"
                  :aria-label="t('settings.ai.placement')"
                  @update:model-value="aiAssistant.setPlacement($event as AiAssistantPlacement)"
                />
              </div>
              <div v-if="aiAssistant.placement === 'tab'" class="settings-ai-interface-row">
                <span class="app-inline-label"
                  >{{ t("settings.ai.sidebarPosition")
                  }}<AppHintIcon :text="t('settings.ai.sidebarPositionHint')"
                /></span>
                <AiConversationSidebarPosition />
              </div>
              <div class="settings-ai-interface-row">
                <span>{{ t("settings.ai.systemNotifications") }}</span>
                <el-switch
                  v-model="settings.aiSystemNotifications"
                  size="small"
                  :aria-label="t('settings.ai.systemNotifications')"
                />
              </div>
            </section>
            <!-- 左侧档案列表、右侧当前档案的连接表单；窄窗口上下排列。 -->
            <div class="settings-ai-profile-layout">
              <aside class="settings-ai-card settings-ai-profiles">
                <header class="settings-ai-profiles__header">
                  <div>
                    <span class="app-inline-label">
                      <strong>{{ t("settings.ai.profile") }}</strong>
                      <AppHintIcon :text="t('settings.ai.profileOriginalHint')" />
                    </span>
                    <small>{{ t("settings.ai.profileHint") }}</small>
                  </div>
                  <el-button size="small" :icon="Plus" @click="addAiProfile">
                    {{ t("settings.ai.createProfile") }}
                  </el-button>
                </header>
                <div
                  class="settings-ai-card-grid"
                  role="radiogroup"
                  :aria-label="t('settings.ai.profile')"
                >
                  <article
                    v-for="profile in settings.aiProfiles"
                    :key="profile.id"
                    class="settings-ai-entry-card settings-ai-entry-card--selectable settings-ai-entry-card--compact"
                    :class="{ 'is-active': profile.id === settings.activeAiProfileId }"
                    role="radio"
                    :aria-checked="profile.id === settings.activeAiProfileId"
                    tabindex="0"
                    @click="selectAiProfile(profile.id)"
                    @keydown.enter.prevent="selectAiProfile(profile.id)"
                    @keydown.space.prevent="selectAiProfile(profile.id)"
                  >
                    <header class="settings-ai-entry-card__head">
                      <span
                        class="settings-ai-entry-card__avatar"
                        :data-tone="entryAvatarTone(profile.name)"
                        aria-hidden="true"
                        >{{ entryAvatarInitial(profile.name) }}</span
                      >
                      <div class="settings-ai-entry-card__title">
                        <strong :title="profile.name"
                          ><span>{{ profile.name }}</span
                          ><el-icon
                            v-if="profile.id === settings.activeAiProfileId"
                            class="settings-ai-entry-card__check"
                            aria-hidden="true"
                            ><Check /></el-icon
                        ></strong>
                        <small :title="profileCompactSubtitle(profile)">{{
                          profileCompactSubtitle(profile)
                        }}</small>
                      </div>
                      <div class="settings-ai-entry-card__actions" @click.stop @keydown.stop>
                        <el-button
                          text
                          size="small"
                          :icon="CopyDocument"
                          :title="t('settings.ai.duplicateProfile')"
                          :aria-label="t('settings.ai.duplicateProfile')"
                          @click="duplicateAiProfile(profile)"
                        />
                        <el-popconfirm
                          :title="t('settings.ai.deleteProfile')"
                          @confirm="deleteAiProfile(profile)"
                        >
                          <template #reference>
                            <el-button
                              text
                              size="small"
                              :icon="Delete"
                              :disabled="settings.aiProfiles.length <= 1"
                              :title="t('settings.ai.deleteProfile')"
                              :aria-label="t('settings.ai.deleteProfile')"
                              @click.stop
                            />
                          </template>
                        </el-popconfirm>
                      </div>
                    </header>
                  </article>
                </div>
              </aside>
              <div class="settings-ai-profile-detail">
                <section class="settings-ai-card settings-ai-form-card">
                  <div class="settings-ai-field-row"
                    ><span>{{ t("settings.ai.profileName") }}</span
                    ><el-input
                      v-model="activeProfile.name"
                      :placeholder="t('settings.ai.profileNamePlaceholder')"
                  /></div>
                  <div class="settings-ai-field-row"
                    ><span>{{ t("settings.ai.provider") }}</span
                    ><el-select :model-value="activeProfile.provider" filterable @update:model-value="selectProvider"
                      ><el-option
                        v-for="option in providerPresets"
                        :key="option.value"
                        :value="option.value"
                        :label="option.value === 'custom' ? t('settings.ai.custom') : option.label"
                      /></el-select
                  ></div>
                  <div class="settings-ai-field-row settings-ai-protocol-field">
                    <span class="settings-ai-field-label app-inline-label">
                      <b>{{ t("settings.ai.apiFormat") }}</b>
                      <AppHintIcon :text="t('settings.ai.apiFormatHint')" />
                    </span>
                    <el-select v-model="activeProfile.apiFormat">
                      <el-option
                        v-for="format in aiApiFormatOptions"
                        :key="format.value"
                        :value="format.value"
                        :label="t(format.label)"
                      />
                    </el-select>
                  </div>
                  <div v-if="activeProfile.apiFormat === 'anthropic'" class="settings-ai-field-row settings-ai-protocol-field">
                    <span class="settings-ai-field-label app-inline-label">
                      <b>{{ t("settings.ai.anthropicAuth") }}</b>
                      <AppHintIcon :text="t('settings.ai.anthropicAuthHint')" />
                    </span>
                    <el-radio-group v-model="activeProfile.anthropicAuth">
                      <el-radio-button value="x-api-key">x-api-key</el-radio-button>
                      <el-radio-button value="bearer">Bearer</el-radio-button>
                    </el-radio-group>
                  </div>
                  <div v-if="activeProfile.apiFormat === 'openai'" class="settings-ai-field-row settings-ai-protocol-field">
                    <span class="settings-ai-field-label app-inline-label">
                      <b>{{ t("settings.ai.streamUsage") }}</b>
                      <AppHintIcon :text="t('settings.ai.streamUsageHint')" />
                    </span>
                    <el-select v-model="activeProfile.openaiStreamUsage">
                      <el-option value="auto" :label="t('settings.ai.streamUsage.auto')" />
                      <el-option value="always" :label="t('settings.ai.streamUsage.always')" />
                      <el-option value="never" :label="t('settings.ai.streamUsage.never')" />
                    </el-select>
                  </div>
                  <div class="settings-ai-field-row"
                    ><span>Base URL</span
                    ><el-input v-model="activeProfile.baseUrl" placeholder="https://api.openai.com/v1"
                  /></div>
                  <div class="settings-ai-field-row"
                    ><span>API Key</span
                    ><el-input v-model="activeProfile.apiKey" show-password placeholder="sk-..."
                  /></div>
                  <el-alert
                    v-if="browserLocalMode"
                    class="settings-ai-browser-note"
                    type="info"
                    :closable="false"
                    show-icon
                    :title="t('settings.ai.browserKeyNote')"
                  />
                  <div class="settings-ai-field-row settings-ai-model-field"
                    ><span class="settings-ai-field-heading"
                      ><b>{{ t("settings.ai.model") }}</b
                      ><el-button
                        text
                        :icon="Refresh"
                        :loading="fetchingModels"
                        :disabled="fetchingModels"
                        @click="fetchAvailableModels"
                        >{{ t("settings.ai.fetchModels") }}</el-button
                      ></span
                    ><el-select v-model="activeProfile.model" filterable allow-create
                      ><el-option
                        v-for="model in activeProfile.commonModels"
                        :key="model"
                        :value="model"
                        :label="modelOptionLabel(model)" /></el-select
                  ></div>
                  <div class="settings-ai-field-row">
                    <span class="settings-ai-field-label app-inline-label">
                      <b>{{ t("settings.ai.reasoning") }}</b>
                      <AppHintIcon :text="t('settings.ai.reasoningHint')" />
                    </span>
                    <el-select v-model="activeProfile.reasoningEffort">
                      <el-option value="default" :label="t('settings.ai.reasoningDefault')" />
                      <el-option
                        v-for="option in reasoningEffortOptions"
                        :key="option.value"
                        :value="option.value"
                        :label="option.label"
                      />
                    </el-select>
                  </div>
                </section>

                <div class="settings-ai-advanced-toggle">
                  <el-button
                    text
                    :icon="aiAdvanced ? ArrowDown : ArrowRight"
                    :aria-expanded="aiAdvanced"
                    @click="aiAdvanced = !aiAdvanced"
                    >{{ t("settings.ai.advanced") }}</el-button
                  >
                  <small>{{ t("settings.ai.advancedHint") }}</small>
                </div>
                <template v-if="aiAdvanced">
                  <section class="settings-ai-card settings-ai-form-card">
                    <div class="settings-ai-field-row">
                      <span class="settings-ai-field-label app-inline-label">
                        <b>{{ t("settings.ai.subagentModel") }}</b>
                        <AppHintIcon :text="t('settings.ai.subagentModelHint')" />
                      </span>
                      <el-select
                        v-model="activeProfile.subagentModel"
                        clearable
                        filterable
                        :placeholder="t('settings.ai.subagentModelInherit')"
                      >
                        <el-option
                          v-for="model in configuredProfileModels(activeProfile)"
                          :key="model"
                          :value="model"
                          :label="model"
                        />
                      </el-select>
                    </div>
                    <div class="settings-ai-field-row">
                      <span class="settings-ai-field-label app-inline-label">
                        <b>{{ t("settings.ai.maxLoops") }}</b>
                        <AppHintIcon :text="t('settings.ai.maxLoopsHint')" />
                      </span>
                      <el-input-number
                        v-model="settings.aiMaxLoops"
                        :min="MIN_AI_MAX_LOOPS"
                        :max="MAX_AI_MAX_LOOPS"
                        :step="10"
                        :controls="false"
                      />
                    </div>
                  </section>
                  <section class="settings-ai-card settings-ai-common-models">
                    <header>
                      <span class="app-inline-label">
                        <strong>{{ t("settings.ai.commonModels") }}</strong>
                        <AppHintIcon :text="t('settings.ai.commonModelsHint')" />
                      </span>
                    </header>
                    <div class="settings-ai-model-tags">
                      <span v-for="model in activeProfile.commonModels" :key="model"
                        >{{ model
      }}<button
                          type="button"
                          class="settings-ai-model-tag-close"
                          :aria-label="t('common.delete')"
                          @click="removeCommonModel(model)"
                        >
                          <el-icon><Close /></el-icon>
                        </button></span
                      >
                    </div>
                    <div class="settings-ai-add-model">
                      <el-select
                        :model-value="''"
                        :placeholder="t('settings.ai.addFromList')"
                        @change="addCommonModel"
                        ><el-option
                          v-for="model in addableCommonModels"
                          :key="model"
                          :value="model"
                          :label="model" /></el-select
                      ><el-input
                        v-model="manualModel"
                        :placeholder="t('settings.ai.manualModel')"
                        @keyup.enter="addCommonModel()"
                        ><template #append><el-button :icon="Plus" @click="addCommonModel()" /></template
                      ></el-input>
                    </div>
                  </section>

                  <section class="settings-ai-card settings-ai-failover-card">
                    <header>
                      <div>
                        <span class="app-inline-label">
                          <strong>{{ t("settings.ai.failoverEnabled") }}</strong>
                          <AppHintIcon :text="t('settings.ai.failoverEnabledHint')" />
                        </span>
                      </div>
                      <el-switch
                        v-model="activeProfile.failoverEnabled"
                        :disabled="fallbackModelOptions(activeProfile).length < 2"
                        :aria-label="t('settings.ai.failoverEnabled')"
                      />
                    </header>
                    <div class="settings-ai-failover-row">
                      <span>{{ t("settings.ai.failoverModels") }}</span>
                      <el-select
                        v-model="activeProfile.failoverModels"
                        multiple
                        filterable
                        clearable
                        :disabled="!activeProfile.failoverEnabled || fallbackModelOptions(activeProfile).length < 2"
                        :placeholder="t('settings.ai.failoverModelsHint')"
                      >
                        <el-option
                          v-for="model in fallbackModelOptions(activeProfile)"
                          :key="model"
                          :value="model"
                          :label="model"
                        />
                      </el-select>
                    </div>
                    <small v-if="fallbackModelOptions(activeProfile).length < 2" class="settings-ai-failover-card__notice">
                      {{ t("settings.ai.failoverNeedModels") }}
                    </small>
                  </section>

                  <section class="settings-ai-card settings-ai-token-card">
                    <header>
                      <span class="app-inline-label">
                        <strong>{{ t("settings.ai.tokenLimits") }}</strong>
                        <AppHintIcon :text="t('settings.ai.tokenLimitsHint')" />
                      </span>
                    </header>
                    <div class="settings-ai-token-grid">
                      <div class="settings-ai-token-field">
                        <span class="app-inline-label"
                          >{{ t("settings.ai.contextWindow")
                          }}<AppHintIcon :text="t('settings.ai.contextWindowHint')" /></span
                        >
                        <el-input-number
                          v-model="activeProfile.contextWindow"
                          :min="1024"
                          :max="10000000"
                          :step="1024"
                          :controls="false"
                        />
                      </div>
                      <div class="settings-ai-token-field">
                        <span class="app-inline-label"
                          >{{ t("settings.ai.maxInputTokens")
                          }}<AppHintIcon :text="t('settings.ai.maxInputTokensHint')" /></span
                        >
                        <el-input-number
                          v-model="activeProfile.maxInputTokens"
                          :min="1024"
                          :max="10000000"
                          :step="1024"
                          :controls="false"
                        />
                      </div>
                      <div class="settings-ai-token-field">
                        <span class="app-inline-label"
                          >{{ t("settings.ai.maxOutputTokens")
                          }}<AppHintIcon :text="t('settings.ai.maxOutputTokensHint')" /></span
                        >
                        <el-input-number
                          v-model="activeProfile.maxOutputTokens"
                          :min="1"
                          :max="1000000"
                          :step="1024"
                          :controls="false"
                        />
                      </div>
                    </div>
                  </section>
                </template>
                <div class="settings-ai-save">
                  <el-button
                    :loading="testingAiConnection"
                    :disabled="testingAiConnection"
                    @click="testActiveAiConnection"
                  >{{ t("settings.ai.test") }}</el-button>
                  <el-button type="primary" @click="saveAiSettings">{{ t("common.save") }}</el-button>
                </div>
              </div>
            </div>
          </div>

          <div v-else-if="aiTab === 'permissions'" class="settings-ai-panel">
            <AgentControlPanel />
            <section class="settings-ai-card settings-ai-permissions-intro">
              <div>
                <strong>{{ t("settings.ai.permissionsTitle") }}</strong>
                <small>{{ t("settings.ai.permissionsHint") }}</small>
              </div>
              <el-tag effect="plain" type="warning">{{ t("settings.ai.dangerousAlwaysAsk") }}</el-tag>
            </section>

            <el-radio-group
              v-model="approvalMode"
              class="settings-ai-approval-modes"
              :disabled="approvalSaving"
              @change="updateApprovalMode"
            >
              <el-radio value="ask" class="settings-ai-approval-mode">
                <span class="settings-ai-approval-mode__icon"><Lock /></span>
                <span class="settings-ai-approval-mode__content">
                  <strong>{{ t("ai.approvalMode.ask") }}</strong>
                  <small>{{ t("ai.approvalMode.askHint") }}</small>
                  <em>{{ t("settings.ai.permissionsAskDetail") }}</em>
                </span>
              </el-radio>
              <el-radio value="agent" class="settings-ai-approval-mode">
                <span class="settings-ai-approval-mode__icon"><CircleCheck /></span>
                <span class="settings-ai-approval-mode__content">
                  <strong>{{ t("ai.approvalMode.agent") }}</strong>
                  <small>{{ t("ai.approvalMode.agentHint") }}</small>
                  <em>{{ t("settings.ai.permissionsAgentDetail") }}</em>
                </span>
              </el-radio>
              <el-radio value="full-access" class="settings-ai-approval-mode is-danger">
                <span class="settings-ai-approval-mode__icon"><Warning /></span>
                <span class="settings-ai-approval-mode__content">
                  <strong>{{ t("ai.approvalMode.fullAccess") }}</strong>
                  <small>{{ t("ai.approvalMode.fullAccessHint") }}</small>
                  <em>{{ t("settings.ai.permissionsFullDetail") }}</em>
                </span>
              </el-radio>
              <el-radio value="plan" class="settings-ai-approval-mode">
                <span class="settings-ai-approval-mode__icon"><Reading /></span>
                <span class="settings-ai-approval-mode__content">
                  <strong>{{ t("ai.approvalMode.plan") }}</strong>
                  <small>{{ t("ai.approvalMode.planHint") }}</small>
                </span>
              </el-radio>
            </el-radio-group>

            <section class="settings-ai-card settings-ai-authorizations">
              <header class="settings-ai-authorizations__header">
                <div>
                  <strong>{{ t("settings.ai.globalAuthorizations") }}</strong>
                  <small>{{ t("settings.ai.globalAuthorizationsHint") }}</small>
                </div>
                <el-popconfirm
                  v-if="globalToolAuthorizations.length"
                  :title="t('settings.ai.clearAuthorizationsConfirm')"
                  @confirm="clearAllGlobalAuthorizations"
                >
                  <template #reference>
                    <el-button
                      size="small"
                      :disabled="authorizationUpdating"
                    >{{ t("settings.ai.clearAuthorizations") }}</el-button>
                  </template>
                </el-popconfirm>
              </header>
              <el-empty
                v-if="!globalToolAuthorizations.length"
                :image-size="48"
                :description="t('settings.ai.noGlobalAuthorizations')"
              />
              <div v-else class="settings-ai-card-grid">
                <article
                  v-for="authorization in globalToolAuthorizations"
                  :key="authorizationKey(authorization)"
                  class="settings-ai-entry-card"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(authorization.toolName)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(authorization.toolName) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="authorization.toolName">{{ authorization.toolName }}</strong>
                      <small :title="authorization.resourceId">{{ authorization.resourceId }}</small>
                    </div>
                  </header>
                  <p class="settings-ai-entry-card__desc">
                    <code>{{ authorization.parameterFingerprint.slice(0, 12) }}</code>
                  </p>
                  <footer class="settings-ai-entry-card__foot">
                    <span
                      class="settings-ai-entry-card__chip"
                      :data-tone="authorizationRiskTone(authorization.risk)"
                      >{{ authorizationRiskLabel(authorization.risk) }}</span
                    >
                    <div class="settings-ai-entry-card__actions">
                      <el-popconfirm
                        :title="t('settings.ai.revokeAuthorizationConfirm')"
                        @confirm="removeGlobalAuthorization(authorization)"
                      >
                        <template #reference>
                          <el-button
                            text
                            :icon="Delete"
                            :disabled="authorizationUpdating"
                            :title="t('settings.ai.revokeAuthorization')"
                            :aria-label="t('settings.ai.revokeAuthorization')"
                          />
                        </template>
                      </el-popconfirm>
                    </div>
                  </footer>
                </article>
              </div>
            </section>
          </div>

          <div v-else-if="aiTab === 'mcp'" class="settings-ai-panel">
            <el-segmented
              v-model="mcpSubTab"
              class="settings-ai-scope-tabs"
              :options="installedSubTabOptions"
              :aria-label="t('settings.mcp.capability')"
            />
            <template v-if="mcpSubTab === 'installed'">
              <section class="settings-ai-installed">
                <div class="settings-ai-installed__title">
                  <strong>{{ t("settings.mcp.installed") }}</strong>
                  <div class="settings-ai-installed__tools">
                    <el-button
                      text
                      size="small"
                      :icon="EditPen"
                      @click="openMcpJsonEditor"
                      >{{ t("settings.mcp.editJson") }}</el-button
                    >
                    <span>{{ settings.mcpServers.length }}</span>
                  </div>
                </div>
                <div v-if="!settings.mcpServers.length" class="settings-ai-empty">
                  {{ t("settings.mcp.installedEmpty") }}
                </div>
                <div v-else class="settings-ai-card-grid">
                  <article
                    v-for="server in settings.mcpServers"
                    :key="server.id"
                    class="settings-ai-entry-card"
                  >
                    <header class="settings-ai-entry-card__head">
                      <span
                        class="settings-ai-entry-card__avatar"
                        :data-tone="entryAvatarTone(server.id)"
                        aria-hidden="true"
                        >{{ entryAvatarInitial(server.id) }}</span
                      >
                      <div class="settings-ai-entry-card__title">
                        <strong :title="server.id">{{ server.id }}</strong>
                        <small :title="server.transport === 'http' ? server.url : server.command">{{
                          server.transport === "http" ? server.url : server.command
                        }}</small>
                      </div>
                      <el-switch v-model="server.enabled" size="small" />
                    </header>
                    <p class="settings-ai-entry-card__status">
                      <i :class="{ 'is-online': mcpStatus(server.id)?.state === 'connected' }" />
                      <span>{{ mcpStatusText(server) }}</span>
                    </p>
                    <footer class="settings-ai-entry-card__foot">
                      <span class="settings-ai-entry-card__chip">{{
                        server.transport === "http" ? "HTTP" : "stdio"
                      }}</span>
                      <div class="settings-ai-entry-card__actions">
                        <el-button
                          v-if="server.docsUrl"
                          text
                          size="small"
                          :icon="TopRight"
                          :title="t('settings.documentation')"
                          :aria-label="t('settings.documentation')"
                          @click="openDocumentation(server.docsUrl)"
                        />
                        <el-button size="small" @click="connectMcp(server)">{{
                          t("settings.mcp.connect")
                        }}</el-button>
                        <el-button
                          v-if="mcpStatus(server.id)?.state === 'connected'"
                          size="small"
                          @click="disconnectMcp(server)"
                          >{{ t("settings.mcp.disconnect") }}</el-button
                        >
                        <el-button
                          text
                          :icon="Delete"
                          :title="t('common.delete')"
                          :aria-label="t('common.delete')"
                          @click="removeMcpServer(server)"
                        />
                      </div>
                    </footer>
                  </article>
                </div>
              </section>
              <el-button class="settings-ai-advanced-button" @click="mcpAdvanced = !mcpAdvanced">{{
                t("settings.mcp.advanced")
              }}</el-button>
              <section v-if="mcpAdvanced" class="settings-ai-card settings-ai-advanced-form">
                <div class="settings-ai-two-columns">
                  <el-input v-model="newMcp.id" :placeholder="t('settings.mcp.serverId')" /><el-select
                    v-model="newMcp.transport"
                    ><el-option value="stdio" label="stdio" /><el-option value="http" label="HTTP"
                  /></el-select>
                </div>
                <el-input
                  v-if="newMcp.transport === 'stdio'"
                  v-model="newMcp.command"
                  :placeholder="t('settings.mcp.command')"
                /><el-input v-else v-model="newMcp.url" placeholder="https://" />
                <el-input
                  v-model="newMcp.docsUrl"
                  :placeholder="t('settings.documentationUrlPlaceholder')"
                />
                <div class="settings-ai-add-row">
                  <el-input v-model="newMcp.args" :placeholder="t('settings.mcp.args')" /><el-button
                    type="primary"
                    :icon="Plus"
                    @click="addMcpServer"
                    >{{ t("common.add") }}</el-button
                  >
                </div>
              </section>
            </template>
            <template v-else>
              <section class="settings-ai-card settings-ai-intro-card">
                <div>
                  <span class="app-inline-label">
                    <strong>{{ t("settings.mcp.capability") }}</strong>
                    <AppHintIcon :text="t('settings.mcp.capabilityHint')" />
                  </span>
                </div>
                <div>
                  <el-button @click="importCursorMcp">{{ t("settings.mcp.readCursor") }}</el-button
                  ><el-button @click="importMcpFile">{{ t("settings.mcp.chooseJson") }}</el-button>
                </div>
              </section>
            <section class="settings-ai-card settings-ai-search-card">
              <header>
                <span class="app-inline-label">
                  <strong>{{ t("settings.mcp.searchInstall") }}</strong>
                  <AppHintIcon :text="t('settings.mcp.searchInstallHint')" />
                </span>
              </header>
              <div class="settings-ai-search-line">
                <el-input
                  v-model="mcpSearch"
                  :placeholder="t('settings.mcp.searchPlaceholder')"
                  @keyup.enter="searchMcpServers"
                /><el-button type="primary" :loading="mcpSearching" @click="searchMcpServers">{{
                  t("common.search")
                }}</el-button>
              </div>
              <div v-if="mcpSearchResults.length" class="settings-ai-card-grid">
                <article
                  v-for="entry in mcpSearchResults"
                  :key="entry.id"
                  class="settings-ai-entry-card"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(entry.name)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(entry.name) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="entry.name">{{ entry.name }}</strong>
                      <small :title="entry.id">{{ entry.id }}</small>
                    </div>
                  </header>
                  <p class="settings-ai-entry-card__desc">{{ entry.description }}</p>
                  <footer class="settings-ai-entry-card__foot">
                    <span class="settings-ai-entry-card__chip">{{
                      registrySourceText(entry)
                    }}</span>
                    <div class="settings-ai-entry-card__actions">
                      <el-button
                        v-if="entry.docsUrl"
                        text
                        size="small"
                        :icon="TopRight"
                        :title="t('settings.documentation')"
                        :aria-label="t('settings.documentation')"
                        @click="openDocumentation(entry.docsUrl)"
                      />
                      <el-button
                        type="primary"
                        size="small"
                        :loading="installingMcpId === entry.id"
                        @click="installMcpEntry(entry)"
                        >{{ t("common.add") }}</el-button
                      >
                    </div>
                  </footer>
                </article>
              </div>
            </section>
            <section class="settings-ai-card settings-ai-template-card">
              <header>
                <span class="app-inline-label">
                  <strong>{{ t("settings.mcp.templates") }}</strong>
                  <AppHintIcon :text="t('settings.mcp.templatesHint')" />
                </span>
              </header>
              <div class="settings-ai-card-grid">
                <article
                  v-for="entry in builtinMcpTemplates"
                  :key="entry.id"
                  class="settings-ai-entry-card"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(entry.name)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(entry.name) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="entry.name">{{ entry.name }}</strong>
                      <small :title="entry.id">{{ entry.id }}</small>
                    </div>
                  </header>
                  <p class="settings-ai-entry-card__desc">{{ entry.description }}</p>
                  <footer class="settings-ai-entry-card__foot">
                    <span class="settings-ai-entry-card__chip">{{
                      registrySourceText(entry)
                    }}</span>
                    <div class="settings-ai-entry-card__actions">
                      <el-button
                        v-if="entry.docsUrl"
                        text
                        size="small"
                        :icon="TopRight"
                        :title="t('settings.documentation')"
                        :aria-label="t('settings.documentation')"
                        @click="openDocumentation(entry.docsUrl)"
                      />
                      <el-button
                        type="primary"
                        size="small"
                        :loading="installingMcpId === entry.id"
                        @click="installMcpEntry(entry)"
                        >{{ t("common.add") }}</el-button
                      >
                    </div>
                  </footer>
                </article>
              </div>
            </section>
            </template>
          </div>

          <div v-else-if="aiTab === 'rules'" class="settings-ai-panel">
            <el-form-item :label="t('settings.ai.readProjectInstructions')">
              <el-switch
                v-model="settings.aiReadProjectInstructions"
                size="small"
                :aria-label="t('settings.ai.readProjectInstructions')"
              />
            </el-form-item>
            <p class="settings-ai-description">{{ t("settings.rules.originalHint") }}</p>
            <div class="settings-ai-rules-filters">
              <el-segmented
                v-model="ruleScope"
                class="settings-ai-scope-tabs"
                :options="ruleScopeOptions"
                :aria-label="t('settings.rules.originalHint')"
              />
              <el-select
                v-if="ruleScope === 'workspace'"
                v-model="ruleWorkspaceId"
                class="settings-ai-rule-workspace-select"
                :placeholder="t('settings.rules.selectWorkspace')"
                :aria-label="t('settings.rules.selectWorkspace')"
              >
                <el-option
                  v-for="workspace in ruleWorkspaceOptions"
                  :key="workspace.value"
                  :label="workspace.label"
                  :value="workspace.value"
                />
              </el-select>
            </div>
            <section class="settings-ai-rules-area">
              <div class="settings-ai-rules-toolbar">
                <strong class="settings-ai-rules-toolbar__title">{{
                  ruleScope === "global"
                    ? t("settings.rules.global")
                    : ruleWorkspaceOptions.find((workspace) => workspace.value === ruleWorkspaceId)?.label
                      || t("settings.rules.workspace")
                }}</strong>
                <el-button type="primary" :icon="Plus" @click="openNewRuleEditor">
                  {{ t("settings.rules.new") }}
                </el-button>
              </div>
              <div v-if="ruleEditorOpen" class="settings-ai-card settings-ai-rule-editor">
                <strong class="settings-ai-rule-editor__title">{{
                  editingRuleId ? t("settings.rules.edit") : t("settings.rules.new")
                }}</strong>
                <el-input v-model="ruleDraft.title" :placeholder="t('settings.rules.title')" />
                <el-input
                  v-model="ruleDraft.content"
                  type="textarea"
                  :rows="6"
                  :placeholder="t('settings.rules.content')"
                />
                <div class="settings-ai-rule-editor__actions">
                  <el-button @click="closeRuleEditor">{{ t("common.cancel") }}</el-button>
                  <el-button
                    type="primary"
                    :disabled="!ruleDraft.title.trim() || !ruleDraft.content.trim()"
                    @click="saveRule"
                  >
                    {{ t("common.save") }}
                  </el-button>
                </div>
              </div>
              <div
                v-if="!visibleAiRules.length && !ruleEditorOpen"
                class="settings-ai-empty settings-ai-empty--plain"
              >
                {{ t("settings.rules.originalEmpty") }}
              </div>
              <div v-else-if="visibleAiRules.length" class="settings-ai-card-grid">
                <article
                  v-for="rule in visibleAiRules"
                  :key="rule.id"
                  class="settings-ai-entry-card"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(rule.title)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(rule.title) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="rule.title">{{ rule.title }}</strong>
                      <small>{{ ruleScopeChipLabel(rule) }}</small>
                    </div>
                    <el-switch v-model="rule.enabled" size="small" :aria-label="rule.title" />
                  </header>
                  <p class="settings-ai-entry-card__desc">{{ rule.content }}</p>
                  <footer class="settings-ai-entry-card__foot">
                    <div class="settings-ai-entry-card__actions">
                      <el-tooltip :content="t('common.edit')" placement="top">
                        <el-button
                          text
                          :icon="EditPen"
                          :aria-label="t('common.edit')"
                          @click="openRuleEditor(rule)"
                        />
                      </el-tooltip>
                      <el-tooltip :content="t('common.delete')" placement="top">
                        <el-button
                          text
                          :icon="Delete"
                          :aria-label="t('common.delete')"
                          @click="removeRule(rule)"
                        />
                      </el-tooltip>
                    </div>
                  </footer>
                </article>
              </div>
            </section>
          </div>

          <div v-else class="settings-ai-panel">
            <section class="settings-ai-card settings-ai-agent-card">
              <header>
                <span class="app-inline-label">
                  <strong>{{ t("settings.acp.originalTitle") }}</strong>
                  <AppHintIcon :text="t('settings.acp.originalHint')" />
                </span>
              </header>
              <div
                class="settings-ai-card-grid"
                role="radiogroup"
                :aria-label="t('settings.acp.originalTitle')"
              >
                <article
                  v-for="agent in ACP_BUILTIN_AGENTS"
                  :key="agent.id"
                  class="settings-ai-entry-card settings-ai-entry-card--selectable"
                  :class="{ 'is-active': settings.acpAdapterId === agent.id }"
                  role="radio"
                  :aria-checked="settings.acpAdapterId === agent.id"
                  tabindex="0"
                  @click="selectAcpAdapter(agent.id)"
                  @keydown.enter.prevent="selectAcpAdapter(agent.id)"
                  @keydown.space.prevent="selectAcpAdapter(agent.id)"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(agent.name)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(agent.name) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="agent.name">{{ agent.name }}</strong>
                      <small :title="acpBuiltinAgentCommandLine(agent)">{{
                        acpBuiltinAgentCommandLine(agent)
                      }}</small>
                    </div>
                    <el-icon
                      v-if="settings.acpAdapterId === agent.id"
                      class="settings-ai-entry-card__check"
                      aria-hidden="true"
                      ><Check
                    /></el-icon>
                  </header>
                  <footer class="settings-ai-entry-card__foot">
                    <span class="settings-ai-entry-card__chip">{{
                      t("settings.registry.source.builtin")
                    }}</span>
                  </footer>
                </article>
                <article
                  v-for="agent in settings.acpAgents"
                  :key="agent.id"
                  class="settings-ai-entry-card settings-ai-entry-card--selectable"
                  :class="{ 'is-active': settings.acpAdapterId === `custom:${agent.id}` }"
                  role="radio"
                  :aria-checked="settings.acpAdapterId === `custom:${agent.id}`"
                  tabindex="0"
                  @click="selectAcpAdapter(`custom:${agent.id}`)"
                  @keydown.enter.prevent="selectAcpAdapter(`custom:${agent.id}`)"
                  @keydown.space.prevent="selectAcpAdapter(`custom:${agent.id}`)"
                >
                  <header class="settings-ai-entry-card__head">
                    <span
                      class="settings-ai-entry-card__avatar"
                      :data-tone="entryAvatarTone(agent.name)"
                      aria-hidden="true"
                      >{{ entryAvatarInitial(agent.name) }}</span
                    >
                    <div class="settings-ai-entry-card__title">
                      <strong :title="agent.name">{{ agent.name }}</strong>
                      <small :title="agent.command">{{ agent.command }}</small>
                    </div>
                    <el-icon
                      v-if="settings.acpAdapterId === `custom:${agent.id}`"
                      class="settings-ai-entry-card__check"
                      aria-hidden="true"
                      ><Check
                    /></el-icon>
                  </header>
                  <p v-if="agent.args" class="settings-ai-entry-card__desc">{{ agent.args }}</p>
                  <footer class="settings-ai-entry-card__foot">
                    <span class="settings-ai-entry-card__chip">{{ t("settings.ai.custom") }}</span>
                    <div class="settings-ai-entry-card__actions" @click.stop @keydown.stop>
                      <el-button
                        text
                        :icon="Delete"
                        :title="t('common.delete')"
                        :aria-label="t('common.delete')"
                        @click="removeAgent(agent)"
                      />
                    </div>
                  </footer>
                </article>
              </div>
              <div class="settings-ai-agent-switch"
                ><span class="app-inline-label"
                  ><strong>{{ t("settings.acp.injectRules") }}</strong
                  ><AppHintIcon :text="t('settings.acp.injectRulesHint')" /></span
                ><el-switch v-model="settings.acpInjectRules" size="small"
              /></div>
            </section>
            <section class="settings-ai-card settings-ai-custom-agent">
              <strong>{{ t("settings.acp.customTitle") }}</strong>
              <div class="settings-ai-two-columns">
                <el-input
                  v-model="newAgent.name"
                  :placeholder="t('settings.acp.namePlaceholder')"
                /><el-input
                  v-model="newAgent.command"
                  :placeholder="t('settings.acp.commandPlaceholder')"
                />
              </div>
              <div class="settings-ai-add-row">
                <el-input
                  v-model="newAgent.args"
                  :placeholder="t('settings.acp.argsPlaceholder')"
                /><el-button type="primary" @click="addAgent">{{ t("common.add") }}</el-button>
              </div>
            </section>
          </div>
        </section>

        <DatabaseSafetySettingsSection v-else-if="active === 'database'" />

        <section v-else-if="active === 'data'" class="settings-flat-page">
          <div class="settings-section-label">{{ t("settings.data.management") }}</div>
          <div class="settings-data-row">
            <span class="app-inline-label"
              ><strong>{{ t("settings.data.export") }}</strong
              ><AppHintIcon :text="t('settings.data.exportHint')" /></span
            ><div class="settings-data-row__actions">
              <el-button @click="exportConfig">{{ t("common.export") }}</el-button>
            </div>
          </div>
          <div class="settings-data-row">
            <span class="app-inline-label"
              ><strong>{{ t("settings.data.import") }}</strong
              ><AppHintIcon :text="t('settings.data.importHint')" /></span
            ><el-button @click="startImportConfig">{{ t("common.import") }}</el-button
            ><input
              ref="importInput"
              type="file"
              accept="application/json"
              hidden
              @change="importConfig"
            />
          </div>
          <div class="settings-data-row">
            <span class="app-inline-label"
              ><strong>{{ t("settings.data.aiHistory") }}</strong
              ><AppHintIcon :text="t('settings.data.aiHistoryHint')" /></span
            ><el-button @click="clearAiHistory">{{ t("common.clear") }}</el-button>
          </div>
          <div class="settings-divider" />
          <DataStorageUsagePanel />
        </section>

        <section v-else class="settings-about">
          <div class="settings-section-label">{{ t("settings.cat.about") }}</div>
          <div class="settings-about__logo"><img :src="MinCodeMark" alt="" /></div>
          <h2>MinCode</h2>
          <p>{{ t("settings.about.tagline") }}</p>
          <dl>
            <div>
              <dt>{{ t("settings.about.version") }}</dt>
              <dd>{{ version }}</dd>
            </div>
            <div>
              <dt>{{ t("settings.about.platform") }}</dt>
              <dd>{{ platform }}</dd>
            </div>
          </dl>
          <div class="settings-update-actions">
            <el-button
              :icon="Refresh"
              :loading="updateChecking"
              :disabled="updateDownloading || Boolean(appUpdate.readyVersion)"
              @click="checkUpdate"
            >
              {{ updateChecking ? t("settings.update.checking") : t("settings.checkUpdate") }}
            </el-button>
            <el-switch
              :model-value="appUpdate.autoCheck"
              :active-text="t('settings.update.autoCheck')"
              @update:model-value="appUpdate.setAutoCheck(Boolean($event))"
            />
            <span v-if="appUpdate.lastCheckedAt" class="settings-update-actions__meta">
              {{ t("settings.update.lastChecked", { time: formatUpdateDate(new Date(appUpdate.lastCheckedAt).toISOString()) }) }}
            </span>
          </div>
          <el-alert
            v-if="updateError"
            class="settings-update-alert"
            type="error"
            :closable="false"
            :title="t('settings.update.failed', { message: updateError })"
            show-icon
          />
          <section v-if="updateInfo" class="settings-update-card">
            <header class="settings-update-card__header">
              <div>
                <strong>{{
                  updateInfo.available
                    ? t("settings.update.available", { version: updateInfo.version })
                    : t("settings.update.latest")
                }}</strong>
                <p>{{
                  !updateInfo.available
                    ? t("settings.update.latestHint")
                    : t(updateInfo.releaseURL ? "settings.update.manualDownload" : "settings.update.availableHint")
                }}</p>
              </div>
              <el-tag :type="updateInfo.available ? 'warning' : 'success'" effect="plain">
                {{ updateInfo.available ? t("settings.update.newVersion") : t("settings.update.upToDate") }}
              </el-tag>
            </header>
            <dl class="settings-update-card__meta">
              <div>
                <dt>{{ t("settings.update.currentVersion") }}</dt>
                <dd>{{ updateInfo.currentVersion }}</dd>
              </div>
              <div v-if="updateInfo.available">
                <dt>{{ t("settings.update.latestVersion") }}</dt>
                <dd>{{ updateInfo.version }}</dd>
              </div>
              <div v-if="updateInfo.available">
                <dt>{{ t("settings.update.pubDate") }}</dt>
                <dd>{{ formatUpdateDate(updateInfo.pubDate) }}</dd>
              </div>
              <div v-if="updateInfo.available && !updateInfo.releaseURL">
                <dt>{{ t("settings.update.packageSize") }}</dt>
                <dd>{{ formatUpdateSize(updateInfo.downloadSize) }}</dd>
              </div>
            </dl>
            <!-- Actions sit above the notes, which scroll on their own, so a long changelog never hides them. -->
            <el-progress
              v-if="updateDownloading && appUpdate.progressPercent !== undefined"
              :percentage="appUpdate.progressPercent"
              :aria-label="t('settings.update.downloading')"
            />
            <footer v-if="appUpdate.readyVersion" class="settings-update-card__footer">
              <el-button type="primary" :loading="appUpdate.restarting" @click="restartAfterUpdate">
                {{ t("settings.update.restart") }}
              </el-button>
              <span>{{ t("settings.update.ready", { version: appUpdate.readyVersion }) }}</span>
            </footer>
            <footer v-else-if="updateInfo.available" class="settings-update-card__footer">
              <el-button v-if="updateInfo.releaseURL" type="primary" :icon="TopRight" @click="openUpdateRelease">
                {{ t("settings.update.openRelease") }}
              </el-button>
              <el-button
                v-else
                type="primary"
                :icon="Download"
                :loading="updateDownloading"
                :disabled="updateChecking"
                @click="downloadUpdate"
              >
                {{
                  updateDownloading
                    ? t("settings.update.downloading")
                    : t("settings.update.download")
                }}
              </el-button>
              <el-button
                v-if="appUpdate.skippedVersion !== updateInfo.version"
                text
                @click="skipUpdateVersion(updateInfo.version)"
              >
                {{ t("settings.update.skipVersion") }}
              </el-button>
              <el-button v-else text @click="appUpdate.clearSkippedVersion()">
                {{ t("settings.update.unskipVersion") }}
              </el-button>
            </footer>
            <div class="settings-update-card__notes">
              <el-scrollbar max-height="360px">
                <div class="settings-update-card__notes-body">
                  <span>{{ t("settings.update.notes") }}</span>
                  <AppUpdateNotes v-if="updateInfo.notes" :notes="updateInfo.notes" />
                  <p v-else>{{ t("settings.update.noNotes") }}</p>
                </div>
              </el-scrollbar>
            </div>
          </section>
        </section>
        </main>
      </el-scrollbar>
    </div>
  </el-dialog>
  <el-dialog
    v-model="mcpJsonEditorOpen"
    append-to-body
    class="settings-mcp-json-dialog"
    width="min(920px, calc(100vw - 40px))"
    :close-on-click-modal="false"
    :title="t('settings.mcp.editJsonTitle')"
  >
    <p class="settings-mcp-json-dialog__hint">{{ t("settings.mcp.editJsonHint") }}</p>
    <div class="settings-mcp-json-dialog__editor">
      <MonacoTextEditor
        v-if="mcpJsonEditorOpen"
        v-model="mcpJsonText"
        path="mcp.json"
        :open-paths="['mcp.json']"
        :preferences="DEFAULT_EDITOR_PREFERENCES"
        @save="saveMcpJson"
      />
    </div>
    <template #footer>
      <el-button @click="mcpJsonEditorOpen = false">{{ t("common.cancel") }}</el-button>
      <el-button @click="formatMcpJson">{{ t("settings.mcp.formatJson") }}</el-button>
      <el-button type="primary" @click="saveMcpJson">{{ t("common.save") }}</el-button>
    </template>
  </el-dialog>
</template>
