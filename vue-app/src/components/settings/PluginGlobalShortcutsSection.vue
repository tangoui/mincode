<script setup lang="ts">
/**
 * Settings → shortcuts → "Plugin global shortcuts": every globalShortcuts
 * entry of the installed plugins with its switch (off by default), key,
 * change / restore default and the state the Go registry computed. Conflicts,
 * registration and the local-only settings live in the backend
 * (services/plugin-global-shortcuts.ts); this section also pushes the current
 * in-app keys so the backend can flag entries that also trigger inside MinCode.
 */
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { EditPen, RefreshLeft } from "@element-plus/icons-vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  acceleratorsOverlap,
  formatAccelerator,
  globalShortcutInAppKeys,
  globalShortcutSaveError,
  globalShortcutStateTag,
  groupGlobalShortcuts,
  type GlobalShortcutStatus,
} from "@/domain/keybindings/plugin-global-shortcuts";
import { resolvePluginTitle } from "@/domain/plugins/title";
import {
  globalShortcutsAvailable,
  listGlobalShortcuts,
  onGlobalShortcutsChanged,
  resetGlobalShortcut,
  setGlobalShortcut,
  setGlobalShortcutInAppKeys,
} from "@/services/plugin-global-shortcuts";
import { useKeybindingsStore } from "@/stores/keybindings";

const KeybindingRecorderDialog = defineAsyncComponent(() => import("@/components/settings/KeybindingRecorderDialog.vue"));

const props = defineProps<{ globalShortcutEnabled: boolean }>();

const { t, localeChain } = useVueI18n();
const store = useKeybindingsStore();
const platform = store.platform;
const available = globalShortcutsAvailable();

const statuses = ref<GlobalShortcutStatus[]>([]);
const loading = ref(false);
const loaded = ref(false);
const loadError = ref("");
const busy = reactive(new Set<string>());
let generation = 0;
let disposed = false;

function entryKey(status: Pick<GlobalShortcutStatus, "pluginId" | "id">) {
  return `${status.pluginId}/${status.id}`;
}

function errorText(cause: unknown) {
  return cause instanceof Error ? cause.message : String(cause);
}

async function refresh() {
  if (!available || disposed) return;
  const current = ++generation;
  loading.value = true;
  try {
    const next = await listGlobalShortcuts();
    if (disposed || current !== generation) return;
    statuses.value = next;
    loadError.value = "";
    loaded.value = true;
  } catch (cause) {
    if (disposed || current !== generation) return;
    loadError.value = errorText(cause);
  } finally {
    if (current === generation) loading.value = false;
  }
}

// One table, grouped per plugin in the backend's order.
const rows = computed(() => groupGlobalShortcuts(statuses.value).flatMap((group) => group.entries));

function titleOf(status: GlobalShortcutStatus) {
  return resolvePluginTitle(status, localeChain.value);
}
function keyLabel(status: GlobalShortcutStatus) {
  return formatAccelerator(status.key, platform);
}
function stateTag(status: GlobalShortcutStatus) {
  const tag = globalShortcutStateTag(status, statuses.value, titleOf);
  return { type: tag.type, label: t(tag.key, { name: tag.name ?? "" }) };
}

function replaceStatus(next: GlobalShortcutStatus) {
  statuses.value = statuses.value.map((status) => (entryKey(status) === entryKey(next) ? next : status));
}

async function update(status: GlobalShortcutStatus, run: () => Promise<GlobalShortcutStatus>) {
  const id = entryKey(status);
  if (busy.has(id)) return;
  busy.add(id);
  try {
    const next = await run();
    if (!disposed) replaceStatus(next);
  } catch (cause) {
    if (disposed) return;
    // Known backend errors are English; map them, otherwise show the raw text after 「保存失败：」.
    const failure = globalShortcutSaveError(errorText(cause));
    ElMessage.error(t(failure.key, { error: failure.error }));
    // A vanished entry (plugin uninstalled) disappears from the list.
    if (failure.key === "settings.keybindings.pluginGlobal.error.missing") void refresh();
  } finally {
    busy.delete(id);
  }
}

function toggle(status: GlobalShortcutStatus, enabled: boolean) {
  void update(status, () => setGlobalShortcut({ pluginId: status.pluginId, id: status.id, enabled }));
}

function restoreDefault(status: GlobalShortcutStatus) {
  void update(status, () => resetGlobalShortcut(status.pluginId, status.id));
}

// ---------------------------------------------------------------- in-app keys
// In-app bindings plus Monaco's own default editor keys.
const inAppKeys = computed(() => globalShortcutInAppKeys(store.resolution.bindings, platform));
let pushedKeys = "";
let pushTimer: ReturnType<typeof setTimeout> | undefined;

async function pushInAppKeys() {
  const keys = inAppKeys.value;
  const signature = keys.join("\n");
  if (signature === pushedKeys) return;
  try {
    await setGlobalShortcutInAppKeys(keys);
    pushedKeys = signature;
    // The backend emits plugin:global-shortcuts-changed when an overlap flag changes.
  } catch {
    // Only the yellow overlap note depends on it; the next keymap change retries.
  }
}

watch(inAppKeys, () => {
  if (!available) return;
  clearTimeout(pushTimer);
  pushTimer = setTimeout(() => void pushInAppKeys(), 200);
});

// ---------------------------------------------------------------- recorder
const recorder = reactive<{ open: boolean; status?: GlobalShortcutStatus }>({ open: false });

