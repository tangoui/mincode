<script setup lang="ts">
/**
 * Keybinding settings: keymap preset, options, and the per-command table with
 * change / add alternate / remove / reset, plus VS Code-shaped import/export.
 * All state lives in the global keybindings store; the view model (rows,
 * filters, override edits, import preview) is the pure settings-model module.
 * Lazy-loaded by SettingsWorkspaceView.
 */
import "@/styles/workspaces/settings/keybindings.css";
import { computed, defineAsyncComponent, onBeforeUnmount, reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, EditPen, Plus, RefreshLeft } from "@element-plus/icons-vue";
import AppFilterButton from "@/components/ui/AppFilterButton.vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { getKeybindingCommand, KEYBINDING_COMMANDS, type KeybindingScope } from "@/domain/keybindings/catalog";
import { importKeybindings } from "@/domain/keybindings/import-export";
import { formatKeySequence, keyChordFromEvent, sequenceId } from "@/domain/keybindings/keys";
import { listKeymapPresets, subscribeKeymapPresets } from "@/domain/keybindings/preset";
import type { ResolvedKeybinding } from "@/domain/keybindings/resolver";
import {
  applyImportedOverrides,
  buildImportPreview,
  buildKeybindingRows,
  catalogRowCommand,
  EMPTY_RECORDER_STATE,
  exportUserKeybindings,
  filterKeybindingRows,
  importedOverrides,
  overridesAfterChange,
  overridesAfterRemove,
  overridesAfterReset,
  pluginRowCommand,
  recordChord,
  recorderSequence,
  type KeybindingImportPreview,
  type KeybindingRow,
  type KeybindingRowFilter,
  type KeybindingRowScope,
  type RecorderState,
} from "@/domain/keybindings/settings-model";
import type { KeybindingOverride } from "@/domain/keybindings/persistence";
import { useRegisteredCommands } from "@/services/command-registry";
import { saveTextFileToDisk } from "@/services/file-download";
import { chooseSettingsJsonFile, readSettingsTextFile } from "@/services/settings-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useKeybindingsStore } from "@/stores/keybindings";
import { useWorkbenchStore } from "@/stores/workbench";

const KeybindingRecorderDialog = defineAsyncComponent(() => import("@/components/settings/KeybindingRecorderDialog.vue"));
const PluginGlobalShortcutsSection = defineAsyncComponent(() => import("@/components/settings/PluginGlobalShortcutsSection.vue"));

const props = defineProps<{ globalShortcutEnabled: boolean }>();
/** Emitted before navigating away (the settings dialog closes itself). */
const emit = defineEmits<{ navigate: [] }>();

const { t, locale } = useVueI18n();
const store = useKeybindingsStore();
const platform = store.platform;
void store.hydrate();

// ---------------------------------------------------------------- presets
const presetRevision = ref(0);
const unsubscribePresets = subscribeKeymapPresets(() => {
  presetRevision.value += 1;
});
const MARKET_PLACEHOLDER = "__market__";
/** Marketplace search that lists keymap plugins (the official `keymaps` plugin and third-party ones carry the `keymap` tag). */
const KEYMAP_MARKET_SEARCH = "keymap";
const presetOptions = computed(() => {
  void presetRevision.value;
  return listKeymapPresets().map((preset) => ({
    value: preset.id,
    label: locale.value === "en" ? preset.title.en : preset.title.zh,
    source:
      preset.source === "plugin"
        ? t("settings.keybindings.presetSourcePlugin", { plugin: preset.pluginName ?? preset.pluginId ?? "" })
        : t("settings.keybindings.presetSourceBuiltin"),
  }));
});
const presetUnavailableTitle = computed(() =>
  store.presetPluginSuggestion
    ? t("settings.keybindings.presetUnavailablePlugin", { id: store.presetId, plugin: store.presetPluginSuggestion })
    : t("settings.keybindings.presetUnavailable", { id: store.presetId }),
);
const router = useRouter();
const workbench = useWorkbenchStore();
async function openKeymapMarketplace() {
  if (!workbench.openView("plugins")) return;
  emit("navigate");
  await router.push({ name: "plugins", query: { search: KEYMAP_MARKET_SEARCH } });
}
function selectPreset(id: string) {
  if (id === MARKET_PLACEHOLDER) {
    void openKeymapMarketplace();
    return;
  }
  store.setPreset(id);
}

