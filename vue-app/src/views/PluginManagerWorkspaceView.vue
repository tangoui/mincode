<script setup lang="ts">
import { computed, defineAsyncComponent, h, onMounted, onBeforeUnmount, ref, watch } from "vue";
import PluginCatalogToolbar from "@/components/plugins/PluginCatalogToolbar.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMetadata from "@/components/plugins/PluginMetadata.vue";
import PluginAiGrants from "@/components/plugins/PluginAiGrants.vue";
import PluginDatabaseDriverTrust from "@/components/plugins/PluginDatabaseDriverTrust.vue";
import PluginCallGrants from "@/components/plugins/PluginCallGrants.vue";
import PluginDatabaseSchemaGrants from "@/components/plugins/PluginDatabaseSchemaGrants.vue";
import PluginWalkthroughList from "@/components/plugins/PluginWalkthroughList.vue";
import PluginProvidedServices from "@/components/plugins/PluginProvidedServices.vue";
import { mergePluginCatalog, filterPluginCatalog, countPluginCategories, pluginCanStart, pluginCategories, type CatalogFilter, type PluginCategory } from "@/domain/plugins/catalog";
import type { PluginInstallPreview } from "@/domain/plugins/install-preview";
import { pluginCapabilityTone } from "@/domain/plugins/capability-tone";
import { describePluginInstallError, type PluginInstallErrorInfo } from "@/domain/plugins/install-error";
import { dynamicStatusBarItemIds } from "@/domain/plugins/status-bar";
import { languagePreviewSummary } from "@/domain/editor/language-contributions";
import { aiPromptPreviewSummary } from "@/domain/ai/plugin-prompts";
import { localizationPreviewSummary } from "@/domain/i18n/locale-pack";
import { customEditorPreviewSummary } from "@/domain/plugins/custom-editors";
import { formatTerminalArgv, terminalProfilePreviewSummary } from "@/domain/terminal/profiles";
import { pluginTaskPreviewSummary } from "@/domain/tasks/plugin-tasks";
import { terminalColorSchemePreviewSummary } from "@/domain/themes/terminal-color-schemes";
import { editorThemePreviewSummary } from "@/domain/themes/editor-themes";
import { commandSnippetPreviewSummary } from "@/domain/terminal/command-snippets";
import { terminalLinkPreviewSummary } from "@/domain/terminal/terminal-links";
import { fileIconPreviewSummary } from "@/domain/projects/file-icon-theme";
import { jsonValidationPreviewSummary } from "@/domain/plugins/json-validation";
import { formatterPreviewSummary } from "@/domain/plugins/formatters";
import { scheduleIntervalParts, schedulePreviewSummary, scheduleTitle } from "@/domain/plugins/schedules";
import { formatAccelerator, globalShortcutPreviewSummary, globalShortcutsChangedByUpdate } from "@/domain/keybindings/plugin-global-shortcuts";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import { usePluginSchedulesStore } from "@/stores/plugin-schedules";
import { linterPreviewSummary } from "@/domain/plugins/linters";
import { aiContextProviderPreviewSummary } from "@/domain/ai/plugin-context";
import { httpCollectionPreviewSummary } from "@/domain/http/plugin-collections";
import { fileTemplatePreviewSummary } from "@/domain/projects/file-templates";
import { httpCodeGeneratorPreviewSummary } from "@/domain/plugins/http-code-generators";
import { languageServerPreviewSummary } from "@/domain/plugins/language-servers";
import { mcpServerPreviewSummary } from "@/domain/plugins/mcp-servers";
import { treeViewPreviewSummary } from "@/domain/plugins/tree-views";
import { testProviderPreviewSummary } from "@/domain/plugins/test-providers";
import { languageFeaturePreviewSummary } from "@/domain/plugins/language-features";
import { walkthroughPreviewSummary } from "@/domain/plugins/walkthroughs";
import { dashboardWidgetPreviewSummary } from "@/domain/plugins/dashboard-widgets";
import { databaseDriverPreviewSummary } from "@/domain/plugins/database-drivers";
import { searchProviderPreviewSummary } from "@/domain/plugins/search-providers";
import { servicePreviewSurfaces } from "@/domain/plugins/services";
import { aiCatalogPreviewSummary } from "@/domain/plugins/ai-catalogs";
import { aiProviderPreviewCount } from "@/domain/plugins/ai-providers";
import { uriHandlerPreviewSummary } from "@/domain/plugins/uri-handlers";
import { trayPreviewTitles } from "@/domain/plugins/tray";
import { settingsPagePreviewTitle } from "@/domain/plugins/settings-pages";
import { useRoute, useRouter } from "vue-router";
import "@/styles/workspaces/plugins/workspace.css";
import { ElCheckbox, ElMessage, ElMessageBox } from "element-plus";
import { ArrowDown, Check, DArrowLeft, DArrowRight, Delete, Download, InfoFilled, Refresh, Setting, Tickets, TopRight, Upload, WarningFilled } from "@element-plus/icons-vue";
import { listConfigurablePlugins } from "@/services/plugin-configuration-service";
import { openPluginSettings } from "@/services/settings-navigation";
import { localizedPluginTitle } from "@/services/plugin-titles";
import { pluginViewIcon } from "@/services/plugin-view-icon";
import { useVueI18n } from "@/i18n/messages";
import {
  getPluginLogs,
  listPluginCategories,
  previewLocalPlugin,
  previewRemotePlugin,
  upgradeLocalPlugin,
  type OfficialPlugin,
  installLocalPlugin,
  listPlugins,
  listPluginViews,
  reloadPlugin,
  setPluginEnabled,
  setPluginScope,
  clearPluginData,
  exportPluginDataArchive,
  importPluginDataArchive,
  inspectPluginDataArchive,
  uninstallPlugin,
  type PluginLogEntry,
  type PluginMeta,
  type PluginView,
  type PluginScope,
  type PluginState,
} from "@/services/plugin-service";
import { chooseNativeDirectory, chooseNativeFile, chooseNativeSaveFile, isDesktopRuntime, nativeErrorMessage, listenAppEvent } from "@/services/desktop-bridge";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { useWorkbenchStore } from "@/stores/workbench";

import { loadPluginMenu, savePluginMenu, PLUGIN_CATALOG_EVENT } from "@/services/plugin-menu-preferences";
const menuPlugins = ref<string[]>([]);
const menuBusy = ref(false);
async function toggleMenu(id: string, enabled: boolean) {
  if (menuBusy.value) return;
  menuBusy.value = true;
  try {
    const current = await loadPluginMenu();
    const next = enabled ? [...new Set([...current, id])] : current.filter(item => item !== id);
    await savePluginMenu(next);
    menuPlugins.value = next;
  } catch (error) { ElMessage.error(String(error)); }
  finally { menuBusy.value = false; }
}
const PluginCapabilityCatalog = defineAsyncComponent(
  () => import("@/components/plugins/PluginCapabilityCatalog.vue")
);
const PluginDevelopmentPanel = defineAsyncComponent(
  () => import("@/components/plugins/development/PluginDevelopmentPanel.vue")
);
const PluginServicesCatalog = defineAsyncComponent(
  () => import("@/components/plugins/PluginServicesCatalog.vue")
);
// Full trust UI and its service stay out of the entry chain (scripts/plugin-full-trust.test.mjs).
const PluginFullTrust = defineAsyncComponent(() => import("@/components/plugins/PluginFullTrust.vue"));
const PluginFullTrustSummary = defineAsyncComponent(() => import("@/components/plugins/PluginFullTrustSummary.vue"));

type PluginManagerSection = "plugins" | "services" | "capabilities" | "development";

const { locale, localeChain, t } = useVueI18n();
const PluginAssociationSettings = defineAsyncComponent(() => import("@/components/plugins/PluginAssociationSettings.vue"));
const route = useRoute();
const router = useRouter();
const workbench = useWorkbenchStore();
const plugins = ref<PluginMeta[]>([]);
const pluginNamesById = computed(() => Object.fromEntries(plugins.value.map((item) => [item.id, item.name])));
// Plugins that declare contributes.configuration get a "Settings" link.
const configurablePluginIds = ref(new Set<string>());
const officialPlugins = ref<OfficialPlugin[]>([]);
function searchFromQuery(value: unknown) {
  return typeof value === "string" ? value.trim().slice(0, 64) : "";
}

