<script setup lang="ts">
/**
 * Settings > Plugins: renders each installed plugin's `contributes.configuration`
 * schema with Element Plus controls. Values are validated again by the Go host
 * on every write. Secret settings are write-only here: the page shows whether a
 * secret is set and can set, replace or clear it, but never reads it back.
 * Plugins with a `contributes.settingsPages` page are listed too; their page
 * (the plugin's own sandboxed view) loads only while it is shown.
 */
import "@/styles/workspaces/settings/plugin-settings.css";
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { useWorkbenchStore } from "@/stores/workbench";
import { isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { listPlugins } from "@/services/plugin-service";
import {
  getPluginConfiguration,
  listConfigurablePlugins,
  listenPluginConfigurationChanged,
  listenPluginSettingsInvalidated,
  resetPluginConfiguration,
  setPluginConfiguration,
  setPluginConfigurationSecret,
} from "@/services/plugin-configuration-service";
import {
  configurationFields,
  configurationFieldValue,
  isValidConfigurationValue,
  type PluginConfigurationField,
  type PluginConfigurationIgnoredValue,
  type PluginConfigurationScope,
  type PluginConfigurationState,
  type PluginConfigurationValue,
} from "@/domain/plugins/configuration";
import {
  createDebouncedTrigger,
  mergeConfigurationDrafts,
  reconcileConfigurationSelection,
} from "@/domain/plugins/configuration-refresh";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { pluginSettingsPages } from "@/services/contributions/settings-pages-runtime";

// The page section (with the plugin view frame) loads only when a plugin with a settings page is shown.
const PluginSettingsPageSection = defineAsyncComponent(() => import("@/components/settings/PluginSettingsPageSection.vue"));

/** `active`: false while the Settings dialog is closed (it stays mounted); a shown page is destroyed then. */
const props = withDefaults(defineProps<{ initialPluginId?: string; active?: boolean }>(), { active: true });
/** A settings page opened itself as a tab: Settings closes so the tab is not hidden behind it. */
const emit = defineEmits<{ navigate: [] }>();
const { t, localeChain } = useVueI18n();
const workbench = useWorkbenchStore();
const desktop = isDesktopRuntime();

interface PluginChoice {
  id: string;
  name: string;
  enabled: boolean;
  development: boolean;
  /** Declares `contributes.configuration` (the host form); false for a page-only plugin. */
  configurable: boolean;
}
type PluginMeta = Awaited<ReturnType<typeof listPlugins>>[number];
type SettingsMode = "options" | "page";

interface LoadStateOptions {
  /** Keep unsaved edits of the same plugin and workspace (background refresh). */
  preserveDrafts?: boolean;
  /** Do not show the loading mask (background refresh). */
  silent?: boolean;
}

// Plugins declaring `contributes.configuration` and the installed plugin metadata (last good read).
const configurableIds = ref<string[]>([]);
const installed = shallowRef(new Map<string, PluginMeta>());
// Picker = configurable plugins ∪ plugins with a settings page (enabled plugins only, from the snapshot).
const plugins = computed<PluginChoice[]>(() => {
  const choices = configurableIds.value.map((id) => {
    const meta = installed.value.get(id);
    return { id, name: meta?.name ?? id, enabled: meta?.enabled === true, development: meta?.source === "development", configurable: true };
  });
  const listed = new Set(configurableIds.value);
  for (const page of pluginSettingsPages.value) {
    if (listed.has(page.pluginId)) continue;
    listed.add(page.pluginId);
    const development = installed.value.get(page.pluginId)?.source === "development";
    choices.push({ id: page.pluginId, name: page.pluginName || page.pluginId, enabled: true, development, configurable: false });
  }
  return choices;
});
let listLoaded = false;
// The last shown choice, so a plugin that vanishes keeps its name in the "unavailable" notice.
let lastSelected: PluginChoice | undefined;
const listLoading = ref(false);
const listError = ref("");
const selectedId = ref("");
// The selected plugin vanished (uninstalled, schema removed, development session
// ended). It stays selected so the page never silently switches to another plugin.
const missingPlugin = ref<PluginChoice>();
const state = ref<PluginConfigurationState>();
const stateLoading = ref(false);
const stateError = ref("");
const saving = ref(false);
// Local edit buffers so text/number inputs save on commit (blur/Enter), not per keystroke.
const globalDraft = reactive<Record<string, PluginConfigurationValue>>({});
const workspaceDraft = reactive<Record<string, PluginConfigurationValue>>({});
const secretDraft = reactive<Record<string, string>>({});
const fieldErrors = reactive<Record<string, string>>({});
// Values the draft buffers were last synced to; a differing draft is an unsaved edit.
let globalBaseline: Record<string, PluginConfigurationValue> = {};
let workspaceBaseline: Record<string, PluginConfigurationValue> = {};
let draftFields: PluginConfigurationField[] = [];
let stopListening: (() => void) | undefined;
let stopInvalidation: (() => void) | undefined;
let disposed = false;

const workspaceId = computed(() => workbench.activeWorkspaceId);
const workspaceName = computed(() => workbench.activeWorkspace?.name ?? workspaceId.value);
const listGate = createLatestRequestGate(() => 0);
const stateGate = createLatestRequestGate(() => `${selectedId.value}\n${workspaceId.value}`);
const refreshTrigger = createDebouncedTrigger(() => void refresh());
const selectedPlugin = computed(() => plugins.value.find((plugin) => plugin.id === selectedId.value));
// The selected plugin's own settings page, and whether the host form or the page is shown
// (remembered per plugin while this panel lives; the host form comes first).
const selectedPage = computed(() => (missingPlugin.value ? undefined : pluginSettingsPages.value.find((page) => page.pluginId === selectedId.value)));
const modeByPlugin = reactive<Record<string, SettingsMode>>({});
// After a mode switch, focus moves into what was switched to (the page header or the form), not <body>.
const focusPage = ref(false);
const formBody = ref<HTMLElement>();
const mode = computed<SettingsMode>({
  get: () => {
    if (!selectedPage.value) return "options";
    if (!selectedPlugin.value?.configurable) return "page";
    return modeByPlugin[selectedId.value] ?? "options";
  },
  set: (value) => {
    if (!selectedId.value) return;
    modeByPlugin[selectedId.value] = value;
    focusPage.value = value === "page";
    if (value === "options") void nextTick(() => formBody.value?.focus());
  },
});
const showModeSwitch = computed(() => Boolean(selectedPage.value && selectedPlugin.value?.configurable));
const modeOptions = computed(() => {
  const page = selectedPage.value;
  const pageTitle = page ? resolvePluginTitle({ title: page.title, titleLocalized: page.titleLocalized }, localeChain.value) || page.view : "";
  return [
    { label: t("pluginContrib.settingsPages.optionsTab"), value: "options" },
    { label: pageTitle, value: "page" },
  ];
});
const fields = computed(() => (state.value ? configurationFields(state.value.schema, localeChain.value) : []));
const schemaTitle = computed(() => {
  const schema = state.value?.schema;
  return schema ? resolvePluginTitle({ title: schema.title ?? "", titleLocalized: schema.titleLocalized }, localeChain.value) : "";
});

function clearRecord(target: Record<string, unknown>) {
  for (const key of Object.keys(target)) delete target[key];
}

function syncDrafts(next: PluginConfigurationState, preserve: boolean) {
  const nextFields = configurationFields(next.schema, localeChain.value);
  const nextGlobal: Record<string, PluginConfigurationValue> = {};
  const nextWorkspace: Record<string, PluginConfigurationValue> = {};
  for (const field of nextFields) {
    if (field.control === "secret") continue;
    const defaults = field.property.default === undefined ? {} : { [field.key]: field.property.default };
    nextGlobal[field.key] = configurationFieldValue(field, next.global, defaults);
    if (field.scope === "workspace") {
      const inherited = Object.prototype.hasOwnProperty.call(next.global, field.key) ? next.global : defaults;
      nextWorkspace[field.key] = configurationFieldValue(field, next.workspace, inherited);
    }
  }
  const global = preserve
    ? mergeConfigurationDrafts(globalBaseline, globalDraft, nextGlobal, draftFields, nextFields)
    : { values: nextGlobal, preserved: [] };
  const workspace = preserve
    ? mergeConfigurationDrafts(workspaceBaseline, workspaceDraft, nextWorkspace, draftFields, nextFields)
    : { values: nextWorkspace, preserved: [] };
  const keptErrors = new Set([...global.preserved.map((key) => `global:${key}`), ...workspace.preserved.map((key) => `workspace:${key}`)]);
  for (const key of Object.keys(fieldErrors)) {
    if (!keptErrors.has(key)) delete fieldErrors[key];
  }
  const secretKeys = new Set(nextFields.filter((field) => field.control === "secret").map((field) => field.key));
  for (const key of Object.keys(secretDraft)) {
    if (!preserve || !secretKeys.has(key)) delete secretDraft[key];
  }
  clearRecord(globalDraft);
  clearRecord(workspaceDraft);
  Object.assign(globalDraft, global.values);
  Object.assign(workspaceDraft, workspace.values);
  globalBaseline = nextGlobal;
  workspaceBaseline = nextWorkspace;
  draftFields = nextFields;
}

function resetStateView() {
  stateGate.invalidate();
  state.value = undefined;
  stateError.value = "";
  stateLoading.value = false;
}

/**
 * Keeps the selection on the merged picker list. A selected plugin that left
 * the list stays selected as "missing" so the page never silently switches.
 */
function reconcileSelection(previous: PluginChoice | undefined, preferred?: string) {
  const ids = plugins.value.map((plugin) => plugin.id);
  const next = reconcileConfigurationSelection(ids, selectedId.value, preferred);
  if (next.missing) {
    missingPlugin.value = previous?.id === next.selectedId
      ? { ...previous, configurable: false }
      : { id: next.selectedId, name: next.selectedId, enabled: false, development: false, configurable: false };
    resetStateView();
  } else {
    missingPlugin.value = undefined;
  }
  selectedId.value = next.selectedId;
  lastSelected = plugins.value.find((plugin) => plugin.id === next.selectedId) ?? missingPlugin.value;
}

/**
 * Re-reads the configurable plugins. Returns false when the response was
 * stale or failed. `preferred` is an explicit navigation target.
 */
async function loadPlugins(options: { preferred?: string; silent?: boolean } = {}): Promise<boolean> {
  if (!desktop) return false;
  const token = listGate.begin();
  if (!options.silent) listLoading.value = true;
  try {
    const [ids, list] = await Promise.all([listConfigurablePlugins(), listPlugins()]);
    if (disposed || !listGate.isCurrent(token)) return false;
    const previous = selectedPlugin.value ?? missingPlugin.value ?? lastSelected;
    installed.value = new Map(list.map((plugin) => [plugin.id, plugin]));
    configurableIds.value = ids;
    listError.value = "";
    listLoaded = true;
    reconcileSelection(previous, options.preferred);
    return true;
  } catch (error) {
    if (disposed || !listGate.isCurrent(token)) return false;
    listError.value = nativeErrorMessage(error);
    return false;
  } finally {
    if (listGate.isCurrent(token)) listLoading.value = false;
  }
}

async function loadState(options: LoadStateOptions = {}) {
  const id = selectedId.value;
  // A page-only plugin has no host form to load.
  if (!id || missingPlugin.value || !configurableIds.value.includes(id)) {
    resetStateView();
    return;
  }
  const token = stateGate.begin();
  if (!options.silent) stateLoading.value = true;
  stateError.value = "";
  try {
    const next = await getPluginConfiguration(id, workspaceId.value || undefined);
    if (disposed || !stateGate.isCurrent(token)) return;
    const current = state.value;
    const sameTarget = !!current && current.pluginId === next.pluginId && current.workspaceId === next.workspaceId;
    syncDrafts(next, options.preserveDrafts === true && sameTarget);
    state.value = next;
  } catch (error) {
    if (disposed || !stateGate.isCurrent(token)) return;
    state.value = undefined;
    stateError.value = nativeErrorMessage(error);
  } finally {
    if (stateGate.isCurrent(token)) stateLoading.value = false;
  }
}

/** Background refresh after a host event: list first, then the selected plugin's schema and values. */
async function refresh() {
  if (disposed) return;
  if (saving.value) {
    // A write is in flight and reloads the state itself; try again afterwards.
    refreshTrigger.schedule();
    return;
  }
  const before = selectedId.value;
  const wasMissing = !!missingPlugin.value;
  if (!(await loadPlugins({ silent: true }))) return;
  // A changed selection is loaded by the selectedId watcher; a missing one shows its own state.
  if (selectedId.value !== before || missingPlugin.value) return;
  await loadState({ preserveDrafts: !wasMissing, silent: !wasMissing });
}

async function run(action: () => Promise<unknown>, success?: string) {
  saving.value = true;
  try {
    await action();
    if (success) ElMessage.success(success);
  } catch (error) {
    ElMessage.error(t("pluginContrib.configuration.saveFailed", { message: nativeErrorMessage(error) }));
  } finally {
    saving.value = false;
    await loadState();
  }
}

function hasOwn(values: Record<string, unknown> | undefined, key: string) {
  return !!values && Object.prototype.hasOwnProperty.call(values, key);
}

function fieldErrorKey(field: PluginConfigurationField, scope: PluginConfigurationScope) {
  return `${scope}:${field.key}`;
}

function commit(field: PluginConfigurationField, scope: PluginConfigurationScope) {
  const current = state.value;
  if (!current) return;
  const draft = scope === "global" ? globalDraft : workspaceDraft;
  const value = draft[field.key];
  const errorKey = fieldErrorKey(field, scope);
  if (!isValidConfigurationValue(field.property, value)) {
    fieldErrors[errorKey] = t("pluginContrib.configuration.invalidValue");
    return;
  }
  delete fieldErrors[errorKey];
  const stored = scope === "global" ? current.global : current.workspace;
  if (hasOwn(stored, field.key) && JSON.stringify(stored[field.key]) === JSON.stringify(value)) return;
  void run(() => setPluginConfiguration(current.pluginId, scope, { [field.key]: value }, scope === "workspace" ? workspaceId.value : undefined));
}

function resetField(field: PluginConfigurationField, scope: PluginConfigurationScope) {
  const current = state.value;
  if (!current) return;
  void run(() => setPluginConfiguration(current.pluginId, scope, { [field.key]: null }, scope === "workspace" ? workspaceId.value : undefined));
}

function toggleOverride(field: PluginConfigurationField, enabled: boolean) {
  const current = state.value;
  if (!current) return;
  if (!enabled) {
    resetField(field, "workspace");
    return;
  }
  const inherited = hasOwn(current.global, field.key) ? current.global : field.property.default === undefined ? {} : { [field.key]: field.property.default };
  const value = configurationFieldValue(field, {}, inherited);
  void run(() => setPluginConfiguration(current.pluginId, "workspace", { [field.key]: value }, workspaceId.value));
}

function saveSecret(field: PluginConfigurationField) {
  const current = state.value;
  const value = secretDraft[field.key] ?? "";
  if (!current || !value) return;
  if (!isValidConfigurationValue({ ...field.property, secret: false }, value)) {
    fieldErrors[fieldErrorKey(field, "global")] = t("pluginContrib.configuration.invalidValue");
    return;
  }
  delete fieldErrors[fieldErrorKey(field, "global")];
  void run(async () => {
    await setPluginConfigurationSecret(current.pluginId, field.key, value);
    secretDraft[field.key] = "";
  }, t("pluginContrib.configuration.secretSaved"));
}

async function clearSecret(pluginId: string, key: string) {
  try {
    await ElMessageBox.confirm(t("pluginContrib.configuration.clearSecretConfirm", { key }), t("pluginContrib.configuration.clearSecret"), {
      type: "warning",
      confirmButtonText: t("pluginContrib.configuration.clearSecret"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  await run(() => setPluginConfigurationSecret(pluginId, key, null), t("pluginContrib.configuration.secretCleared"));
}

function resetIgnored(item: PluginConfigurationIgnoredValue) {
  const current = state.value;
  if (!current) return;
  void run(() => setPluginConfiguration(current.pluginId, item.scope, { [item.key]: null }, item.workspaceId));
}

async function resetAll() {
  const current = state.value;
  if (!current) return;
  try {
    await ElMessageBox.confirm(t("pluginContrib.configuration.resetAllConfirm"), t("pluginContrib.configuration.resetAll"), {
      type: "warning",
      confirmButtonText: t("pluginContrib.configuration.resetAll"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  await run(() => resetPluginConfiguration(current.pluginId));
}

function ignoredScope(item: PluginConfigurationIgnoredValue) {
  if (item.scope === "global") return t("pluginContrib.configuration.scopeGlobal");
  return item.workspaceId === workspaceId.value
    ? t("pluginContrib.configuration.scopeCurrentWorkspace")
    : t("pluginContrib.configuration.scopeOtherWorkspace", { id: item.workspaceId ?? "" });
}

watch(selectedId, () => {
  if (missingPlugin.value && missingPlugin.value.id !== selectedId.value) missingPlugin.value = undefined;
  if (selectedPlugin.value) lastSelected = selectedPlugin.value;
  focusPage.value = false;
  void loadState();
});
// A new contribution snapshot adds or removes settings pages (a disabled plugin's page leaves).
watch(pluginSettingsPages, () => {
  if (!listLoaded || disposed) return;
  const before = selectedId.value;
  const wasMissing = Boolean(missingPlugin.value);
  reconcileSelection(lastSelected ?? missingPlugin.value);
  // A plugin that came back under the same selection reloads its form; a changed selection loads via selectedId.
  if (wasMissing && !missingPlugin.value && selectedId.value === before) void loadState();
});
watch(workspaceId, () => void loadState());
watch(
  () => props.initialPluginId,
  (id) => {
    if (id && plugins.value.some((plugin) => plugin.id === id)) selectedId.value = id;
    else if (id) void loadPlugins({ preferred: id });
  }
);

onMounted(() => {
  if (!desktop) return;
  void loadPlugins({ preferred: props.initialPluginId });
  stopListening = listenPluginConfigurationChanged((event) => {
    if (event.pluginId === selectedId.value && !saving.value) void loadState({ preserveDrafts: true, silent: true });
  });
  stopInvalidation = listenPluginSettingsInvalidated(() => refreshTrigger.schedule());
});
onBeforeUnmount(() => {
  disposed = true;
  stopListening?.();
  stopInvalidation?.();
  refreshTrigger.cancel();
  listGate.invalidate();
  stateGate.invalidate();
});
</script>

<template>
  <section class="settings-flat-page app-plugin-settings">
    <header class="app-plugin-settings__header">
      <div class="settings-section-label">{{ t("pluginContrib.configuration.title") }}</div>
      <p class="settings-page-note">{{ t("pluginContrib.configuration.description") }}</p>
    </header>

    <el-alert v-if="!desktop" type="warning" :closable="false" :title="t('pluginContrib.configuration.desktopOnly')" />
    <template v-else>
      <el-alert v-if="listError" type="error" :closable="false" :title="t('pluginContrib.configuration.listFailed', { message: listError })" />
      <el-empty
        v-if="!listError && !listLoading && plugins.length === 0 && !missingPlugin"
        :image-size="64"
        :description="t('pluginContrib.configuration.empty')"
      />
      <!-- A failed background refresh keeps the last list usable. -->
      <div v-else-if="listLoading || plugins.length > 0 || missingPlugin" v-loading="listLoading" class="app-plugin-settings__picker">
        <el-select
          v-model="selectedId"
          filterable
          :aria-label="t('pluginContrib.configuration.selectPlugin')"
          :placeholder="t('pluginContrib.configuration.selectPlugin')"
        >
          <el-option v-for="plugin in plugins" :key="plugin.id" :value="plugin.id" :label="plugin.name">
            <span>{{ plugin.name }}</span>
            <el-tag v-if="plugin.development" size="small" type="warning" class="app-plugin-settings__option-tag">{{ t("pluginContrib.configuration.development") }}</el-tag>
            <el-tag v-if="!plugin.enabled" size="small" type="info" class="app-plugin-settings__option-tag">{{ t("pluginContrib.configuration.disabled") }}</el-tag>
          </el-option>
          <el-option v-if="missingPlugin" :key="`missing:${missingPlugin.id}`" :value="missingPlugin.id" :label="missingPlugin.name" disabled>
            <span>{{ missingPlugin.name }}</span>
            <el-tag size="small" type="info" class="app-plugin-settings__option-tag">{{ t("pluginContrib.configuration.unavailable") }}</el-tag>
          </el-option>
        </el-select>
        <el-tag v-if="selectedPlugin && !selectedPlugin.enabled" type="info">{{ t("pluginContrib.configuration.disabledHint") }}</el-tag>
      </div>

      <el-alert
        v-if="missingPlugin"
        type="warning"
        :closable="false"
        :title="t('pluginContrib.configuration.pluginUnavailable', { name: missingPlugin.name })"
      />
      <el-alert
        v-else-if="selectedPlugin?.development"
        type="info"
        :closable="false"
        :title="t('pluginContrib.configuration.developmentHint')"
      />

      <el-segmented
        v-if="showModeSwitch"
        v-model="mode"
        class="app-plugin-settings__mode"
        :options="modeOptions"
        :aria-label="t('pluginContrib.settingsPages.modeLabel')"
      />
      <!-- The plugin's own page: mounted only while Settings is open and the page is chosen. -->
      <PluginSettingsPageSection
        v-if="active && selectedPage && mode === 'page'"
        :key="selectedPage.pluginId"
        :page="selectedPage"
        :autofocus="focusPage"
        @navigate="emit('navigate')"
      />

      <el-alert v-if="mode === 'options' && stateError" type="error" :closable="false" :title="t('pluginContrib.configuration.loadFailed', { message: stateError })" />

      <div v-if="mode === 'options' && state" ref="formBody" v-loading="stateLoading" class="app-plugin-settings__body" tabindex="-1">
        <div v-if="schemaTitle" class="app-plugin-settings__schema-title">{{ schemaTitle }}</div>

        <el-alert v-if="state.storeError" type="error" :closable="false" :title="t('pluginContrib.configuration.storeError', { message: state.storeError })">
          <el-button size="small" type="danger" plain :disabled="saving" @click="resetAll">{{ t("pluginContrib.configuration.resetAll") }}</el-button>
        </el-alert>
        <el-alert v-if="state.secretsReadFailed" type="warning" :closable="false" :title="t('pluginContrib.configuration.secretsReadFailed')" />

        <el-form label-position="top" class="app-plugin-settings__form" @submit.prevent>
          <el-form-item
            v-for="field in fields"
            :key="field.key"
            :label="field.title"
            :error="fieldErrors[`global:${field.key}`]"
            class="app-plugin-settings__item"
          >
            <div class="app-plugin-settings__field">
              <p v-if="field.description" class="settings-page-note app-plugin-settings__description">{{ field.description }}</p>
              <code class="app-plugin-settings__key">{{ field.key }}</code>

              <!-- secret: write-only -->
              <template v-if="field.control === 'secret'">
                <div class="app-plugin-settings__row">
                  <el-tag :type="state.secrets[field.key] ? 'success' : 'info'" size="small">
                    {{ state.secrets[field.key] ? t("pluginContrib.configuration.secretSet") : t("pluginContrib.configuration.secretUnset") }}
                  </el-tag>
                  <el-input
                    v-model="secretDraft[field.key]"
                    type="password"
                    show-password
                    autocomplete="off"
                    :aria-label="field.title"
                    :placeholder="state.secrets[field.key] ? t('pluginContrib.configuration.secretReplacePlaceholder') : t('pluginContrib.configuration.secretPlaceholder')"
                    :disabled="saving"
                    @keydown.enter.prevent="saveSecret(field)"
                  />
                  <el-button :disabled="saving || !secretDraft[field.key]" @click="saveSecret(field)">
                    {{ state.secrets[field.key] ? t("pluginContrib.configuration.replaceSecret") : t("pluginContrib.configuration.setSecret") }}
                  </el-button>
                  <el-button v-if="state.secrets[field.key]" type="danger" plain :disabled="saving" @click="clearSecret(state.pluginId, field.key)">
                    {{ t("pluginContrib.configuration.clearSecret") }}
                  </el-button>
                </div>
              </template>

              <template v-else>
                <div class="app-plugin-settings__row">
                  <el-switch
                    v-if="field.control === 'boolean'"
                    :model-value="(globalDraft[field.key] as boolean)"
                    @update:model-value="globalDraft[field.key] = $event"
                    :aria-label="field.title"
                    :disabled="saving"
                    @change="commit(field, 'global')"
                  />
                  <el-input-number
                    v-else-if="field.control === 'number' || field.control === 'integer'"
                    :model-value="(globalDraft[field.key] as number)"
                    @update:model-value="globalDraft[field.key] = $event"
                    controls-position="right"
                    :aria-label="field.title"
                    :min="field.property.minimum"
                    :max="field.property.maximum"
                    :step="1"
                    :precision="field.control === 'integer' ? 0 : undefined"
                    :disabled="saving"
                    @change="commit(field, 'global')"
                  />
                  <el-select
                    v-else-if="field.control === 'enum'"
                    :model-value="(globalDraft[field.key] as string | number)"
                    @update:model-value="globalDraft[field.key] = $event"
                    :aria-label="field.title"
                    :disabled="saving"
                    @change="commit(field, 'global')"
                  >
                    <el-option v-for="option in field.options" :key="String(option.value)" :value="option.value" :label="option.label" />
                  </el-select>
                  <el-select
                    v-else-if="field.control === 'array'"
                    :model-value="(globalDraft[field.key] as string[])"
                    @update:model-value="globalDraft[field.key] = $event"
                    multiple
                    filterable
                    :allow-create="field.options.length === 0"
                    :default-first-option="field.options.length === 0"
                    :multiple-limit="field.property.maxItems ?? 0"
                    :aria-label="field.title"
                    :placeholder="field.options.length === 0 ? t('pluginContrib.configuration.listPlaceholder') : undefined"
                    :disabled="saving"
                    @change="commit(field, 'global')"
                  >
                    <el-option v-for="option in field.options" :key="String(option.value)" :value="option.value" :label="option.label" />
                  </el-select>
                  <el-input
                    v-else
                    :model-value="(globalDraft[field.key] as string)"
                    @update:model-value="globalDraft[field.key] = $event"
                    :aria-label="field.title"
                    :maxlength="field.property.maxLength"
                    :placeholder="field.property.format === 'url' ? 'https://' : undefined"
                    :disabled="saving"
                    @change="commit(field, 'global')"
                  />
                  <el-button
                    v-if="hasOwn(state.global, field.key)"
                    text
                    size="small"
                    :disabled="saving"
                    :aria-label="t('pluginContrib.configuration.resetFieldLabel', { name: field.title })"
                    @click="resetField(field, 'global')"
                  >
                    {{ t("pluginContrib.configuration.resetField") }}
                  </el-button>
                </div>

                <!-- per-workspace override -->
                <div v-if="field.scope === 'workspace' && workspaceId" class="app-plugin-settings__override">
                  <el-switch
                    :model-value="hasOwn(state.workspace, field.key)"
                    :disabled="saving"
                    :active-text="t('pluginContrib.configuration.overrideWorkspace', { name: workspaceName })"
                    @change="toggleOverride(field, Boolean($event))"
                  />
                  <div v-if="hasOwn(state.workspace, field.key)" class="app-plugin-settings__row">
                    <el-switch
                      v-if="field.control === 'boolean'"
                      :model-value="(workspaceDraft[field.key] as boolean)"
                      @update:model-value="workspaceDraft[field.key] = $event"
                      :aria-label="t('pluginContrib.configuration.workspaceValue', { name: field.title })"
                      :disabled="saving"
                      @change="commit(field, 'workspace')"
                    />
                    <el-input-number
                      v-else-if="field.control === 'number' || field.control === 'integer'"
                      :model-value="(workspaceDraft[field.key] as number)"
                      @update:model-value="workspaceDraft[field.key] = $event"
                      controls-position="right"
                      :aria-label="t('pluginContrib.configuration.workspaceValue', { name: field.title })"
                      :min="field.property.minimum"
                      :max="field.property.maximum"
                      :step="1"
                      :precision="field.control === 'integer' ? 0 : undefined"
                      :disabled="saving"
                      @change="commit(field, 'workspace')"
                    />
                    <el-select
                      v-else-if="field.control === 'enum' || field.control === 'array'"
                      :model-value="(workspaceDraft[field.key] as string | number | string[])"
                      @update:model-value="workspaceDraft[field.key] = $event"
                      :multiple="field.control === 'array'"
                      filterable
                      :allow-create="field.control === 'array' && field.options.length === 0"
                      :default-first-option="field.control === 'array' && field.options.length === 0"
                      :aria-label="t('pluginContrib.configuration.workspaceValue', { name: field.title })"
                      :disabled="saving"
                      @change="commit(field, 'workspace')"
                    >
                      <el-option v-for="option in field.options" :key="String(option.value)" :value="option.value" :label="option.label" />
                    </el-select>
                    <el-input
                      v-else
                      :model-value="(workspaceDraft[field.key] as string)"
                      @update:model-value="workspaceDraft[field.key] = $event"
                      :aria-label="t('pluginContrib.configuration.workspaceValue', { name: field.title })"
                      :maxlength="field.property.maxLength"
                      :disabled="saving"
                      @change="commit(field, 'workspace')"
                    />
                  </div>
                  <p v-if="fieldErrors[`workspace:${field.key}`]" class="app-plugin-settings__error">{{ fieldErrors[`workspace:${field.key}`] }}</p>
                </div>
              </template>
            </div>
          </el-form-item>
        </el-form>

        <section v-if="state.ignored.length" class="app-plugin-settings__cleanup">
          <div class="settings-section-label">{{ t("pluginContrib.configuration.ignoredTitle") }}</div>
          <p class="settings-page-note">{{ t("pluginContrib.configuration.ignoredHint") }}</p>
          <div v-for="item in state.ignored" :key="`${item.scope}:${item.workspaceId ?? ''}:${item.key}`" class="settings-data-row">
            <span>
              <strong>{{ item.key }}</strong>
              <small>{{ ignoredScope(item) }} · {{ item.reason }}</small>
            </span>
            <el-button size="small" :disabled="saving" @click="resetIgnored(item)">{{ t("pluginContrib.configuration.resetIgnored") }}</el-button>
          </div>
        </section>

        <section v-if="state.orphanSecrets.length" class="app-plugin-settings__cleanup">
          <div class="settings-section-label">{{ t("pluginContrib.configuration.orphanTitle") }}</div>
          <p class="settings-page-note">{{ t("pluginContrib.configuration.orphanHint") }}</p>
          <div v-for="key in state.orphanSecrets" :key="key" class="settings-data-row">
            <span><strong>{{ key }}</strong></span>
            <el-button size="small" type="danger" plain :disabled="saving" @click="clearSecret(state.pluginId, key)">
              {{ t("pluginContrib.configuration.deleteOrphan") }}
            </el-button>
          </div>
        </section>
      </div>
    </template>
  </section>
</template>
