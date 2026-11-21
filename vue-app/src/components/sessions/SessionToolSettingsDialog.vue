<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ArrowDown, ArrowUp, Delete, FolderOpened, Plus, RefreshLeft, Search } from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import type { SessionProviderId } from "@/domain/ai/session-deep-link";
import {
  CUSTOM_SESSION_FIELD_KEYS,
  DEFAULT_CUSTOM_ASSISTANT_ROLES,
  DEFAULT_CUSTOM_FIELDS,
  DEFAULT_CUSTOM_USER_ROLES,
  MAX_CUSTOM_SESSION_TOOLS,
  builtinSessionTool,
  createCustomSessionTool,
  createDefaultBuiltinPrefs,
  customToolBackendConfig,
  defaultCustomGlob,
  findCustomSessionTool,
  isBuiltinSessionToolId,
  isSessionToolEnabled,
  moveSessionTool,
  normalizeSessionToolPrefs,
  type CustomSessionFieldKey,
  type CustomSessionTool,
  type SessionToolPrefs,
} from "@/domain/ai/session-tools";
import { useVueI18n } from "@/i18n/messages";
import type { MessageKey } from "@/i18n/messages";
import {
  applySessionToolRoots,
  loadSessionToolPrefs,
  saveSessionToolPrefs,
} from "@/services/session-tool-preferences";
import { chooseNativeDirectory, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();

const FIELD_LABEL_KEYS: Record<CustomSessionFieldKey, MessageKey> = {
  sessionId: "sessions.toolSettings.field.sessionId",
  cwd: "sessions.toolSettings.field.cwd",
  title: "sessions.toolSettings.field.title",
  messages: "sessions.toolSettings.field.messages",
  role: "sessions.toolSettings.field.role",
  content: "sessions.toolSettings.field.content",
  timestamp: "sessions.toolSettings.field.timestamp",
  model: "sessions.toolSettings.field.model",
};

const draft = ref<SessionToolPrefs | null>(null);
const selectedId = ref("");
const loading = ref(false);
const saving = ref(false);
const testing = ref(false);
const desktopRuntime = isDesktopRuntime();

function builtinLabel(id: SessionProviderId) {
  return t(`sessions.provider.${id}` as const);
}

function toolLabel(id: string) {
  if (isBuiltinSessionToolId(id)) return builtinLabel(id);
  return (draft.value && findCustomSessionTool(draft.value, id)?.name) || id;
}

function toolMark(id: string) {
  if (isBuiltinSessionToolId(id)) return builtinSessionTool(id).mark;
  return (draft.value && findCustomSessionTool(draft.value, id)?.mark) || "?";
}

const items = computed(() => {
  const prefs = draft.value;
  if (!prefs) return [];
  return prefs.order.map((id, index) => ({
    id,
    label: toolLabel(id),
    mark: toolMark(id),
    builtin: isBuiltinSessionToolId(id),
    enabled: isSessionToolEnabled(prefs, id),
    first: index === 0,
    last: index === prefs.order.length - 1,
  }));
});

const selectedBuiltin = computed(() => {
  const prefs = draft.value;
  const id = selectedId.value;
  if (!prefs || !isBuiltinSessionToolId(id)) return null;
  return { id, definition: builtinSessionTool(id), prefs: prefs.builtins[id] };
});

const selectedCustom = computed<CustomSessionTool | null>(() =>
  draft.value ? (findCustomSessionTool(draft.value, selectedId.value) ?? null) : null,
);

const customLimitReached = computed(
  () => (draft.value?.custom.length ?? 0) >= MAX_CUSTOM_SESSION_TOOLS,
);

async function loadDraft() {
  loading.value = true;
  try {
    const prefs = await loadSessionToolPrefs();
    draft.value = prefs;
    if (!prefs.order.includes(selectedId.value)) selectedId.value = prefs.order[0] ?? "";
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) void loadDraft();
  },
  { immediate: true },
);

function close() {
  emit("update:modelValue", false);
}