const filters = ref<CatalogFilter>({ category: "all", source: "all", status: "all", search: searchFromQuery(route.query.search) });
const categories = ref<PluginCategory[]>([]);
const allEntries = computed(() => mergePluginCatalog(plugins.value, officialPlugins.value));
const catalogEntries = computed(() => filterPluginCatalog(allEntries.value, filters.value));
const page = ref(1);
const pageSize = 24;
const pageEntries = computed(() => catalogEntries.value.slice((page.value - 1) * pageSize, page.value * pageSize));
const pageCount = computed(() => Math.max(1, Math.ceil(catalogEntries.value.length / pageSize)));
watch(catalogEntries, () => { page.value = 1; });
const categoryCounts = computed(() => countPluginCategories(allEntries.value, filters.value));
const visibleCategories = computed(() => {
  const used = new Set<PluginCategory>([...categories.value, ...allEntries.value.map(entry => entry.category)]);
  return pluginCategories.filter(category => used.has(category));
});
const detailId = ref("");
const detail = computed(() => allEntries.value.find(entry => entry.id === detailId.value));
const detailsVisible = ref(false);
const previewPath = ref("");
const preview = ref<PluginInstallPreview>();
const previewVisible = ref(false);
const previewLoading = ref(false);
// Any non-read action could later skip its confirmation under full trust, granted only in the plugin manager.
const previewHasNonReadActions = computed(() => preview.value?.manifest.contributes?.actions
  ?.some(action => action.risk !== "read") === true);
const previewError = ref<PluginInstallErrorInfo>();
/** Supported MinCode range of a catalog entry's latest release, for display. */
let previewGeneration = 0;
let refreshGeneration = 0;
let logsGeneration = 0;
let disposed = false;
function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function invalidatePreview() {
  previewGeneration++;
  preview.value = undefined;
  previewPath.value = "";
  previewLoading.value = false;
  previewError.value = undefined;
}

watch(previewVisible, visible => { if (!visible) invalidatePreview(); }, { flush: "sync" });
const officialError = ref("");
const officialBusy = ref("");
const pluginViews = ref<PluginView[]>([]);
const loading = ref(false);
const busyId = ref("");
const listError = ref("");
const sourcePath = ref("");
const installing = ref(false);
const choosingSource = ref(false);
watch(sourcePath, invalidatePreview, { flush: "sync" });
const logsVisible = ref(false);
const logsLoading = ref(false);
const logsTitle = ref("");
const logs = ref<PluginLogEntry[]>([]);
function sectionFromQuery(tab: unknown): PluginManagerSection {
  return tab === "services" || tab === "capabilities" || tab === "development" ? tab : "plugins";
}

const activeSection = ref<PluginManagerSection>(sectionFromQuery(route.query.tab));
const viewsByPlugin = computed(() => {
  const grouped = new Map<string, PluginView[]>();
  for (const view of pluginViews.value) {
    const views = grouped.get(view.pluginId) ?? [];
    views.push(view);
    grouped.set(view.pluginId, views);
  }
  return grouped;
});

function localizedViewTitle(view: PluginView) {
  return localizedPluginTitle(view);
}

function openPluginView(view: PluginView) {
  openPluginViewTab(workbench, router, view.pluginId, view.id, localizedViewTitle(view));
}

function stateType(state: PluginState) {
  if (state === "running") return "success";
  if (state === "error") return "danger";
  if (state === "starting" || state === "stopping") return "warning";
  return "info";
}

function stateLabel(state: PluginState) {
  return t(`plugins.state.${state}` as Parameters<typeof t>[0]);
}

async function refresh() {
  if (loading.value) return;
  if (!isDesktopRuntime()) {
    listError.value = t("common.desktopRequired");
    return;
  }
  loading.value = true;
  listError.value = "";
  try {
    const generation = ++refreshGeneration;
    const [installed, views, menu, categoryList, configurable] = await Promise.all([
      listPlugins(), listPluginViews(), loadPluginMenu(), listPluginCategories(),
      // A failure here only hides the Settings links; it never blocks the list.
      listConfigurablePlugins().catch(() => [] as string[]),
    ]);
    if (disposed || generation !== refreshGeneration) return;
    plugins.value = installed;
    configurablePluginIds.value = new Set(configurable);
    pluginViews.value = views;
    menuPlugins.value = menu;
    categories.value = categoryList;
    officialPlugins.value = [];
    officialError.value = "";
    window.dispatchEvent(new Event(PLUGIN_CATALOG_EVENT));
  } catch (error) {
    if (!disposed) listError.value = String(error);
  } finally {
    loading.value = false;
  }
}

async function persistScope(plugin: PluginMeta, scope: PluginScope, workspaces: string[]) {
  if (busyId.value) return;
  busyId.value = plugin.id;
  try {
    await setPluginScope(plugin.id, scope, workspaces);
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
    await refresh();
  } finally {
    busyId.value = "";
  }
}

function changeScope(plugin: PluginMeta, value: unknown) {
  const scope: PluginScope = value === "workspace" ? "workspace" : "global";
  if (scope === plugin.scope) return;
  const workspaces = scope === "workspace"
    ? (plugin.workspaces.length ? [...plugin.workspaces] : [workbench.activeWorkspaceId])
    : [];
  void persistScope(plugin, scope, workspaces);
}

function changeScopeWorkspaces(plugin: PluginMeta, value: unknown) {
  const workspaces = Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
  void persistScope(plugin, "workspace", workspaces);
}

async function toggle(plugin: PluginMeta, enabled: boolean) {
  if (busyId.value || officialBusy.value || (enabled && !pluginCanStart(plugin))) return;
  const previous = plugin.enabled;
  if (enabled && plugin.source === "local") {
    try {
      await ElMessageBox.confirm(
        [
          t("plugins.enableLocalConfirm", {
            capabilities: plugin.capabilities.length ? plugin.capabilities.join(", ") : t("plugins.noCapabilities"),
          }),
          plugin.capabilities.includes("database.driver") ? t("plugins.capabilityDatabaseDriver") : "",
          plugin.capabilities.includes("database.schema") ? t("plugins.capabilityDatabaseSchema") : "",
          plugin.capabilities.includes("remote.context") ? t("plugins.capabilityRemoteContext") : "",
          plugin.capabilities.includes("ui.wasm") ? t("plugins.capabilityUiWasm") : "",
          plugin.capabilities.includes("ai.config") ? t("plugins.capabilityAiConfig") : "",
        ]
          .filter(Boolean)
          .join(" "),
        t("plugins.enableCapabilityTitle"),
        { type: "warning", confirmButtonText: t("plugins.enable"), cancelButtonText: t("common.cancel") }
      );
    } catch {
      plugin.enabled = previous;
      return;
    }
  }
  busyId.value = plugin.id;
  plugin.enabled = enabled;
  try {
    await setPluginEnabled(plugin.id, enabled);
    await refresh();
  } catch (error) {
    plugin.enabled = previous;
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    busyId.value = "";
  }
}

async function retry(plugin: PluginMeta) {
  if (busyId.value || officialBusy.value || !pluginCanStart(plugin)) return;
  busyId.value = plugin.id;
  try {
    await reloadPlugin(plugin.id);
    ElMessage.success(t("plugins.reloadSuccess"));
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
    await refresh();
  } finally {
    busyId.value = "";
  }
}

async function chooseSourceDirectory() {
  if (choosingSource.value || installing.value || previewLoading.value || disposed) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }

  let accepted = false;
  choosingSource.value = true;
  try {
    const selected = await chooseNativeDirectory(t("plugins.chooseDirectoryTitle"));
    if (selected && !disposed && !installing.value && !previewLoading.value) {
      sourcePath.value = selected;
      accepted = true;
    }
  } catch (error) {
    if (!disposed) ElMessage.error(String(error));
  } finally {
    choosingSource.value = false;
  }
  if (accepted) await install();
}