// ---------------------------------------------------------------- rows
const pluginCommands = useRegisteredCommands();
const rows = computed(() =>
  buildKeybindingRows({
    commands: [
      ...KEYBINDING_COMMANDS.map(catalogRowCommand),
      ...pluginCommands.value
        .filter((command) => command.source === "plugin" && command.id.startsWith("plugin:"))
        .map((command) => pluginRowCommand(command.id, command.title)),
    ],
    resolution: store.resolution,
    overrides: store.overrides,
    presetId: store.activePreset?.id ?? store.presetId,
    platform,
  }),
);

function commandTitle(id: string): string {
  const entry = getKeybindingCommand(id);
  if (entry) return t(entry.titleKey);
  return pluginCommands.value.find((command) => command.id === id)?.title ?? id;
}
function rowTitle(row: KeybindingRow): string {
  return row.titleKey ? t(row.titleKey) : row.title ?? row.id;
}
function scopeLabel(scope: KeybindingRowScope) {
  return t(`settings.keybindings.scope.${scope}` as MessageKey);
}
function sourceLabel(row: KeybindingRow) {
  return t(`settings.keybindings.source.${row.source}` as MessageKey);
}
function sourceTagType(row: KeybindingRow) {
  if (row.source === "user") return "primary";
  if (row.source === "removed") return "danger";
  if (row.source === "preset") return "success";
  return "info";
}

// ---------------------------------------------------------------- search & filter
const searchText = ref("");
const filter = ref<KeybindingRowFilter>("all");
const filterOpen = ref(false);
const SCOPES: readonly (KeybindingScope | "plugin")[] = ["global", "editor", "diff", "terminal", "ai", "project", "database", "plugin"];
const filterOptions = computed(() => [
  { value: "all", label: t("settings.keybindings.filter.all") },
  { value: "modified", label: t("settings.keybindings.filter.modified") },
  { value: "conflicts", label: t("settings.keybindings.filter.conflicts") },
  ...SCOPES.map((scope) => ({ value: `scope:${scope}`, label: scopeLabel(scope) })),
]);

const keySearchActive = ref(false);
const keySearch = ref<RecorderState>(EMPTY_RECORDER_STATE);
const searchField = ref<{ focus: () => void }>();
const keySearchSequence = computed(() => recorderSequence(keySearch.value));
const keySearchLabel = computed(() => (keySearchSequence.value ? formatKeySequence(keySearchSequence.value, platform) : ""));
let searchFocused = false;