function setEnabled(id: string, enabled: boolean) {
  const prefs = draft.value;
  if (!prefs) return;
  if (isBuiltinSessionToolId(id)) {
    prefs.builtins[id].enabled = enabled;
    return;
  }
  const tool = findCustomSessionTool(prefs, id);
  if (tool) tool.enabled = enabled;
}

function move(id: string, delta: number) {
  if (draft.value) draft.value.order = moveSessionTool(draft.value.order, id, delta);
}

function addCustomTool() {
  const prefs = draft.value;
  if (!prefs) return;
  if (customLimitReached.value) {
    ElMessage.warning(t("sessions.toolSettings.maxCustom", { max: MAX_CUSTOM_SESSION_TOOLS }));
    return;
  }
  const tool = createCustomSessionTool(
    t("sessions.toolSettings.newToolName"),
    prefs.custom.map((item) => item.id),
  );
  prefs.custom.push(tool);
  prefs.order.push(tool.id);
  selectedId.value = tool.id;
}

async function removeCustomTool(tool: CustomSessionTool) {
  const prefs = draft.value;
  if (!prefs) return;
  try {
    await ElMessageBox.confirm(
      t("sessions.toolSettings.deleteConfirm", { name: tool.name || tool.id }),
      t("sessions.toolSettings.delete"),
      { type: "warning", confirmButtonText: t("common.delete"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  const index = prefs.order.indexOf(tool.id);
  prefs.custom = prefs.custom.filter((item) => item.id !== tool.id);
  prefs.order = prefs.order.filter((id) => id !== tool.id);
  selectedId.value = prefs.order[Math.max(0, Math.min(index, prefs.order.length - 1))] ?? "";
}

function resetBuiltin(id: SessionProviderId) {
  if (draft.value) draft.value.builtins[id] = createDefaultBuiltinPrefs();
}

async function browseDirectory(target: { dir: string }, name: string) {
  if (!desktopRuntime) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }
  const current = target.dir.trim();
  const directory = await chooseNativeDirectory(
    t("sessions.toolSettings.chooseDir", { name }),
    current.startsWith("/") ? current : undefined,
  );
  if (directory) target.dir = directory;
}

function isAbsoluteDir(dir: string) {
  return dir.startsWith("/") || dir === "~" || /^~[\\/]/.test(dir) || /^[A-Za-z]:[\\/]/.test(dir) || dir.startsWith("\\\\");
}

/** 返回第一条校验错误并选中出错的工具；全部通过时返回空串。 */
function validate(prefs: SessionToolPrefs) {
  for (const id of prefs.order) {
    const name = toolLabel(id);
    if (isBuiltinSessionToolId(id)) {
      const dir = prefs.builtins[id].dir.trim();
      if (dir && !isAbsoluteDir(dir)) {
        selectedId.value = id;
        return t("sessions.toolSettings.dirAbsolute", { name });
      }
      continue;
    }
    const tool = findCustomSessionTool(prefs, id);
    if (!tool) continue;
    if (!tool.name.trim()) {
      selectedId.value = id;
      return t("sessions.toolSettings.nameRequired");
    }
    const dir = tool.dir.trim();
    if (!dir) {
      selectedId.value = id;
      return t("sessions.toolSettings.dirRequired", { name });
    }
    if (!isAbsoluteDir(dir)) {
      selectedId.value = id;
      return t("sessions.toolSettings.dirAbsolute", { name });
    }
  }
  return "";
}

async function testCustomTool(tool: CustomSessionTool) {
  if (!desktopRuntime) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }
  if (!tool.dir.trim()) {
    ElMessage.warning(t("sessions.toolSettings.dirRequired", { name: tool.name || tool.id }));
    return;
  }
  testing.value = true;
  try {
    const sessions = await invokeBackend<unknown[] | null>("list_ai_cli_sessions", {
      adapter: "custom",
      custom: customToolBackendConfig(tool),
    });
    const count = sessions?.length ?? 0;
    if (count > 0) ElMessage.success(t("sessions.toolSettings.testResult", { count }));
    else ElMessage.warning(t("sessions.toolSettings.testEmpty"));
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    testing.value = false;
  }
}

async function save() {
  const current = draft.value;
  if (!current || saving.value) return;
  const problem = validate(current);
  if (problem) {
    ElMessage.warning(problem);
    return;
  }
  const prefs = normalizeSessionToolPrefs(current);
  saving.value = true;
  try {
    // 先让后端校验数据目录，失败时保留对话框以便修改，不落盘。
    await applySessionToolRoots(prefs);
  } catch (error) {
    ElMessage.error(t("sessions.toolSettings.rootsFailed", { error: String(error) }));
    saving.value = false;
    return;
  }
  try {
    await saveSessionToolPrefs(prefs);
    ElMessage.success(t("sessions.toolSettings.saved"));
    close();
  } catch (error) {
    ElMessage.error(t("sessions.toolSettings.saveFailed", { error: String(error) }));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <el-dialog
    class="session-tool-settings"
    :model-value="modelValue"
    width="min(920px, calc(100vw - 40px))"
    align-center
    append-to-body
    destroy-on-close
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <strong>{{ t("sessions.toolSettings.title") }}</strong>
    </template>

    <div v-loading="loading" class="session-tool-settings__body">
      <aside class="session-tool-settings__list">
        <el-scrollbar class="session-tool-settings__list-scroll" tag="ul">
          <li
            v-for="item in items"
            :key="item.id"
            class="session-tool-settings__item"
            :class="{ 'is-active': item.id === selectedId, 'is-disabled': !item.enabled }"
          >
            <button type="button" class="session-tool-settings__item-main" @click="selectedId = item.id">
              <span class="session-tool-settings__mark">{{ item.mark }}</span>
              <span class="session-tool-settings__item-label">{{ item.label }}</span>
              <small>{{ item.builtin ? t("sessions.toolSettings.builtinTag") : t("sessions.toolSettings.customTag") }}</small>
            </button>
            <div class="session-tool-settings__item-actions">
              <el-button
                text
                size="small"
                :icon="ArrowUp"
                :disabled="item.first"
                :title="t('sessions.toolSettings.moveUp')"
                :aria-label="t('sessions.toolSettings.moveUp')"
                @click="move(item.id, -1)"
              />
              <el-button
                text
                size="small"
                :icon="ArrowDown"
                :disabled="item.last"
                :title="t('sessions.toolSettings.moveDown')"
                :aria-label="t('sessions.toolSettings.moveDown')"
                @click="move(item.id, 1)"
              />
              <el-switch
                size="small"
                :model-value="item.enabled"
                :aria-label="t('sessions.toolSettings.enabled')"
                @update:model-value="setEnabled(item.id, Boolean($event))"
              />
            </div>
          </li>
        </el-scrollbar>
        <el-button
          class="session-tool-settings__add"
          :icon="Plus"
          :disabled="!draft || customLimitReached"
          @click="addCustomTool"
        >
          {{ t("sessions.toolSettings.addCustom") }}
        </el-button>
      </aside>

      <el-scrollbar class="session-tool-settings__detail" view-class="session-tool-settings__detail-view">
        <el-form v-if="selectedBuiltin" label-position="top" @submit.prevent>
          <header class="session-tool-settings__detail-header">
            <strong>{{ builtinLabel(selectedBuiltin.id) }}</strong>
            <el-button text :icon="RefreshLeft" @click="resetBuiltin(selectedBuiltin.id)">
              {{ t("sessions.toolSettings.resetTool") }}
            </el-button>
          </header>
          <el-form-item :label="t('sessions.toolSettings.dataDir')">
            <div class="session-tool-settings__row">
              <el-input
                v-model="selectedBuiltin.prefs.dir"
                clearable
                :placeholder="selectedBuiltin.definition.defaultDir"
              />
              <el-button
                :icon="FolderOpened"
                @click="browseDirectory(selectedBuiltin.prefs, builtinLabel(selectedBuiltin.id))"
              >
                {{ t("sessions.toolSettings.browse") }}
              </el-button>
            </div>
            <p class="session-tool-settings__hint">
              {{
                selectedBuiltin.definition.dirEnv
                  ? t("sessions.toolSettings.dirEnvHint", { env: selectedBuiltin.definition.dirEnv })
                  : t("sessions.toolSettings.dirHint")
              }}
            </p>
          </el-form-item>
          <el-form-item :label="t('sessions.toolSettings.resumeTemplate')">
            <el-input
              v-model="selectedBuiltin.prefs.resumeTemplate"
              clearable
              :placeholder="selectedBuiltin.definition.resumeTemplate"
            />
          </el-form-item>
          <el-form-item :label="t('sessions.toolSettings.newTemplate')">
            <el-input
              v-model="selectedBuiltin.prefs.newTemplate"
              clearable
              :placeholder="selectedBuiltin.definition.newTemplate"
            />
            <p class="session-tool-settings__hint">{{ t("sessions.toolSettings.templateHint") }}</p>
          </el-form-item>
        </el-form>

        <el-form v-else-if="selectedCustom" label-position="top" @submit.prevent>
          <header class="session-tool-settings__detail-header">
            <strong>{{ selectedCustom.name || selectedCustom.id }}</strong>
            <div class="session-tool-settings__row is-compact">
              <el-button text :icon="Search" :loading="testing" @click="testCustomTool(selectedCustom)">
                {{ t("sessions.toolSettings.test") }}
              </el-button>
              <el-button text type="danger" :icon="Delete" @click="removeCustomTool(selectedCustom)">
                {{ t("sessions.toolSettings.delete") }}
              </el-button>
            </div>
          </header>
          <div class="session-tool-settings__grid">
            <el-form-item :label="t('sessions.toolSettings.name')" required>
              <el-input v-model="selectedCustom.name" maxlength="60" />
            </el-form-item>
            <el-form-item :label="t('sessions.toolSettings.mark')">
              <el-input v-model="selectedCustom.mark" maxlength="3" />
            </el-form-item>
          </div>
          <el-form-item :label="t('sessions.toolSettings.dataDir')" required>
            <div class="session-tool-settings__row">
              <el-input v-model="selectedCustom.dir" clearable placeholder="~/.my-cli/sessions" />
              <el-button :icon="FolderOpened" @click="browseDirectory(selectedCustom, selectedCustom.name)">
                {{ t("sessions.toolSettings.browse") }}
              </el-button>
            </div>
          </el-form-item>
          <div class="session-tool-settings__grid">
            <el-form-item :label="t('sessions.toolSettings.format')">
              <el-radio-group v-model="selectedCustom.format">
                <el-radio-button value="jsonl">{{ t("sessions.toolSettings.formatJsonl") }}</el-radio-button>
                <el-radio-button value="json">{{ t("sessions.toolSettings.formatJson") }}</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item :label="t('sessions.toolSettings.glob')">
              <el-input v-model="selectedCustom.glob" clearable :placeholder="defaultCustomGlob(selectedCustom.format)" />
            </el-form-item>
          </div>
          <p class="session-tool-settings__hint">
            {{ selectedCustom.format === "json" ? t("sessions.toolSettings.formatJsonHint") : t("sessions.toolSettings.formatJsonlHint") }}
            {{ t("sessions.toolSettings.globHint") }}
          </p>

          <el-form-item :label="t('sessions.toolSettings.resumeTemplate')">
            <el-input v-model="selectedCustom.resumeTemplate" clearable placeholder="my-cli --resume {id}" />
          </el-form-item>
          <el-form-item :label="t('sessions.toolSettings.newTemplate')">
            <el-input v-model="selectedCustom.newTemplate" clearable placeholder="my-cli" />
            <p class="session-tool-settings__hint">{{ t("sessions.toolSettings.customTemplateHint") }}</p>
          </el-form-item>

          <el-collapse class="session-tool-settings__advanced">
            <el-collapse-item :title="t('sessions.toolSettings.fields')" name="fields">
              <p class="session-tool-settings__hint">{{ t("sessions.toolSettings.fieldsHint") }}</p>
              <div class="session-tool-settings__grid">
                <el-form-item v-for="key in CUSTOM_SESSION_FIELD_KEYS" :key="key" :label="t(FIELD_LABEL_KEYS[key])">
                  <el-input v-model="selectedCustom.fields[key]" clearable :placeholder="DEFAULT_CUSTOM_FIELDS[key]" />
                </el-form-item>
                <el-form-item :label="t('sessions.toolSettings.userRoles')">
                  <el-input v-model="selectedCustom.userRoles" clearable :placeholder="DEFAULT_CUSTOM_USER_ROLES" />
                </el-form-item>
                <el-form-item :label="t('sessions.toolSettings.assistantRoles')">
                  <el-input
                    v-model="selectedCustom.assistantRoles"
                    clearable
                    :placeholder="DEFAULT_CUSTOM_ASSISTANT_ROLES"
                  />
                </el-form-item>
              </div>
              <p class="session-tool-settings__hint">{{ t("sessions.toolSettings.rolesHint") }}</p>
            </el-collapse-item>
          </el-collapse>
        </el-form>

        <p v-else class="session-tool-settings__hint">{{ t("sessions.toolSettings.selectTool") }}</p>
      </el-scrollbar>
    </div>

    <template #footer>
      <el-button @click="close">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :loading="saving" :disabled="!draft" @click="save">
        {{ t("common.save") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.session-tool-settings__body {
  display: grid;
  min-height: min(420px, 50vh);
  max-height: min(64vh, 640px);
  grid-template-columns: 260px minmax(0, 1fr);
  /* 行轨道必须可收缩，否则 auto 行会按内容撑开，右侧滚动失效并溢出弹窗 */
  grid-template-rows: minmax(0, 1fr);
  gap: 16px;
}
.session-tool-settings__list {
  display: flex;
  min-height: 0;
  flex-direction: column;
  gap: 10px;
  border-right: 1px solid var(--app-border);
  padding-right: 12px;
}
.session-tool-settings__list-scroll {
  height: auto;
  min-height: 0;
  flex: 1;
}
.session-tool-settings__list-scroll :deep(ul) {
  margin: 0;
  padding: 0;
  list-style: none;
}
.session-tool-settings__item {
  display: flex;
  align-items: center;
  gap: 4px;
  border-radius: 6px;
  padding: 2px 4px;
}
.session-tool-settings__item:hover {
  background: var(--app-muted);
}
.session-tool-settings__item.is-active {
  background: var(--app-accent-soft);
}
.session-tool-settings__item.is-disabled .session-tool-settings__item-main {
  opacity: 0.55;
}
.session-tool-settings__item-main {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 8px;
  border: 0;
  background: transparent;
  color: var(--app-text);
  cursor: pointer;
  padding: 6px 2px;
  text-align: left;
}
.session-tool-settings__item-main small {
  flex: none;
  color: var(--app-text-muted);
  font-size: 11px;
}
.session-tool-settings__item-label {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.session-tool-settings__mark {
  display: inline-grid;
  width: 24px;
  height: 20px;
  flex: none;
  place-items: center;
  border: 1px solid var(--app-border);
  border-radius: 5px;
  background: var(--app-muted);
  font-size: 10px;
  font-weight: 700;
}
.session-tool-settings__item-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 0;
}
.session-tool-settings__item-actions .el-button {
  margin: 0;
  padding: 4px;
}
.session-tool-settings__item-actions .el-switch {
  margin-left: 4px;
}
.session-tool-settings__add {
  width: 100%;
}
.session-tool-settings__detail {
  min-width: 0;
}
.session-tool-settings__detail :deep(.session-tool-settings__detail-view) {
  padding-right: 12px;
}
.session-tool-settings__detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 12px;
}
.session-tool-settings__row {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
}
.session-tool-settings__row.is-compact {
  width: auto;
  gap: 0;
}
.session-tool-settings__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 12px;
}
.session-tool-settings__hint {
  margin: 6px 0 0;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.session-tool-settings__advanced {
  margin-top: 8px;
}
</style>