// schedules 贡献点：安装预览列出后台定时任务（宿主按间隔唤醒插件调用只读动作）。
const previewSchedules = computed(() => schedulePreviewSummary(preview.value?.manifest.contributes));
const pluginSchedules = usePluginSchedulesStore();
function scheduleInterval(minutes: number) {
  const [value, unit] = scheduleIntervalParts(minutes);
  return t(unit === "hours" ? "pluginContrib.schedules.everyHours" : "pluginContrib.schedules.everyMinutes", { n: value });
}
function scheduleList(items: readonly { title: string; titleLocalized?: Record<string, string>; intervalMinutes: number }[]) {
  return items
    .map((item) => t("pluginContrib.schedules.item", { title: scheduleTitle(item, locale.value), interval: scheduleInterval(item.intervalMinutes) }))
    .join(t("pluginContrib.schedules.separator"));
}
function scheduleLine(pluginId: string) {
  return scheduleList(pluginSchedules.byPlugin.get(pluginId) ?? []);
}
// globalShortcuts 贡献点：安装预览列出申请的全局快捷键（标题 + 默认按键），默认关闭。
const previewGlobalShortcuts = computed(() => globalShortcutPreviewSummary(preview.value?.manifest.contributes));
const keyboardPlatform = currentKeyboardPlatform();
function globalShortcutList(items: ReturnType<typeof globalShortcutPreviewSummary>) {
  return items
    .map((item) => t("pluginContrib.globalShortcuts.item", { title: resolvePluginTitle(item, locale.value), key: formatAccelerator(item.key, keyboardPlatform) }))
    .join(t("pluginContrib.globalShortcuts.separator"));
}
const previewGlobalShortcutList = computed(() => globalShortcutList(previewGlobalShortcuts.value));
// menus.tray：安装预览列出加到系统托盘菜单的命令（清单标题）。
const previewTrayTitles = computed(() => trayPreviewTitles(preview.value?.manifest.contributes, localeChain.value));
// settingsPages：安装预览说明在 设置 → 插件 中提供的设置页面（不是权限扩大）。
const previewSettingsPageTitle = computed(() => settingsPagePreviewTitle(preview.value?.manifest.contributes, localeChain.value));
// Host-validated data provider entries (install_preview.go); titles are the manifest's own text.
const previewDataProviders = (point: string) =>
  (preview.value?.dataProviders ?? [])
    .filter((provider) => provider.point === point)
    .map((provider) => ({ id: provider.id, title: resolvePluginTitle(provider, localeChain.value) }));
const previewSessionSources = computed(() => previewDataProviders("sessionSources"));
const previewScmProviders = computed(() => previewDataProviders("scmCommitMessage"));
const previewDecorationProviders = computed(() => previewDataProviders("fileDecorations"));
// 更新包改变了用户已开启的条目（按键或执行内容）：安装后这些条目先关闭，需用户确认后重新开启。
const previewGlobalShortcutsChanged = computed(() =>
  globalShortcutList(globalShortcutsChangedByUpdate(previewGlobalShortcuts.value, preview.value?.globalShortcutsChanged)),
);

// Marketplace popularity on cards, compact for large numbers (1.2k / 1.2万).
async function chooseSourceZip() {
  if (choosingSource.value || installing.value || previewLoading.value || disposed) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }

  let accepted = false;
  choosingSource.value = true;
  try {
    const selected = await chooseNativeFile(t("plugins.choosePackageTitle"), [
      { DisplayName: t("plugins.packageFiles"), Pattern: "*.min" },
      { DisplayName: t("plugins.legacyPackageFiles"), Pattern: "*.zip" },
    ]);
    if (selected && !disposed && !installing.value && !previewLoading.value) {
      sourcePath.value = selected;
      accepted = true;
    }
  } catch (error) {
    if (!disposed) ElMessage.error(String(error));
  } finally {
    choosingSource.value = false;
  }
  if (accepted) await install();
}

async function install() {
  const path = sourcePath.value.trim();
  if (!path || installing.value || previewLoading.value || choosingSource.value || disposed) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  invalidatePreview();
  const generation = previewGeneration;
  previewVisible.value = true;
  previewLoading.value = true;
  try {
    const result = /^https:\/\//i.test(path) ? await previewRemotePlugin(path) : await previewLocalPlugin(path);
    if (disposed || generation !== previewGeneration || !previewVisible.value) return;
    preview.value = result;
    previewPath.value = path;
  } catch (error) {
    if (disposed || generation !== previewGeneration) return;
    const info = describePluginInstallError(String(error));
    previewError.value = info;
  } finally {
    if (generation === previewGeneration) previewLoading.value = false;
  }
}

// statusBar 贡献点的 dynamic 条目可在运行时更新文字，安装预览需要明示。
const previewDynamicStatusBarIds = computed(() => dynamicStatusBarItemIds(preview.value?.manifest.contributes));
// languages 贡献点：安装预览列出新增语言与为内置语言追加的文件关联。
const previewLanguages = computed(() => languagePreviewSummary(preview.value?.manifest.contributes));
// aiPrompts 贡献点：安装预览说明新增的 AI 提示词与技能数量（第三方文本，需手动调用）。
const previewAiPrompts = computed(() => aiPromptPreviewSummary(preview.value?.manifest.contributes));
// localizations 贡献点：安装预览列出界面语言包（仅在设置中手动选择后生效，不能覆盖简体中文/English 与安全审批文案）。
const previewLocalizations = computed(() => localizationPreviewSummary(preview.value?.manifest.contributes));
// customEditors 贡献点：安装预览列出每个自定义编辑器的文件模式、默认/可选以及是否可写回文件。
const previewCustomEditors = computed(() => customEditorPreviewSummary(preview.value?.manifest.contributes, preview.value?.manifest.id));
// terminalProfiles 贡献点：安装预览列出每个终端配置声明的命令行（不沙箱，首次启动与每次变更都需确认）。
const previewTerminalProfiles = computed(() => terminalProfilePreviewSummary(preview.value?.manifest.contributes, preview.value?.manifest.id));
// tasks / terminalColorSchemes：安装预览列出任务命令行与检测提供程序（每个项目首次运行需确认），以及终端配色（仅数据）。
const previewTasks = computed(() => pluginTaskPreviewSummary(preview.value?.manifest.contributes));
const previewTerminalColorSchemes = computed(() => terminalColorSchemePreviewSummary(preview.value?.manifest.contributes));
// editorThemes：安装预览列出编辑器配色（仅数据）。
const previewEditorThemes = computed(() => editorThemePreviewSummary(preview.value?.manifest.contributes));
// commandSnippets / terminalLinks：安装预览列出片段库（仅插入不执行）与链接规则（固定主机 / 本地文件）。
const previewCommandSnippets = computed(() => commandSnippetPreviewSummary(preview.value?.manifest.contributes));
const previewTerminalLinks = computed(() => terminalLinkPreviewSummary(preview.value?.manifest.contributes));
// fileIcons 贡献点：安装预览列出文件图标主题（仅以图片显示，需在设置中手动选择后生效）。
const previewFileIcons = computed(() => fileIconPreviewSummary(preview.value?.manifest.contributes));
// jsonValidation / formatters：安装预览列出 JSON Schema 适用的文件模式，以及格式化程序（需用户选择后才运行、格式化时发送文档内容）。
const previewJsonValidation = computed(() => jsonValidationPreviewSummary(preview.value?.manifest.contributes));
const previewFormatters = computed(() => formatterPreviewSummary(preview.value?.manifest.contributes));
// linters / aiContextProviders：安装预览列出代码检查程序（需在设置中选择后才运行、检查时发送文档内容）与 AI 上下文 @提及（仅在用户预览并确认后附加）。
const previewLinters = computed(() => linterPreviewSummary(preview.value?.manifest.contributes));
const previewAiContextProviders = computed(() => aiContextProviderPreviewSummary(preview.value?.manifest.contributes));
// httpCollections / fileTemplates：安装预览列出请求集合模板（只读、不含认证、不自动发送）与文件/项目模板（预览确认后仅新建）。
const previewHttpCollections = computed(() => httpCollectionPreviewSummary(preview.value?.manifest.contributes));
const previewFileTemplates = computed(() => fileTemplatePreviewSummary(preview.value?.manifest.contributes));
// httpCodeGenerators 贡献点：安装预览列出 HTTP 代码生成器（仅模板数据，基于脱敏请求在本地渲染）。
const previewHttpCodeGenerators = computed(() => httpCodeGeneratorPreviewSummary(preview.value?.manifest.contributes));
// languageServers 贡献点：安装预览列出语言服务器（启用并确认完整启动命令后才运行）。
const previewLanguageServers = computed(() => languageServerPreviewSummary(preview.value?.manifest.contributes));
// mcpServers 贡献点：安装预览列出 AI 连接器（在设置中启用并确认完整启动命令后才运行）。
const previewMcpServers = computed(() => mcpServerPreviewSummary(preview.value?.manifest.contributes));
const previewTreeViews = computed(() => treeViewPreviewSummary(preview.value?.manifest.contributes));
const previewTestProviders = computed(() => testProviderPreviewSummary(preview.value?.manifest.contributes));
const previewLanguageFeatures = computed(() => languageFeaturePreviewSummary(preview.value?.manifest.contributes));
// walkthroughs 贡献点：安装预览列出上手指南（只在用户主动打开时显示）及可运行本插件命令的步骤数。
const previewWalkthroughs = computed(() => walkthroughPreviewSummary(preview.value?.manifest.contributes));
// dashboardWidgets 贡献点：安装预览列出仪表盘小组件及数据小组件在可见时运行的只读动作。
const previewDashboardWidgets = computed(() => dashboardWidgetPreviewSummary(preview.value?.manifest.contributes));
const previewDatabaseDrivers = computed(() => databaseDriverPreviewSummary(preview.value?.manifest.contributes));
// searchProviders / uriHandlers：安装预览列出命令面板搜索前缀（输入前缀时把查询发送给插件）与 mincode:// 链接处理程序（每次都需用户确认）。
const previewSearchProviders = computed(() => searchProviderPreviewSummary(preview.value?.manifest.contributes));
const previewUriHandlers = computed(() => uriHandlerPreviewSummary(preview.value?.manifest.contributes));
// services：清单显式声明了新入口（SFTP 文件、结果集、HTTP 响应、终端选区）时提示这些内容会在用户点击后交给插件。
const previewServiceSurfaces = computed(() => servicePreviewSurfaces(preview.value?.manifest.contributes));
// aiCatalogs：安装预览列出“设置 → AI → 发现”中新增的连接器/技能来源（搜索时把关键词发送给插件，添加前需用户确认）。
const previewAiCatalogs = computed(() => aiCatalogPreviewSummary(preview.value?.manifest.contributes));
const previewAiProviders = computed(() => aiProviderPreviewCount(preview.value?.manifest.contributes));
const canConfirmInstall = computed(() => {
  if (!preview.value || previewLoading.value || installing.value) return false;
  return !preview.value.installed && previewPath.value === sourcePath.value.trim();
});