function syncSearchRecording() {
  // Pause the dispatcher (and the double-modifier detector) only while keys are being captured here.
  store.setRecording(keySearchActive.value && searchFocused);
}
function toggleKeySearch() {
  keySearchActive.value = !keySearchActive.value;
  if (!keySearchActive.value) keySearch.value = EMPTY_RECORDER_STATE;
  syncSearchRecording();
  searchField.value?.focus();
}
function clearKeySearch() {
  keySearch.value = EMPTY_RECORDER_STATE;
  searchField.value?.focus();
}
function updateSearchText(value: string) {
  if (!keySearchActive.value) searchText.value = value;
}
function onSearchFocus(focused: boolean) {
  searchFocused = focused;
  syncSearchRecording();
}
function onSearchKeydown(event: KeyboardEvent) {
  if (!keySearchActive.value || event.isComposing || event.keyCode === 229) return;
  const bare = !event.ctrlKey && !event.altKey && !event.metaKey && !event.shiftKey;
  if (bare && event.key === "Tab") return;
  if (bare && event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    toggleKeySearch();
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  if (event.repeat) return;
  const chord = keyChordFromEvent(event);
  if (chord) keySearch.value = recordChord(keySearch.value, chord, performance.now());
}

const visibleRows = computed(() =>
  filterKeybindingRows(
    rows.value,
    {
      text: keySearchActive.value ? "" : searchText.value,
      keySequence: keySearchActive.value && keySearchSequence.value ? sequenceId(keySearchSequence.value) : undefined,
      filter: filter.value,
    },
    rowTitle,
    platform,
  ),
);
const conflictRowCount = computed(() => rows.value.filter((row) => row.conflictCount > 0).length);

// ---------------------------------------------------------------- row actions
function commit(next: KeybindingOverride[]) {
  const rejected = store.setOverrides(next);
  if (rejected > 0) ElMessage.error(t("settings.keybindings.rejected", { count: rejected }));
  return rejected;
}

const recorder = reactive<{ open: boolean; row?: KeybindingRow; mode: "change" | "add"; target?: ResolvedKeybinding }>({
  open: false,
  mode: "change",
});

function openRecorder(row: KeybindingRow, mode: "change" | "add") {
  recorder.row = row;
  recorder.mode = mode;
  recorder.target = mode === "change" ? row.bindings[0]?.binding : undefined;
  recorder.open = true;
}

function saveRecorded(value: { key: string; when?: string }) {
  if (!recorder.row) return;
  const change = { command: recorder.row.id, key: value.key, platform, ...(value.when ? { when: value.when } : {}) };
  const next = overridesAfterChange(store.overrides, recorder.target ? { ...change, target: recorder.target } : change);
  if (commit(next) === 0) ElMessage.success(t("settings.keybindings.saved"));
}

function removeBindings(row: KeybindingRow) {
  commit(overridesAfterRemove(store.overrides, row.id, platform));
}

function resetRow(row: KeybindingRow) {
  commit(overridesAfterReset(store.overrides, row.id));
}

async function resetAll() {
  try {
    await ElMessageBox.confirm(
      t("settings.keybindings.resetAllConfirm", { count: store.overrides.length }),
      t("settings.keybindings.resetAll"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  store.resetOverrides();
  ElMessage.success(t("settings.keybindings.resetAllDone"));
}

function actionLabel(action: MessageKey, row: KeybindingRow) {
  return t("settings.keybindings.actionLabel", { action: t(action), command: rowTitle(row) });
}

// ---------------------------------------------------------------- import / export
async function exportBindings() {
  const content = exportUserKeybindings(store.overrides, platform);
  try {
    const saved = await saveTextFileToDisk({
      title: t("settings.keybindings.exportTitle"),
      filename: "mincode-keybindings.json",
      content,
      filters: [{ DisplayName: "JSON", Pattern: "*.json" }],
      browserMimeType: "application/json",
    });
    if (saved) ElMessage.success(t("settings.keybindings.exportDone"));
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

const importInput = ref<HTMLInputElement>();
const importPreview = ref<KeybindingImportPreview>();
const importOpen = ref(false);

function previewImport(text: string) {
  try {
    importPreview.value = buildImportPreview(importKeybindings(text, platform), platform);
    importOpen.value = true;
  } catch (cause) {
    ElMessage.error(
      t("settings.keybindings.importInvalid", { error: cause instanceof Error ? cause.message : String(cause) }),
    );
  }
}

// Desktop reads the chosen file through the backend; browser preview uses a file input.
async function startImport() {
  if (!isDesktopRuntime()) {
    importInput.value?.click();
    return;
  }
  try {
    const path = await chooseSettingsJsonFile(t("settings.keybindings.importTitle"));
    if (!path) return;
    previewImport(await readSettingsTextFile(path));
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

async function importFromInput(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  previewImport(await file.text());
}

function applyImport(mode: "merge" | "replace") {
  if (!importPreview.value) return;
  const imported = importedOverrides(importPreview.value, platform);
  const rejected = commit(applyImportedOverrides(store.overrides, imported, mode));
  importOpen.value = false;
  ElMessage.success(t("settings.keybindings.importDone", { count: imported.length - rejected }));
}

function importReason(reason: string) {
  return t(`settings.keybindings.importReason.${reason}` as MessageKey);
}
function describeEntry(entry: unknown) {
  try {
    const text = JSON.stringify(entry);
    return text.length > 120 ? `${text.slice(0, 117)}...` : text;
  } catch {
    return String(entry);
  }
}

onBeforeUnmount(() => {
  unsubscribePresets();
  if (keySearchActive.value && searchFocused) store.setRecording(false);
});
</script>

<template>
  <section class="settings-flat-page app-keybindings" data-testid="keybinding-settings-panel">
    <div class="settings-section-label">
      <span class="app-inline-label">
        {{ t("settings.shortcuts") }}
        <AppHintIcon :text="t('settings.keybindings.hint')" />
      </span>
    </div>

    <el-alert
      v-if="store.presetUnavailable"
      class="app-keybindings__alert"
      type="warning"
      show-icon
      :closable="false"
      :title="presetUnavailableTitle"
    >
      <el-button
        v-if="store.presetPluginSuggestion"
        link
        type="primary"
        data-testid="keybinding-preset-market"
        @click="openKeymapMarketplace"
      >
        {{ t("settings.keybindings.presetOpenMarket") }}
      </el-button>
    </el-alert>

    <el-form label-position="left" label-width="auto" class="app-keybindings__options">
      <el-form-item>
        <template #label>
          <span class="app-inline-label">
            {{ t("settings.keybindings.preset") }}
            <AppHintIcon :text="t('settings.keybindings.presetHint')" />
          </span>
        </template>
        <el-select
          class="app-keybindings__preset"
          :model-value="store.presetId"
          :aria-label="t('settings.keybindings.preset')"
          data-testid="keybinding-preset-select"
          @update:model-value="selectPreset"
        >
          <el-option v-for="option in presetOptions" :key="option.value" :label="option.label" :value="option.value">
            <span class="app-keybindings__preset-option">
              <span>{{ option.label }}</span>
              <span class="app-keybindings__preset-source">{{ option.source }}</span>
            </span>
          </el-option>
          <el-option
            :value="MARKET_PLACEHOLDER"
            :label="t('settings.keybindings.presetMarket')"
            class="app-keybindings__preset-market"
            data-testid="keybinding-preset-market-option"
          />
        </el-select>
        <span class="app-keybindings__note">{{ t("settings.keybindings.presetNote") }}</span>
      </el-form-item>
      <el-form-item>
        <template #label>
          <span class="app-inline-label">
            {{ t("settings.keybindings.option.doubleModifier") }}
            <AppHintIcon :text="t('settings.keybindings.option.doubleModifierHint')" />
          </span>
        </template>
        <el-switch
          :model-value="store.doubleModifier"
          :aria-label="t('settings.keybindings.option.doubleModifier')"
          @update:model-value="store.setOptions({ doubleModifier: $event === true })"
        />
      </el-form-item>
      <el-form-item>
        <template #label>
          <span class="app-inline-label">
            {{ t("settings.keybindings.option.sendToShell") }}
            <AppHintIcon :text="t('settings.keybindings.option.sendToShellHint')" />
          </span>
        </template>
        <el-switch
          :model-value="store.options.sendKeybindingsToShell"
          :aria-label="t('settings.keybindings.option.sendToShell')"
          @update:model-value="store.setOptions({ sendKeybindingsToShell: $event === true })"
        />
      </el-form-item>
    </el-form>

    <div class="app-keybindings__toolbar">
      <div
        class="app-keybindings__search"
        :class="{ 'is-recording': keySearchActive }"
        @keydown.capture="onSearchKeydown"
        @focusin="onSearchFocus(true)"
        @focusout="onSearchFocus(false)"
      >
        <AppSearchField
          ref="searchField"
          :model-value="keySearchActive ? keySearchLabel : searchText"
          :placeholder="t(keySearchActive ? 'settings.keybindings.recordSearchActive' : 'settings.keybindings.search')"
          :aria-label="t(keySearchActive ? 'settings.keybindings.recordSearchActive' : 'settings.keybindings.search')"
          :clearable="!keySearchActive"
          :clear-label="t('common.clear')"
          @update:model-value="updateSearchText"
        />
      </div>
      <el-button
        v-if="keySearchActive && keySearchLabel"
        size="small"
        text
        @click="clearKeySearch"
      >{{ t("common.clear") }}</el-button>
      <el-button
        size="small"
        :type="keySearchActive ? 'primary' : undefined"
        :plain="keySearchActive"
        :aria-pressed="keySearchActive"
        data-testid="keybinding-key-search"
        @click="toggleKeySearch"
      >{{ t("settings.keybindings.recordSearch") }}</el-button>
      <el-popover v-model:visible="filterOpen" trigger="click" placement="bottom-end" :width="200">
        <template #reference>
          <AppFilterButton
            :active="filter !== 'all'"
            :label="t('settings.keybindings.filter')"
            :aria-expanded="filterOpen"
          />
        </template>
        <el-radio-group
          v-model="filter"
          class="app-keybindings__filter"
          :aria-label="t('settings.keybindings.filter')"
          @change="filterOpen = false"
        >
          <el-radio v-for="option in filterOptions" :key="option.value" :value="option.value">
            {{ option.label }}
            <span v-if="option.value === 'conflicts' && conflictRowCount" class="app-keybindings__count">{{ conflictRowCount }}</span>
          </el-radio>
        </el-radio-group>
      </el-popover>
      <span class="app-keybindings__spacer" />
      <el-button size="small" @click="startImport">{{ t("common.import") }}</el-button>
      <el-button size="small" :disabled="!store.overrides.length" @click="exportBindings">{{ t("common.export") }}</el-button>
      <el-button size="small" :disabled="!store.overrides.length" data-testid="keybinding-reset-all" @click="resetAll">
        {{ t("settings.keybindings.resetAll") }}
      </el-button>
      <input ref="importInput" type="file" accept="application/json,.json,.jsonc" hidden @change="importFromInput" />
    </div>

    <!-- A plain table: el-table made every cell a component (89 commands × 5 columns, plus a
         button component per row action) and opening this tab froze for ~150 ms. -->
    <div class="app-keybindings__table" data-testid="keybinding-table">
      <table>
        <colgroup>
          <col class="app-keybindings__col-command" />
          <col class="app-keybindings__col-keys" />
          <col class="app-keybindings__col-when" />
          <col class="app-keybindings__col-source" />
          <col class="app-keybindings__col-actions" />
        </colgroup>
        <thead>
          <tr>
            <th scope="col">{{ t("settings.keybindings.column.command") }}</th>
            <th scope="col">{{ t("settings.keybindings.column.keybinding") }}</th>
            <th scope="col">{{ t("settings.keybindings.column.when") }}</th>
            <th scope="col">{{ t("settings.keybindings.column.source") }}</th>
            <th scope="col" class="is-right">{{ t("settings.keybindings.column.actions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in visibleRows" :key="row.id">
            <td>
              <div class="app-keybindings__command" :data-command="row.id">
                <span class="app-keybindings__title">
                  {{ rowTitle(row) }}
                  <span v-if="!row.available" class="app-keybindings__tag is-info" :title="t('settings.keybindings.unavailableHint')">
                    {{ t("settings.keybindings.unavailable") }}
                  </span>
                </span>
                <code>{{ row.id }}</code>
              </div>
            </td>
            <td>
              <div class="app-keybindings__keys">
                <template v-if="row.bindings.length">
                  <kbd
                    v-for="binding in row.bindings"
                    :key="`${binding.key}|${binding.when ?? ''}`"
                    class="app-keybinding-kbd"
                    :class="{ 'is-conflict': binding.conflict, 'is-reserved': binding.reserved }"
                    :title="binding.conflict ? t('settings.keybindings.conflictHint') : undefined"
                  >{{ binding.label }}</kbd>
                  <span v-if="row.conflictCount" class="app-keybindings__tag is-warning">{{ t("settings.keybindings.conflictTag") }}</span>
                </template>
                <span v-else class="app-keybindings__muted">{{ t("settings.shortcuts.unbound") }}</span>
              </div>
            </td>
            <td>
              <div class="app-keybindings__when">
                <span>{{ scopeLabel(row.scope) }}</span>
                <template v-for="binding in row.bindings" :key="`w:${binding.key}|${binding.when ?? ''}`">
                  <code v-if="binding.when" :title="binding.when">{{ binding.when }}</code>
                </template>
              </div>
            </td>
            <td>
              <span class="app-keybindings__tag" :class="`is-${sourceTagType(row)}`">{{ sourceLabel(row) }}</span>
            </td>
            <td class="is-right">
              <div class="app-keybindings__actions">
                <button
                  type="button"
                  class="app-keybindings__action"
                  :aria-label="actionLabel('settings.keybindings.action.change', row)"
                  :title="t('settings.keybindings.action.change')"
                  data-testid="keybinding-change"
                  @click="openRecorder(row, 'change')"
                ><EditPen class="app-keybindings__icon" aria-hidden="true" /></button>
                <button
                  type="button"
                  class="app-keybindings__action"
                  :aria-label="actionLabel('settings.keybindings.action.add', row)"
                  :title="t('settings.keybindings.action.add')"
                  @click="openRecorder(row, 'add')"
                ><Plus class="app-keybindings__icon" aria-hidden="true" /></button>
                <button
                  type="button"
                  class="app-keybindings__action"
                  :disabled="!row.bindings.length"
                  :aria-label="actionLabel('settings.keybindings.action.remove', row)"
                  :title="t('settings.keybindings.action.remove')"
                  @click="removeBindings(row)"
                ><Delete class="app-keybindings__icon" aria-hidden="true" /></button>
                <button
                  type="button"
                  class="app-keybindings__action"
                  :disabled="!row.modified"
                  :aria-label="actionLabel('settings.keybindings.action.reset', row)"
                  :title="t('settings.keybindings.action.reset')"
                  data-testid="keybinding-reset"
                  @click="resetRow(row)"
                ><RefreshLeft class="app-keybindings__icon" aria-hidden="true" /></button>
              </div>
            </td>
          </tr>
          <tr v-if="!visibleRows.length">
            <td colspan="5" class="app-keybindings__empty">{{ t("settings.keybindings.empty") }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <PluginGlobalShortcutsSection :global-shortcut-enabled="props.globalShortcutEnabled" />

    <KeybindingRecorderDialog
      v-if="recorder.row"
      v-model="recorder.open"
      :command-id="recorder.row.id"
      :command-title="rowTitle(recorder.row)"
      :mode="recorder.mode"
      :current-label="recorder.row.bindings[0]?.label"
      :initial-when="recorder.target?.when"
      :resolution="store.resolution"
      :platform="platform"
      :global-shortcut-enabled="props.globalShortcutEnabled"
      :double-modifier-enabled="store.doubleModifier"
      :title-of="commandTitle"
      @save="saveRecorded"
    />

    <el-dialog
      v-model="importOpen"
      class="app-keybindings-import"
      width="min(680px, calc(100vw - 40px))"
      append-to-body
      :title="t('settings.keybindings.importPreview.title')"
    >
      <template v-if="importPreview">
        <p class="app-keybindings__note">
          {{ t("settings.keybindings.importPreview.summary", { mapped: importPreview.mapped.length, unsupported: importPreview.unsupported.length }) }}
        </p>
        <h4 class="app-keybindings-import__heading">
          {{ t("settings.keybindings.importPreview.mapped", { count: importPreview.mapped.length }) }}
        </h4>
        <el-table :data="importPreview.mapped" size="small" max-height="240" :empty-text="t('settings.keybindings.importPreview.empty')">
          <el-table-column :label="t('settings.keybindings.column.command')" min-width="200">
            <template #default="{ row }">
              <div class="app-keybindings__command">
                <span class="app-keybindings__title">{{ commandTitle(row.command) }}</span>
                <code>{{ row.command }}</code>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('settings.keybindings.column.keybinding')" min-width="140">
            <template #default="{ row }">
              <el-tag v-if="row.removal" size="small" type="danger">{{ t("settings.keybindings.importPreview.removal") }}</el-tag>
              <kbd v-if="row.label" class="app-keybinding-kbd">{{ row.label }}</kbd>
            </template>
          </el-table-column>
          <el-table-column :label="t('settings.keybindings.column.when')" min-width="120">
            <template #default="{ row }"><code v-if="row.when">{{ row.when }}</code></template>
          </el-table-column>
        </el-table>
        <template v-if="importPreview.unsupported.length">
          <h4 class="app-keybindings-import__heading">
            {{ t("settings.keybindings.importPreview.unsupported", { count: importPreview.unsupported.length }) }}
          </h4>
          <el-scrollbar max-height="160px">
            <ul class="app-keybindings-import__issues">
              <li v-for="issue in importPreview.unsupported" :key="issue.index">
                <el-tag size="small" type="info">{{ importReason(issue.reason) }}</el-tag>
                <code>{{ describeEntry(issue.entry) }}</code>
                <small>{{ issue.detail }}</small>
              </li>
            </ul>
          </el-scrollbar>
        </template>
      </template>
      <template #footer>
        <el-button @click="importOpen = false">{{ t("common.cancel") }}</el-button>
        <el-button :disabled="!importPreview?.mapped.length" @click="applyImport('replace')">
          {{ t("settings.keybindings.importPreview.replace") }}
        </el-button>
        <el-button type="primary" :disabled="!importPreview?.mapped.length" @click="applyImport('merge')">
          {{ t("settings.keybindings.importPreview.merge") }}
        </el-button>
      </template>
    </el-dialog>
  </section>
</template>