function openRecorder(status: GlobalShortcutStatus) {
  recorder.status = status;
  recorder.open = true;
}

function saveRecorded(value: { key: string }) {
  const status = recorder.status;
  if (!status) return;
  // The same physical keys as the manifest default are stored as "default".
  const key = acceleratorsOverlap(value.key, status.defaultKey, platform) ? "" : value.key;
  void update(status, () => setGlobalShortcut({ pluginId: status.pluginId, id: status.id, key }));
}

function actionLabel(action: "change" | "reset", status: GlobalShortcutStatus) {
  return t(`settings.keybindings.pluginGlobal.action.${action}Label` as MessageKey, { title: titleOf(status) });
}

let stopChanged: (() => void) | undefined;
onMounted(async () => {
  if (!available) return;
  stopChanged = onGlobalShortcutsChanged(() => void refresh());
  await pushInAppKeys();
  await refresh();
});

onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(pushTimer);
  stopChanged?.();
});
</script>

<template>
  <section class="app-plugin-global-shortcuts" data-testid="plugin-global-shortcuts">
    <div class="settings-section-label">
      <span class="app-inline-label">
        {{ t("settings.keybindings.pluginGlobal.title") }}
        <AppHintIcon :text="t('settings.keybindings.pluginGlobal.hint')" />
      </span>
    </div>

    <el-alert
      v-if="!available"
      type="info"
      show-icon
      :closable="false"
      :title="t('settings.keybindings.pluginGlobal.desktopOnly')"
    />
    <el-alert
      v-else-if="loadError"
      type="error"
      show-icon
      :closable="false"
      :title="t('settings.keybindings.pluginGlobal.loadFailed', { error: loadError })"
    >
      <el-button link type="primary" @click="refresh">{{ t("common.refresh") }}</el-button>
    </el-alert>

    <el-table
      v-if="available && !loadError"
      v-loading="loading && !loaded"
      class="app-keybindings__table"
      :data="rows"
      :row-key="entryKey"
      size="small"
      :empty-text="loaded ? t('settings.keybindings.pluginGlobal.empty') : ' '"
      data-testid="plugin-global-shortcuts-table"
    >
      <el-table-column :label="t('settings.keybindings.pluginGlobal.column.plugin')" min-width="120">
        <template #default="{ row }">
          <span class="app-plugin-global-shortcuts__plugin" :title="row.pluginId">{{ row.pluginName }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.keybindings.pluginGlobal.column.title')" min-width="160">
        <template #default="{ row }">
          <div class="app-keybindings__command">
            <span class="app-keybindings__title">{{ titleOf(row) }}</span>
            <span v-if="row.inAppOverlap" class="app-plugin-global-shortcuts__overlap">
              {{ t("settings.keybindings.pluginGlobal.inAppOverlap") }}
            </span>
            <span v-if="row.changedByUpdate" class="app-plugin-global-shortcuts__overlap" data-testid="plugin-global-shortcut-changed">
              {{ t("settings.keybindings.pluginGlobal.changedByUpdate") }}
            </span>
          </div>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.keybindings.pluginGlobal.column.key')" min-width="110">
        <template #default="{ row }">
          <div class="app-keybindings__keys">
            <kbd class="app-keybinding-kbd" :class="{ 'is-conflict': row.state === 'conflict' }">{{ keyLabel(row) }}</kbd>
          </div>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.keybindings.pluginGlobal.column.state')" min-width="130">
        <template #default="{ row }">
          <el-tag size="small" :type="stateTag(row).type" disable-transitions>{{ stateTag(row).label }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.keybindings.column.actions')" width="132" align="right">
        <template #default="{ row }">
          <div class="app-keybindings__actions">
            <el-switch
              size="small"
              :model-value="row.enabled"
              :loading="busy.has(entryKey(row))"
              :aria-label="t('settings.keybindings.pluginGlobal.action.toggleLabel', { title: titleOf(row) })"
              data-testid="plugin-global-shortcut-switch"
              @update:model-value="toggle(row, $event === true)"
            />
            <el-button
              text
              size="small"
              :icon="EditPen"
              :disabled="busy.has(entryKey(row))"
              :aria-label="actionLabel('change', row)"
              :title="t('settings.keybindings.pluginGlobal.action.change')"
              data-testid="plugin-global-shortcut-change"
              @click="openRecorder(row)"
            />
            <el-button
              text
              size="small"
              :icon="RefreshLeft"
              :disabled="row.key === row.defaultKey || busy.has(entryKey(row))"
              :aria-label="actionLabel('reset', row)"
              :title="t('settings.keybindings.pluginGlobal.action.reset')"
              data-testid="plugin-global-shortcut-reset"
              @click="restoreDefault(row)"
            />
          </div>
        </template>
      </el-table-column>
    </el-table>

    <KeybindingRecorderDialog
      v-if="recorder.status"
      v-model="recorder.open"
      variant="global"
      mode="change"
      :command-id="entryKey(recorder.status)"
      :command-title="`${recorder.status.pluginName} · ${titleOf(recorder.status)}`"
      :current-label="keyLabel(recorder.status)"
      :resolution="store.resolution"
      :platform="platform"
      :global-shortcut-enabled="props.globalShortcutEnabled"
      :double-modifier-enabled="store.doubleModifier"
      :title-of="(id) => id"
      :in-app-keys="inAppKeys"
      @save="saveRecorded"
    />
  </section>
</template>