async function confirmInstall() {
  if (!canConfirmInstall.value || !preview.value) return;
  if (!isDesktopRuntime()) { ElMessage.warning(t("common.desktopRequired")); return; }
  const path = preview.value.sourcePath;
  const installedId = preview.value.manifest.id;
  installing.value = true;
  try {
    await installLocalPlugin(path);
    if (disposed) return;
    previewVisible.value = false;
    sourcePath.value = "";
    await refresh();
    await enableAfterInstall(installedId);
  } catch (error) {
    if (!disposed) {
      invalidatePreview();
      previewError.value = describePluginInstallError(String(error));
    }
  } finally {
    installing.value = false;
  }
}

// The install preview already listed the capabilities the user accepted, so a
// fresh install starts right away instead of asking a second time.
async function enableAfterInstall(id: string) {
  const plugin = plugins.value.find((item) => item.id === id);
  if (!plugin || plugin.enabled || !pluginCanStart(plugin)) {
    ElMessage.success(t("plugins.installSuccess"));
    return;
  }
  busyId.value = plugin.id;
  try {
    await setPluginEnabled(plugin.id, true);
    ElMessage.success(t("plugins.installedAndEnabled"));
  } catch (error) {
    ElMessage.warning(t("plugins.installedEnableFailed", { error: nativeErrorMessage(error) }));
  } finally {
    busyId.value = "";
    await refresh();
  }
}

async function upgrade(plugin: PluginMeta, official?: OfficialPlugin) {
  if (busyId.value || officialBusy.value) return;
  busyId.value = plugin.id;
  try {
    let path = "";
    {
      if (!isDesktopRuntime()) throw new Error(t("common.desktopRequired"));
      path = (await chooseNativeFile(t("plugins.chooseUpgradePackageTitle"), [
        { DisplayName: t("plugins.packageFiles"), Pattern: "*.min" },
        { DisplayName: t("plugins.legacyPackageFiles"), Pattern: "*.zip" },
      ]) ?? "").trim();
      if (!path) return;
    }
    const version = official ? `v${plugin.version} → v${official.version}` : `v${plugin.version} → ${path}`;
    await ElMessageBox.confirm(
      t("plugins.upgradeConfirm", { name: plugin.name, version }),
      t("plugins.upgradeTitle"),
      { type: "warning", confirmButtonText: t("plugins.upgrade"), cancelButtonText: t("common.cancel") },
    );
    await upgradeLocalPlugin(plugin.id, path);
    ElMessage.success(t("plugins.upgradeSuccess"));
    await refresh();
  } catch (error) {
    if (error !== "cancel" && error !== "close") {
      ElMessage.error(String(error));
      await refresh();
    }
  } finally {
    busyId.value = "";
  }
}

async function uninstall(plugin: Pick<PluginMeta, "id" | "name">) {
  if (busyId.value || officialBusy.value) return;
  const purgeData = ref(false);
  try {
    await ElMessageBox.confirm(
      () => h("div", { class: "plugin-uninstall-confirm" }, [
        h("p", t(purgeData.value ? "plugins.uninstallConfirmPurge" : "plugins.uninstallConfirm", { name: plugin.name })),
        h(ElCheckbox, {
          modelValue: purgeData.value,
          "onUpdate:modelValue": (value: string | number | boolean) => {
            purgeData.value = Boolean(value);
          },
        }, () => t("plugins.uninstallPurgeData")),
      ]),
      t("plugins.uninstall"),
      { type: "warning", confirmButtonText: t("plugins.uninstall"), cancelButtonText: t("common.cancel") }
    );
  } catch {
    return;
  }
  busyId.value = plugin.id;
  try {
    await uninstallPlugin(plugin.id, purgeData.value);
    ElMessage.success(t("plugins.uninstallSuccess"));
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    busyId.value = "";
  }
}

async function clearData(plugin: Pick<PluginMeta, "id" | "name">) {
  if (busyId.value || officialBusy.value) return;
  try {
    await ElMessageBox.confirm(
      t("plugins.clearDataConfirm", { name: plugin.name }),
      t("plugins.clearData"),
      { type: "warning", confirmButtonText: t("plugins.clearData"), cancelButtonText: t("common.cancel") }
    );
  } catch {
    return;
  }
  busyId.value = plugin.id;
  try {
    await clearPluginData(plugin.id);
    ElMessage.success(t("plugins.clearDataSuccess"));
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    busyId.value = "";
  }
}

// One menu per page for the scope picker and the data actions: an ElSelect and an ElDropdown on
// every card made a page of 24 cards slow to show and to page through.
const cardMenu = ref<{ kind: "scope" | "data"; pluginId: string; x: number; y: number }>();
const cardMenuPlugin = computed(() => (cardMenu.value ? plugins.value.find((item) => item.id === cardMenu.value!.pluginId) : undefined));

function openCardMenu(event: MouseEvent, kind: "scope" | "data", pluginId: string) {
  const anchor = (event.currentTarget as HTMLElement | null)?.getBoundingClientRect();
  const width = 180;
  const x = anchor ? anchor.left : event.clientX;
  const y = anchor ? anchor.bottom + 4 : event.clientY;
  cardMenu.value = { kind, pluginId, x: Math.max(8, Math.min(x, window.innerWidth - width - 8)), y: Math.max(8, y) };
}

/** Runs a menu entry for the menu's plugin after closing the menu. */
function runCardMenu(action: (plugin: PluginMeta) => void) {
  const plugin = cardMenuPlugin.value;
  cardMenu.value = undefined;
  if (plugin) action(plugin);
}

function handleDataCommand(plugin: PluginMeta, command: string) {
  if (command === "export") void exportData(plugin);
  else if (command === "import") void importData(plugin);
  else void clearData(plugin);
}

// 数据归档由后端直接读写所选文件，插件文件（可能很大）不经过前端。
async function exportData(plugin: PluginMeta) {
  if (busyId.value || officialBusy.value) return;
  if (!isDesktopRuntime()) {
    ElMessage.info(t("plugins.dataDesktopOnly"));
    return;
  }
  const date = new Date().toISOString().slice(0, 10);
  const path = await chooseNativeSaveFile(t("plugins.exportData"), `${plugin.id}-data-${date}.zip`, [
    { DisplayName: t("plugins.dataArchive"), Pattern: "*.zip" },
  ]);
  if (!path) return;
  busyId.value = plugin.id;
  try {
    const manifest = await exportPluginDataArchive(plugin.id, path);
    ElMessage.success(t("plugins.exportDataSuccess", { keys: manifest.keys, files: manifest.files }));
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    busyId.value = "";
  }
}

async function importData(plugin: PluginMeta) {
  if (busyId.value || officialBusy.value) return;
  if (!isDesktopRuntime()) {
    ElMessage.info(t("plugins.dataDesktopOnly"));
    return;
  }
  const path = await chooseNativeFile(t("plugins.importData"), [
    { DisplayName: t("plugins.dataArchive"), Pattern: "*.zip" },
  ]);
  if (!path) return;
  try {
    const manifest = await inspectPluginDataArchive(path);
    if (manifest.pluginId !== plugin.id) {
      ElMessage.error(t("plugins.importDataWrongPlugin", { name: manifest.pluginName || manifest.pluginId }));
      return;
    }
    await ElMessageBox.confirm(
      t("plugins.importDataConfirm", {
        name: plugin.name,
        keys: manifest.keys,
        files: manifest.files,
        date: manifest.exportedAt.slice(0, 10),
        version: manifest.pluginVersion || "-",
      }),
      t("plugins.importData"),
      { type: "warning", confirmButtonText: t("plugins.importData"), cancelButtonText: t("common.cancel") }
    );
  } catch (error) {
    if (error !== "cancel" && error !== "close") ElMessage.error(String(error));
    return;
  }
  busyId.value = plugin.id;
  try {
    const imported = await importPluginDataArchive(plugin.id, path);
    ElMessage.success(t("plugins.importDataSuccess", { keys: imported.keys, files: imported.files }));
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    busyId.value = "";
  }
}

async function showLogs(plugin: PluginMeta) {
  logsTitle.value = `${plugin.name} · ${t("plugins.logs")}`;
  logsVisible.value = true;
  logsLoading.value = true;
  logs.value = [];
  const generation = ++logsGeneration;
  try {
    const result = await getPluginLogs(plugin.id);
    if (!disposed && generation === logsGeneration) logs.value = result;
  } catch (error) {
    if (!disposed && generation === logsGeneration) ElMessage.error(String(error));
  } finally {
    if (!disposed && generation === logsGeneration) logsLoading.value = false;
  }
}

function selectSection(value: string | number) {
  const next = sectionFromQuery(value);
  activeSection.value = next;
  void router.replace({
    name: "plugins",
    query: next === "plugins" ? {} : { tab: next },
  });
}

watch(
  () => route.query.tab,
  (tab) => {
    activeSection.value = sectionFromQuery(tab);
  }
);

// Deep links such as the keymap settings "get more keymaps" entry pre-fill the search.
watch(
  () => route.query.search,
  (search) => {
    const next = searchFromQuery(search);
    if (next) filters.value = { ...filters.value, search: next };
  }
);

watch(logsVisible, visible => { if (!visible) logsGeneration++; });
let stopWorkspaceWatch: (() => void) | undefined;
let stopFullTrustWatch: (() => void) | undefined;
// Bumped on every full-trust change so the card switches and the toolbar list reload.
const fullTrustToken = ref(0);
onBeforeUnmount(() => {
  stopWorkspaceWatch?.();
  stopFullTrustWatch?.();
  disposed = true; refreshGeneration++; logsGeneration++; invalidatePreview();
});
onMounted(() => {
  void refresh();
  if (isDesktopRuntime()) {
    stopWorkspaceWatch = listenAppEvent("plugin:workspace-changed", () => { void refresh(); });
    // Grants, revocations and lapses (upgrade, disable, package change) update the badges and switches.
    void import("@/services/plugin-full-trust").then(({ listenFullTrustChanged }) => {
      if (disposed) return;
      stopFullTrustWatch = listenFullTrustChanged(() => {
        fullTrustToken.value++;
        void refresh();
      });
    });
  }
});
</script>

<template>
  <section class="plugin-manager-workspace">
    <div class="plugin-manager-sections">
      <div v-if="activeSection === 'plugins'" class="plugin-manager-tabs__refresh">
        <PluginFullTrustSummary v-if="plugins.some((p) => p.fullTrust)" :refresh-token="fullTrustToken" @changed="refresh" />
        <el-button
          text
          :icon="Refresh"
          :loading="loading"
          @click="refresh"
        >
          {{ t("common.refresh") }}
        </el-button>
      </div>

      <el-tabs
        v-model="activeSection"
        class="plugin-manager-tabs"
        @tab-change="selectSection"
      >
        <el-tab-pane name="plugins">
          <template #label>
            <span class="plugin-manager-tab-label">
              <span>{{ t("plugins.tab.plugins") }}</span>
              <span class="plugin-manager-tab-label__count">{{ t("plugins.count", { count: allEntries.length }) }}</span>
            </span>
          </template>
        <div class="plugin-manager-plugins-pane">
      <div class="plugin-manager-catalog-header">
        <PluginCatalogToolbar
          v-model:filters="filters"
          v-model:source-path="sourcePath"
          :busy="installing || previewLoading || choosingSource"
          @choose-package="chooseSourceZip"
          @choose-directory="chooseSourceDirectory"
          @preview="install"
        />
      </div>

      <div class="plugin-manager-catalog-content">
        <div class="plugin-manager-catalog-alerts">
          <el-alert v-if="officialError" :title="officialError" type="warning" :closable="false" />
          <el-alert v-if="listError" :title="listError" type="warning" :closable="false" />
        </div>
        <div class="app-plugin-catalog" :aria-busy="loading">
          <el-scrollbar class="app-plugin-category-scroll">
            <el-menu class="app-plugin-categories" :default-active="filters.category" :aria-label="t('plugins.category')" @select="filters.category = $event">
              <el-menu-item v-for="category in (['all', ...visibleCategories] as const)" :key="category" :index="category">
                <span>{{ t(`plugins.category.${category}`) }}</span><span>{{ categoryCounts[category] }}</span>
              </el-menu-item>
            </el-menu>
          </el-scrollbar>
          <div class="plugin-manager-results">
            <el-scrollbar class="plugin-manager-scroll">
              <div class="plugin-manager-grid">
        <template v-for="entry in pageEntries" :key="entry.id">
        <el-card
          v-for="plugin in entry.plugin ? [entry.plugin] : []"
          :key="plugin.id"
          shadow="never"
          class="plugin-manager-card"
          body-class="plugin-manager-card__body"
        >
          <img
            v-if="plugin.cover && failedCovers[entry.id] !== plugin.cover"
            :src="plugin.cover"
            :alt="plugin.name"
            class="plugin-manager-cover"
            loading="lazy"
            @error="failedCovers[entry.id] = plugin.cover"
          />
          <div class="plugin-manager-card__scroll">
            <div class="plugin-manager-card__content">
            <div class="plugin-manager-meta">
              <div class="plugin-manager-card__heading">
                <div class="plugin-manager-meta__title">
                  <el-icon class="plugin-manager-icon" aria-hidden="true"><component :is="pluginViewIcon(plugin.icon || entry.official?.icon)" /></el-icon>
                  <strong :title="plugin.name">{{ plugin.name }}</strong>
                  <el-tag disable-transitions v-if="entry.official" size="small" effect="plain">{{ locale === 'zh' ? '官方' : 'Official' }}</el-tag>
                  <el-tag disable-transitions v-else size="small" effect="plain">{{ t(`plugins.source.${plugin.source}` as Parameters<typeof t>[0]) }}</el-tag>
                  <el-tag disable-transitions v-if="plugin.runtime === 'declarative' && plugin.enabled && plugin.state === 'stopped'" size="small" effect="plain" type="success" :title="t('plugins.declarativeActiveHelp')">{{ t('plugins.state.declarativeActive') }}</el-tag>
                  <el-tag disable-transitions v-else size="small" effect="plain" :type="stateType(plugin.state)" :title="plugin.enabled && plugin.state === 'stopped' ? t('plugins.dormantHelp') : undefined">{{ plugin.enabled && plugin.state === 'stopped' ? t('plugins.state.dormant') : stateLabel(plugin.state) }}</el-tag>
                  <el-tag disable-transitions v-if="plugin.enabled && !plugin.activeInWorkspace" size="small" type="warning" effect="plain">{{ t('plugins.inactiveInWorkspace') }}</el-tag>
                  <el-tag disable-transitions v-if="plugin.fullTrust" type="danger" effect="plain" size="small" :title="t('plugins.fullTrust.badgeHelp')">{{ t('plugins.fullTrust.badge') }}</el-tag>
                </div>
              </div>
              <div class="plugin-manager-meta__identity">
                <code>{{ plugin.id }}</code>
                <small>{{ t('plugins.installedVersion') }}: v{{ plugin.version }}</small>
                <small>{{ t('plugins.author') }}: {{ plugin.author || t('plugins.unknown') }}</small>
                <small :title="`${plugin.runtime} · API v${plugin.apiVersion}`">API v{{ plugin.apiVersion }}</small>
              </div>
              <p :title="plugin.description || plugin.id">{{ plugin.description || plugin.id }}</p>
              <div v-if="plugin.capabilities.length" class="plugin-manager-capabilities">
                <el-tag disable-transitions
                  v-for="capability in plugin.capabilities"
                  :key="capability"
                  size="small"
                  effect="plain"
                  :data-tone="pluginCapabilityTone(capability)"
                >
                  {{ capability }}
                </el-tag>
              </div>
            </div>



            <el-alert
              v-if="plugin.lastError"
              class="plugin-manager-alert"
              type="error"
              :title="plugin.lastError"
              :closable="false"
              show-icon
            />

            </div>
          </div>

          <div class="plugin-manager-actions">
            <div class="plugin-manager-primary-actions">
              <el-button
                v-for="view in viewsByPlugin.get(plugin.id)"
                :key="view.id"
                type="primary"
                plain
                size="small"
                :icon="TopRight"
                :title="localizedViewTitle(view)"
                :disabled="!plugin.enabled || !plugin.activeInWorkspace || !['running', 'stopped'].includes(plugin.state)"
                @click="openPluginView(view)"
              >
                {{ (viewsByPlugin.get(plugin.id)?.length ?? 0) > 1 ? localizedViewTitle(view) : t('plugins.openView') }}
              </el-button>
              <el-switch
                size="small"
                v-if="viewsByPlugin.get(plugin.id)?.length"
                :model-value="menuPlugins.includes(plugin.id)"
                :disabled="menuBusy"
                :active-text="locale === 'zh' ? '菜单' : 'Menu'"
                :aria-label="locale === 'zh' ? '加入左侧菜单' : 'Show in sidebar'"
                @change="toggleMenu(plugin.id, Boolean($event))"
              />
              <el-switch
                size="small"
                :model-value="plugin.enabled"
                :disabled="!!busyId || !!officialBusy || loading || (!plugin.enabled && !pluginCanStart(plugin))"
                :loading="busyId === plugin.id"
                :active-text="t('plugins.enabled')"
                :aria-label="plugin.enabled ? t('plugins.enabled') : t('plugins.disabled')"
                @change="toggle(plugin, Boolean($event))"
              />
              <el-button
                v-if="plugin.runtime !== 'declarative'"
                size="small"
                class="plugin-manager-scope-select"
                :disabled="!!busyId || !!officialBusy || loading"
                :aria-label="t('plugins.scope')"
                aria-haspopup="menu"
                @click="openCardMenu($event, 'scope', plugin.id)"
              >
                {{ plugin.scope === 'workspace' ? t('plugins.scopeWorkspace') : t('plugins.scopeGlobal') }}<el-icon class="el-icon--right"><ArrowDown /></el-icon>
              </el-button>
              <el-select
                v-if="plugin.runtime !== 'declarative' && plugin.scope === 'workspace'"
                size="small"
                multiple
                collapse-tags
                collapse-tags-tooltip
                class="plugin-manager-scope-workspaces"
                :model-value="plugin.workspaces"
                :disabled="!!busyId || !!officialBusy || loading"
                :placeholder="t('plugins.scopeWorkspacesPlaceholder')"
                :aria-label="t('plugins.scopeWorkspacesPlaceholder')"
                @change="changeScopeWorkspaces(plugin, $event)"
              >
                <el-option
                  v-for="workspace in workbench.workspaces"
                  :key="workspace.id"
                  :value="workspace.id"
                  :label="workspace.name"
                />
              </el-select>
            </div>
            <p v-if="scheduleLine(plugin.id)" class="plugin-manager-schedules">{{ t('pluginContrib.schedules.cardLine', { schedules: scheduleLine(plugin.id) }) }}</p>
            <div class="plugin-manager-secondary-actions">
              <el-button text @click="detailId = entry.id; detailsVisible = true">{{ t('plugins.details') }}</el-button>
              <el-button v-if="plugin.source === 'local'" text :icon="Upload" :title="t('plugins.upgradeFromPackage')" :aria-label="t('plugins.upgradeFromPackage')" :disabled="!!busyId || !!officialBusy || loading" @click="upgrade(plugin)">{{ t('plugins.upgrade') }}</el-button>
              <el-button
                v-if="configurablePluginIds.has(plugin.id)"
                text
                :icon="Setting"
                :aria-label="t('pluginContrib.configuration.openSettingsLabel', { name: plugin.name })"
                @click="openPluginSettings(plugin.id)"
              >
                {{ t("pluginContrib.configuration.openSettings") }}
              </el-button>
              <el-button text :icon="Tickets" @click="showLogs(plugin)">{{ t("plugins.logs") }}</el-button>
              <PluginAssociationSettings v-if="plugin.enabled && plugin.capabilities.includes('system.associations')" :plugin-id="plugin.id" />
              <PluginAiGrants v-if="plugin.capabilities.includes('ai')" :plugin-id="plugin.id" :plugin-name="plugin.name" />
              <PluginDatabaseDriverTrust v-if="plugin.capabilities.includes('database.driver')" :plugin-id="plugin.id" :plugin-name="plugin.name" />
              <PluginFullTrust v-if="plugin.source === 'local' && plugin.runtime !== 'declarative'" :key="`full-trust-${plugin.id}-${plugin.version}`" :plugin-id="plugin.id" :plugin-name="plugin.name" :plugin-version="plugin.version" :trusted="plugin.fullTrust === true" @changed="refresh" />
              <PluginCallGrants v-if="plugin.capabilities.includes('plugins')" :plugin-id="plugin.id" :plugin-names="pluginNamesById" />
              <PluginDatabaseSchemaGrants v-if="plugin.capabilities.includes('database.schema')" :plugin-id="plugin.id" />
              <el-button
                v-if="plugin.enabled && plugin.runtime !== 'declarative' && pluginCanStart(plugin)"
                text
                :icon="Refresh"
                :loading="busyId === plugin.id"
                @click="retry(plugin)"
              >
                {{ t("plugins.reload") }}
              </el-button>
              <el-button
                text
                class="plugin-manager-data-button"
                aria-haspopup="menu"
                :disabled="!!busyId || !!officialBusy || loading"
                :loading="busyId === plugin.id"
                @click="openCardMenu($event, 'data', plugin.id)"
              >
                {{ t("plugins.data") }}<el-icon class="el-icon--right"><ArrowDown /></el-icon>
              </el-button>
              <el-button
                v-if="plugin.source === 'local'"
                text
                type="danger"
                :icon="Delete"
                :loading="busyId === plugin.id"
                @click="uninstall(plugin)"
              >
                {{ t("plugins.uninstall") }}
              </el-button>
            </div>
          </div>
        </el-card>
        </template>

        <section v-if="!loading && !catalogEntries.length" class="plugin-manager-runtime-empty">
          <el-empty :image-size="92" :description="t('plugins.empty')" />
        </section>
              </div>
            </el-scrollbar>
            <div v-if="catalogEntries.length > pageSize" class="app-plugin-pagination">
              <el-button text size="small" :icon="DArrowLeft" :disabled="page <= 1" :aria-label="t('plugins.pagination.first')" :title="t('plugins.pagination.first')" @click="page = 1" />
              <el-pagination v-model:current-page="page" :page-size="pageSize" :total="catalogEntries.length" layout="total, prev, pager, next" />
              <el-button text size="small" :icon="DArrowRight" :disabled="page >= pageCount" :aria-label="t('plugins.pagination.last')" :title="t('plugins.pagination.last')" @click="page = pageCount" />
            </div>
          </div>
        </div>
      </div>
        </div>
      </el-tab-pane>
        <el-tab-pane :label="t('plugins.tab.development')" name="development" lazy>
          <PluginDevelopmentPanel />
        </el-tab-pane>
        <el-tab-pane :label="t('plugins.tab.services')" name="services" lazy>
          <PluginServicesCatalog />
        </el-tab-pane>
        <el-tab-pane :label="t('plugins.tab.capabilities')" name="capabilities" lazy>
          <PluginCapabilityCatalog />
        </el-tab-pane>
      </el-tabs>
    </div>

    <el-drawer v-model="detailsVisible" class="app-plugin-detail-drawer" :title="t('plugins.details')" size="min(560px, 90vw)" append-to-body destroy-on-close>
      <template v-if="detail?.plugin || detail?.official">
        <PluginMetadata :metadata="(detail.plugin ?? detail.official)!" :source="detail.official ? 'official' : 'local'" />
        <PluginProvidedServices v-if="detail.plugin" :plugin-id="detail.plugin.id" />
        <PluginWalkthroughList v-if="detail.plugin" :plugin-id="detail.plugin.id" />
      </template>
      <el-empty v-else :description="t('plugins.viewPluginMissing')" />
      <el-alert v-if="detail?.plugin?.lastError" :title="detail.plugin.lastError" type="error" :closable="false" />
      <template #footer>
        <el-button @click="detailsVisible = false">{{ t('common.close') }}</el-button>
      </template>
    </el-drawer>
    <el-dialog v-model="previewVisible" class="app-plugin-preview-dialog" :title="t('plugins.preview')" width="min(680px, 92vw)" :close-on-click-modal="false" :close-on-press-escape="!installing" :show-close="!installing" append-to-body destroy-on-close>
      <el-scrollbar max-height="64vh">
        <el-skeleton v-if="previewLoading" :rows="5" animated />
        <el-alert v-if="previewError" :title="previewErrorTitle" type="error" show-icon :closable="false">
          <p v-if="previewError.kind === 'unknown'">{{ previewError.detail }}</p>
          <template v-else>
            <p>{{ t(`plugins.installError.${previewError.kind}.hint`, { version: previewError.version ?? '', current: previewError.current ?? '' }) }}</p>
            <p class="app-plugin-preview__error-detail">{{ t('plugins.installError.detail', { detail: previewError.detail }) }}</p>
          </template>
        </el-alert>
        <div v-if="preview" class="app-plugin-preview">
          <section class="app-plugin-preview__summary">
            <span class="app-plugin-preview__mark" aria-hidden="true">{{ Array.from(preview.manifest.name.trim())[0]?.toUpperCase() ?? "?" }}</span>
            <div class="app-plugin-preview__heading">
              <div class="app-plugin-preview__title">
                <strong>{{ preview.manifest.name }}</strong>
                <el-tag size="small" effect="plain" round>v{{ preview.manifest.version }}</el-tag>
                <el-tag size="small" type="info" effect="light" round>{{ t('plugins.catalogSource.local') }}</el-tag>
              </div>
              <code>{{ preview.manifest.id }}</code>
            </div>
            <div class="app-plugin-preview__stats">
              <span><strong>{{ preview.fileCount }}</strong>{{ t('plugins.previewFiles') }}</span>
              <span><strong>{{ formatBytes(preview.totalBytes) }}</strong>{{ t('plugins.previewExpandedSize') }}</span>
            </div>
          </section>
          <el-alert v-if="preview.installed" :title="t('plugins.alreadyInstalled')" type="warning" show-icon :closable="false" />


          <div class="app-plugin-preview__source" :title="t('plugins.previewSource', { path: preview.sourcePath })">
            <span>{{ t('plugins.previewSourceLabel') }}</span>
            <code>{{ `\u200e${preview.sourcePath}\u200e` }}</code>
          </div>

          <PluginMetadata :metadata="preview.manifest" source="local" />

          <section class="app-plugin-preview__notes" :aria-label="t('plugins.previewNotes')">
            <strong>{{ t('plugins.previewNotes') }}</strong>
            <p><el-icon><InfoFilled /></el-icon><span>{{ t('plugins.previewNotice') }}</span></p>
            <p v-if="previewDynamicStatusBarIds.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.statusBar.previewDynamic', { count: previewDynamicStatusBarIds.length, ids: previewDynamicStatusBarIds.join(', ') }) }}</span></p>
            <p v-if="previewLanguages.languages.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.languages.previewLanguages', { count: previewLanguages.languages.length, languages: previewLanguages.languages.join('; ') }) }}</span></p>
            <p v-if="previewLanguages.associations.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.languages.previewAssociations', { associations: previewLanguages.associations.join('; ') }) }}</span></p>
            <p v-if="previewAiPrompts"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.aiPrompts.previewSummary', { prompts: previewAiPrompts.prompts, skills: previewAiPrompts.skills }) }}</span></p>
            <p v-if="previewLocalizations.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.localizations.previewSummary', { count: previewLocalizations.length, languages: previewLocalizations.join('; ') }) }}</span></p>
            <p v-for="editor in previewCustomEditors" :key="`custom-editor-${editor.title}-${editor.patterns.join(',')}`"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.customEditors.previewTitle') }} · {{ editor.title }} — {{ t('pluginContrib.customEditors.previewPatterns', { patterns: editor.patterns.join(', ') }) }}; {{ editor.priority === 'default' ? t('pluginContrib.customEditors.previewDefault') : t('pluginContrib.customEditors.previewOption') }}; {{ editor.access === 'readWrite' ? t('pluginContrib.customEditors.previewWrites') : t('pluginContrib.customEditors.previewReadOnly') }}</span></p>
            <p v-for="profile in previewTerminalProfiles" :key="`terminal-profile-${profile.title}-${profile.command}`" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.terminalProfiles.previewTitle') }} · {{ profile.title }} — <code>{{ formatTerminalArgv([profile.command, ...profile.args]) }}</code>{{ profile.envNames.length ? `; ${t('pluginContrib.terminalProfiles.previewEnv', { names: profile.envNames.join(', ') })}` : '' }}; {{ t('pluginContrib.terminalProfiles.previewConfirm') }}</span></p>
            <p v-if="previewTasks.tasks.length || previewTasks.providers.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span><template v-if="previewTasks.tasks.length">{{ t('pluginContrib.tasks.previewSummary', { count: previewTasks.tasks.length, tasks: previewTasks.tasks.map((task) => `${task.title} (${formatTerminalArgv([task.command, ...task.args])})`).join('; ') }) }}</template> <template v-if="previewTasks.providers.length">{{ t('pluginContrib.tasks.previewProviders', { providers: previewTasks.providers.join('; ') }) }}</template></span></p>
            <p v-if="previewAiProviders"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.aiProviders.previewSummary', { count: previewAiProviders }) }}</span></p>
            <p v-if="previewTerminalColorSchemes.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.terminalColorSchemes.previewSummary', { count: previewTerminalColorSchemes.length, titles: previewTerminalColorSchemes.join('; ') }) }}</span></p>
            <p v-if="previewEditorThemes.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.editorThemes.previewSummary', { count: previewEditorThemes.length, titles: previewEditorThemes.join('; ') }) }}</span></p>
            <p v-if="previewFileIcons"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.fileIcons.previewSummary', { count: previewFileIcons.count, titles: previewFileIcons.titles.join('; ') }) }}</span></p>
            <p v-if="previewJsonValidation"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.jsonValidation.previewSummary', { count: previewJsonValidation.count, patterns: previewJsonValidation.patterns.join(', ') }) }}</span></p>
            <p v-if="previewSchedules.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.schedules.previewSummary', { count: previewSchedules.length, schedules: scheduleList(previewSchedules) }) }}</span></p>
            <p v-if="previewGlobalShortcuts.length" data-testid="plugin-preview-global-shortcuts"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.globalShortcuts.previewSummary', { count: previewGlobalShortcuts.length, shortcuts: previewGlobalShortcutList }) }}</span></p>
            <p v-if="previewTrayTitles.length" data-testid="plugin-preview-tray"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.menus.previewTray', { count: previewTrayTitles.length, titles: previewTrayTitles.join('; ') }) }}</span></p>
            <p v-if="previewSettingsPageTitle" data-testid="plugin-preview-settings-page"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.settingsPages.previewSummary', { title: previewSettingsPageTitle }) }}</span></p>
            <p v-for="source in previewSessionSources" :key="`session-source-${source.id}`" data-testid="plugin-preview-session-source"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.sessionSources.previewSummary', { title: source.title }) }}</span></p>
            <p v-for="provider in previewScmProviders" :key="`scm-provider-${provider.id}`" class="is-warning" data-testid="plugin-preview-scm-provider"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.scmCommitMessage.previewSummary', { title: provider.title }) }}</span></p>
            <p v-for="provider in previewDecorationProviders" :key="`decoration-provider-${provider.id}`" data-testid="plugin-preview-decoration-provider"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.fileDecorations.previewSummary', { title: provider.title }) }}</span></p>
            <p v-if="previewGlobalShortcutsChanged" class="is-warning" data-testid="plugin-preview-global-shortcuts-changed"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.globalShortcuts.previewChangedByUpdate', { shortcuts: previewGlobalShortcutsChanged }) }}</span></p>
            <p v-if="previewFormatters.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.formatters.previewSummary', { count: previewFormatters.length, formatters: previewFormatters.map((item) => `${item.title} (${item.languages.join(', ')})`).join('; ') }) }} {{ t('pluginContrib.formatters.previewSendsText') }}</span></p>
            <p v-if="previewLinters.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.linters.previewSummary', { count: previewLinters.length, linters: previewLinters.map((item) => `${item.title} (${item.languages.join(', ')})`).join('; ') }) }} {{ t('pluginContrib.linters.previewSendsText') }}{{ previewLinters.some((item) => item.onType) ? ` ${t('pluginContrib.linters.previewOnType', { linters: previewLinters.filter((item) => item.onType).map((item) => item.title).join(', ') })}` : '' }}</span></p>
            <p v-if="previewAiContextProviders.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.aiContextProviders.previewSummary', { count: previewAiContextProviders.length, providers: previewAiContextProviders.map((item) => `@${item.mention} (${item.title})`).join('; ') }) }} {{ t('pluginContrib.aiContextProviders.previewSendsArgument') }}</span></p>
            <p v-if="previewHttpCollections"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.httpCollections.previewSummary', { count: previewHttpCollections.count, titles: previewHttpCollections.titles.join('; ') }) }}</span></p>
            <p v-if="previewFileTemplates.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.fileTemplates.previewSummary', { count: previewFileTemplates.length, titles: previewFileTemplates.map((item) => `${item.title} (${item.files})`).join('; ') }) }}</span></p>
            <p v-if="previewHttpCodeGenerators.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.httpCodeGenerators.previewSummary', { count: previewHttpCodeGenerators.length, titles: previewHttpCodeGenerators.join('; ') }) }}</span></p>
            <p v-if="previewLanguageServers.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.languageServers.previewSummary', { count: previewLanguageServers.length, servers: previewLanguageServers.join('; ') }) }}</span></p>
            <p v-if="previewMcpServers.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.mcpServers.previewSummary', { count: previewMcpServers.length, servers: previewMcpServers.join('; ') }) }}</span></p>
            <p v-if="previewTreeViews.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.treeViews.previewSummary', { count: previewTreeViews.length, views: previewTreeViews.join('; ') }) }}</span></p>
            <p v-if="previewTestProviders.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.testProviders.previewSummary', { count: previewTestProviders.length, providers: previewTestProviders.join('; ') }) }}</span></p>
            <p v-if="previewLanguageFeatures.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.languageFeatures.previewSummary', { count: previewLanguageFeatures.length, providers: previewLanguageFeatures.join('; ') }) }}</span></p>
            <p v-for="library in previewCommandSnippets" :key="`command-snippets-${library.library}`"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.commandSnippets.previewSummary', { title: library.title, count: library.path ? '?' : library.commands, targets: library.targets.join(', ') }) }}</span></p>
            <p v-for="link in previewTerminalLinks" :key="`terminal-link-${link.id}`"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.terminalLinks.previewSummary', { pattern: link.pattern, target: link.target, terminals: link.terminals.join(', ') }) }}</span></p>
            <p v-if="previewWalkthroughs"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.walkthroughs.previewSummary', { count: previewWalkthroughs.count, titles: previewWalkthroughs.titles.join('; ') }) }}</span></p>
            <p v-if="previewWalkthroughs?.commandSteps"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.walkthroughs.previewCommandSteps', { count: previewWalkthroughs.commandSteps }) }}</span></p>
            <p v-if="previewDashboardWidgets"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.dashboardWidgets.previewSummary', { count: previewDashboardWidgets.count }) }}</span></p>
            <p v-if="previewDashboardWidgets?.actions.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.dashboardWidgets.previewRunsWhenVisible', { actions: previewDashboardWidgets.actions.join(', ') }) }}</span></p>
            <template v-for="(driver, index) in previewDatabaseDrivers" :key="`database-driver-${index}`">
              <p><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.databaseDrivers.previewSummary', { title: driver.title, dialect: driver.dialect }) }}<template v-if="driver.replaces">{{ t('pluginContrib.databaseDrivers.previewReplaces', { type: driver.replaces }) }}</template></span></p>
              <p v-if="driver.secretFields.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.databaseDrivers.previewSecrets', { title: driver.title, fields: driver.secretFields.join(', ') }) }}</span></p>
              <p v-if="driver.writes || driver.destructive" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t(driver.destructive ? 'pluginContrib.databaseDrivers.previewDestructive' : 'pluginContrib.databaseDrivers.previewWrites', { title: driver.title }) }}</span></p>
            </template>
            <p v-if="preview.manifest.capabilities?.includes('database.driver')" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.capabilityDatabaseDriver') }}</span></p>
            <p v-if="preview.manifest.capabilities?.includes('database.schema')" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.capabilityDatabaseSchema') }}</span></p>
            <p v-if="preview.manifest.capabilities?.includes('remote.context')" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.capabilityRemoteContext') }}</span></p>
            <p v-if="preview.manifest.capabilities?.includes('ui.wasm')" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.capabilityUiWasm') }}</span></p>
            <p v-if="preview.manifest.capabilities?.includes('ai.config')" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.capabilityAiConfig') }}</span></p>
            <p v-if="previewSearchProviders.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.searchProviders.previewSummary', { count: previewSearchProviders.length, prefixes: previewSearchProviders.map((item) => `${item.prefix}: (${item.title})`).join('; ') }) }} {{ t('pluginContrib.searchProviders.previewSendsQuery') }}</span></p>
            <p v-if="previewServiceSurfaces.length" class="is-warning" data-testid="plugin-preview-service-surfaces"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.services.previewSurfaces', { surfaces: previewServiceSurfaces.map((surface) => t(`plugins.services.surface.${surface}`)).join('; ') }) }}</span></p>
            <p v-if="previewAiCatalogs.length" class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('pluginContrib.aiCatalogs.previewSummary', { count: previewAiCatalogs.length, catalogs: previewAiCatalogs.map((item) => `${item.title} (${t(item.kind === 'mcp' ? 'pluginContrib.aiCatalogs.kindMcp' : 'pluginContrib.aiCatalogs.kindSkill')})`).join('; ') }) }} {{ t('pluginContrib.aiCatalogs.previewSendsQuery') }}</span></p>
            <p v-if="previewUriHandlers.length"><el-icon><InfoFilled /></el-icon><span>{{ t('pluginContrib.uriHandlers.previewSummary', { count: previewUriHandlers.length, paths: previewUriHandlers.map((item) => `mincode://${preview?.manifest.id ?? ''}${item.path} → ${item.target}`).join('; ') }) }}</span></p>
            <p v-if="preview.manifest.runtime?.type === 'declarative'"><el-icon><InfoFilled /></el-icon><span>{{ t('plugins.declarativePreviewNotice') }}</span></p>
            <p v-else class="is-warning"><el-icon><WarningFilled /></el-icon><span>{{ t('plugins.localTrustWarning') }}</span></p>
          </section>
          <p v-if="previewHasNonReadActions" class="app-plugin-preview__trust-hint"><el-icon><InfoFilled /></el-icon><span>{{ t('plugins.fullTrust.previewHint') }}</span></p>
        </div>
      </el-scrollbar>
      <template #footer>
        <el-button :disabled="installing" @click="previewVisible = false">{{ previewError ? t('common.close') : t('common.cancel') }}</el-button>
        <el-button v-if="!previewError" type="primary" :loading="installing" :disabled="!canConfirmInstall" @click="confirmInstall">{{ t('plugins.confirmInstall') }}</el-button>
      </template>
    </el-dialog>
    <el-dialog v-model="logsVisible" :title="logsTitle" width="min(720px, 88vw)" destroy-on-close append-to-body>
      <div v-loading="logsLoading" class="plugin-manager-logs">
        <el-empty v-if="!logsLoading && !logs.length" :image-size="72" :description="t('plugins.logsEmpty')" />
        <el-table v-else :data="logs" size="small" max-height="420">
          <el-table-column prop="time" :label="t('plugins.logTime')" width="190" />
          <el-table-column prop="level" :label="t('plugins.logLevel')" width="90" />
          <el-table-column prop="message" :label="t('plugins.logMessage')" min-width="320" show-overflow-tooltip />
        </el-table>
      </div>
    </el-dialog>
    <AppContextMenu
      v-if="cardMenu && cardMenuPlugin"
      :open="true"
      :x="cardMenu.x"
      :y="cardMenu.y"
      :width="180"
      :aria-label="cardMenu.kind === 'scope' ? t('plugins.scope') : t('plugins.data')"
      @close="cardMenu = undefined"
    >
      <template v-if="cardMenu.kind === 'scope'">
        <el-dropdown-item
          v-for="option in (['global', 'workspace'] as const)"
          :key="option"
          :aria-checked="cardMenuPlugin.scope === option"
          role="menuitemradio"
          @click="runCardMenu((plugin) => changeScope(plugin, option))"
        >
          <el-icon :style="{ visibility: cardMenuPlugin.scope === option ? 'visible' : 'hidden' }"><Check /></el-icon>{{ option === 'workspace' ? t('plugins.scopeWorkspace') : t('plugins.scopeGlobal') }}
        </el-dropdown-item>
      </template>
      <template v-else>
        <el-dropdown-item :icon="Download" @click="runCardMenu((plugin) => handleDataCommand(plugin, 'export'))">{{ t("plugins.exportData") }}</el-dropdown-item>
        <el-dropdown-item :icon="Upload" @click="runCardMenu((plugin) => handleDataCommand(plugin, 'import'))">{{ t("plugins.importData") }}</el-dropdown-item>
        <el-dropdown-item :icon="Delete" divided class="plugin-manager-danger-item" @click="runCardMenu((plugin) => handleDataCommand(plugin, 'clear'))">
          {{ t("plugins.clearData") }}
        </el-dropdown-item>
      </template>
    </AppContextMenu>
  </section>
</template>
