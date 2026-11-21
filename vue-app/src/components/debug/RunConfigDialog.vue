<script setup lang="ts">
/**
 * Run configurations of one project: the list (new from a debugger's
 * snippet, copy, delete) and an editor for the selected configuration whose
 * fields come from the debugger's configurationSchema. Saving only changes
 * MinCode's own store; nothing is written into the project. Desktop only
 * (opened from the run toolbar).
 */
import { computed, onMounted, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { CopyDocument, Delete, Plus } from "@element-plus/icons-vue";
import {
  applyConfigFields,
  configFormFields,
  draftsFor,
  remoteServerArgs,
  switchRequestFields,
  validateConfigField,
  validateConfigFields,
  type ConfigFieldDrafts,
  type ConfigFormField,
} from "@/domain/debug/config-form";
import { duplicateConfiguration, newConfigurationFromSnippet } from "@/domain/debug/configurations";
import type { DebugConfigurationSnippet, DebugRequestKind, DebuggerInfo, JsonSchemaObject, RunConfiguration } from "@/domain/debug/types";
import { resolveLocalizedTitle } from "@/domain/i18n/localized-title";
import { useVueI18n } from "@/i18n/messages";
import { listDebuggers } from "@/services/debug-service";
import { copyText } from "@/services/desktop-bridge";
import { useDebugConfigsStore } from "@/stores/debug-configs";

const props = defineProps<{
  modelValue: boolean;
  projectRoot: string;
  /** edit: open the selected configuration; new: start with the new-configuration form. */
  mode: "edit" | "new";
}>();

const emit = defineEmits<{
  "update:modelValue": [visible: boolean];
}>();

const { t, te, localeChain } = useVueI18n();
const configs = useDebugConfigsStore();

const REQUESTS: DebugRequestKind[] = ["launch", "attach"];

const state = computed(() => configs.stateFor(props.projectRoot));

// ---- debuggers ----

const debuggers = ref<DebuggerInfo[]>([]);
const loadingDebuggers = ref(true);
const loadError = ref("");

async function loadDebuggers() {
  loadingDebuggers.value = true;
  loadError.value = "";
  try {
    debuggers.value = await listDebuggers();
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : String(error);
  } finally {
    loadingDebuggers.value = false;
  }
}

function debuggerKey(item: Pick<DebuggerInfo, "pluginId" | "type">): string {
  return `${item.pluginId}\u0000${item.type}`;
}

function debuggerTitle(item: DebuggerInfo): string {
  return resolveLocalizedTitle(item, localeChain.value);
}

function snippetLabel(snippet: DebugConfigurationSnippet): string {
  return resolveLocalizedTitle({ title: snippet.label, titleLocalized: snippet.labelLocalized }, localeChain.value);
}

function debuggerFor(cfg: Pick<RunConfiguration, "pluginId" | "type">): DebuggerInfo | undefined {
  const key = debuggerKey(cfg);
  return debuggers.value.find((item) => debuggerKey(item) === key);
}

function schemaFor(item: DebuggerInfo | undefined, request: DebugRequestKind): JsonSchemaObject | undefined {
  return item?.configurationSchema?.[request];
}

function message(key: string): string {
  return te(key) ? t(key) : key;
}

// ---- the configuration being edited ----

const pane = ref<"edit" | "new">(props.mode);
const editingId = ref<string | null>(null);
const draftName = ref("");
const draftRequest = ref<DebugRequestKind>("launch");
/** The fields the drafts apply to: the stored ones, less what a request switch dropped. */
const baseFields = ref<Record<string, unknown>>({});
const formFields = ref<ConfigFormField[]>([]);
const drafts = ref<ConfigFieldDrafts>({});
const errors = ref<Record<string, string>>({});
const savedSnapshot = ref("");
/** Fields only the other request describes, kept while switching launch/attach until a save or another configuration. */
const requestStash = ref<Record<string, unknown>>({});
/** Bumped to re-render the list when a selection was refused, so it highlights the edited configuration again. */
const menuNonce = ref(0);
const menuKey = computed(() => `${pane.value}:${editingId.value ?? ""}:${menuNonce.value}`);

const editing = computed(() => state.value.configs.find((cfg) => cfg.id === editingId.value) ?? null);
const editingDebugger = computed(() => (editing.value ? debuggerFor(editing.value) : undefined));
const requestOptions = computed(() => {
  const schema = editingDebugger.value?.configurationSchema;
  const offered = REQUESTS.filter((request) => schema?.[request]);
  return offered.includes(draftRequest.value) ? offered : [...offered, draftRequest.value];
});

function snapshot(): string {
  return JSON.stringify({ name: draftName.value, request: draftRequest.value, drafts: drafts.value, base: baseFields.value });
}

const dirty = computed(() => pane.value === "edit" && editing.value !== null && snapshot() !== savedSnapshot.value);

function rebuildForm() {
  formFields.value = configFormFields(schemaFor(editingDebugger.value, draftRequest.value), baseFields.value);
  drafts.value = draftsFor(formFields.value, baseFields.value);
  errors.value = {};
}

function edit(cfg: RunConfiguration | null) {
  editingId.value = cfg?.id ?? null;
  pane.value = cfg ? "edit" : "new";
  if (!cfg) return;
  draftName.value = cfg.name;
  draftRequest.value = cfg.request;
  baseFields.value = JSON.parse(JSON.stringify(cfg.fields)) as Record<string, unknown>;
  requestStash.value = {};
  rebuildForm();
  savedSnapshot.value = snapshot();
}

/**
 * Switching launch/attach carries the edits over. Fields only the old request
 * describes are kept aside (and come back when switching back) but are not
 * saved with the new request. With an invalid field the switch is refused.
 */
function changeRequest(next: DebugRequestKind) {
  if (next === draftRequest.value) return;
  const invalid = validateConfigFields(formFields.value, drafts.value);
  if (Object.keys(invalid).length) {
    errors.value = invalid;
    return;
  }
  const current = applyConfigFields(formFields.value, drafts.value, baseFields.value);
  const debugger_ = editingDebugger.value;
  const switched = switchRequestFields(current, schemaFor(debugger_, draftRequest.value), schemaFor(debugger_, next), requestStash.value);
  draftRequest.value = next;
  baseFields.value = switched.fields;
  requestStash.value = switched.stash;
  rebuildForm();
}

function setDraft(key: string, value: string | boolean) {
  drafts.value = { ...drafts.value, [key]: value };
  if (errors.value[key]) {
    const { [key]: _cleared, ...rest } = errors.value;
    errors.value = rest;
  }
}

function setName(value: string) {
  draftName.value = value;
  if (errors.value.name) {
    const { name: _cleared, ...rest } = errors.value;
    errors.value = rest;
  }
}

function textDraft(key: string): string {
  const value = drafts.value[key];
  return typeof value === "string" ? value : "";
}

/** JSON (and other) fields are checked when they lose focus. */
function validateOne(field: ConfigFormField) {
  const error = validateConfigField(field, drafts.value[field.key]);
  const { [field.key]: _old, ...rest } = errors.value;
  errors.value = error ? { ...rest, [field.key]: error } : rest;
}

function save(): boolean {
  const cfg = editing.value;
  if (!cfg) return false;
  const next = validateConfigFields(formFields.value, drafts.value);
  if (!draftName.value.trim()) next.name = "debug.configDialog.required";
  errors.value = next;
  if (Object.keys(next).length) return false;
  const fields = applyConfigFields(formFields.value, drafts.value, baseFields.value);
  configs.upsertConfig(props.projectRoot, { ...cfg, name: draftName.value.trim(), request: draftRequest.value, fields });
  draftName.value = draftName.value.trim();
  baseFields.value = fields;
  requestStash.value = {};
  savedSnapshot.value = snapshot();
  return true;
}

/** True when there are no unsaved changes or the user agreed to discard them. */
async function confirmDiscard(): Promise<boolean> {
  if (!dirty.value) return true;
  try {
    await ElMessageBox.confirm(t("debug.configDialog.discardMessage"), t("debug.configDialog.discardTitle"), {
      confirmButtonText: t("debug.configDialog.discard"),
      cancelButtonText: t("common.cancel"),
      type: "warning",
    });
    return true;
  } catch {
    return false;
  }
}

async function selectFromList(id: string) {
  if (id === editingId.value && pane.value === "edit") return;
  // The menu highlights the clicked item before this runs; re-render it when the switch is refused.
  if (!(await confirmDiscard())) {
    menuNonce.value += 1;
    return;
  }
  edit(state.value.configs.find((cfg) => cfg.id === id) ?? null);
}

// ---- list actions ----

async function startNew() {
  if (!(await confirmDiscard())) return;
  pane.value = "new";
  newSnippetIndex.value = null;
  newName.value = "";
}

async function copySelected() {
  const cfg = editing.value;
  if (!cfg || !(await confirmDiscard())) return;
  const copy = duplicateConfiguration(cfg, t("debug.configDialog.copyName", { name: cfg.name }));
  configs.upsertConfig(props.projectRoot, copy);
  edit(copy);
}

async function deleteSelected() {
  const cfg = editing.value;
  if (!cfg) return;
  try {
    await ElMessageBox.confirm(t("debug.configDialog.deleteMessage", { name: cfg.name }), t("debug.configDialog.deleteTitle"), {
      confirmButtonText: t("debug.configDialog.delete"),
      cancelButtonText: t("common.cancel"),
      type: "warning",
    });
  } catch {
    return;
  }
  configs.removeConfig(props.projectRoot, cfg.id);
  edit(state.value.configs[0] ?? null);
}

// ---- new configuration ----

const newDebuggerKey = ref("");
const newSnippetIndex = ref<number | null>(null);
const newName = ref("");
const newDebugger = computed(() => debuggers.value.find((item) => debuggerKey(item) === newDebuggerKey.value));
const newSnippets = computed(() => newDebugger.value?.configurationSnippets ?? []);

watch(debuggers, (list) => {
  if (!list.some((item) => debuggerKey(item) === newDebuggerKey.value)) newDebuggerKey.value = list[0] ? debuggerKey(list[0]) : "";
});
watch(newDebuggerKey, () => {
  newSnippetIndex.value = null;
});

function create() {
  const item = newDebugger.value;
  const snippet = newSnippetIndex.value === null ? undefined : newSnippets.value[newSnippetIndex.value];
  if (!item || !snippet) return;
  const cfg = newConfigurationFromSnippet(item, snippet, newName.value.trim() || snippetLabel(snippet));
  configs.upsertConfig(props.projectRoot, cfg);
  configs.selectConfig(props.projectRoot, cfg.id);
  newName.value = "";
  newSnippetIndex.value = null;
  edit(cfg);
}

function cancelNew() {
  const cfg = state.value.configs.find((item) => item.id === state.value.selectedConfigId) ?? state.value.configs[0] ?? null;
  if (cfg) edit(cfg);
}

// ---- remote attach ----

const remote = computed(() => (draftRequest.value === "attach" ? editingDebugger.value?.remote : undefined));
const remoteArgs = computed(() => {
  // Plugins may omit argsTemplate even though the type requires it.
  const template = remote.value?.argsTemplate;
  if (typeof template !== "string" || !template.trim()) return "";
  // An empty or invalid port shows the schema default, else keeps ${port}.
  const field = formFields.value.find((item) => item.key === "port" && (item.kind === "integer" || item.kind === "number"));
  const draft = drafts.value.port;
  const usable = (value: unknown): value is string => typeof value === "string" && value.trim() !== "" && !!field && !validateConfigField(field, value);
  const fallback = field?.default === undefined ? undefined : String(field.default);
  return remoteServerArgs(template, usable(draft) ? draft : usable(fallback) ? fallback : undefined);
});

async function copyRemoteArgs() {
  try {
    await copyText(remoteArgs.value);
    ElMessage.success({ message: t("debug.configDialog.copied"), grouping: true });
  } catch {
    ElMessage.error({ message: t("debug.configDialog.copyFailed"), grouping: true });
  }
}

// ---- dialog ----

async function close() {
  if (await confirmDiscard()) emit("update:modelValue", false);
}

function beforeClose(done: () => void) {
  void confirmDiscard().then((discard) => {
    if (discard) done();
  });
}

/** OK: saves pending changes (when valid) and closes; with nothing changed it just closes. */
function saveAndClose() {
  if (!dirty.value || save()) emit("update:modelValue", false);
}

function placeholder(field: ConfigFormField): string {
  if (field.kind === "stringList") return t("debug.configDialog.listPlaceholder");
  if (field.kind === "stringMap") return t("debug.configDialog.mapPlaceholder");
  if (field.default === undefined || field.kind === "boolean") return "";
  const value = typeof field.default === "string" ? field.default : JSON.stringify(field.default);
  return t("debug.configDialog.defaultValue", { value });
}

onMounted(async () => {
  await Promise.all([configs.load().catch(() => undefined), loadDebuggers()]);
  const list = state.value.configs;
  const selected = list.find((cfg) => cfg.id === state.value.selectedConfigId) ?? list[0] ?? null;
  if (props.mode === "edit" && selected) edit(selected);
  else pane.value = "new";
});

// The schema arrives with the debugger list; a retry rebuilds the open form.
watch(debuggers, () => {
  if (pane.value === "edit" && editing.value && !dirty.value) edit(editing.value);
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('debug.configDialog.title')"
    width="860px"
    append-to-body
    class="run-config-dialog"
    :before-close="beforeClose"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="run-config-dialog__body">
      <aside class="run-config-dialog__list" :aria-label="t('debug.configDialog.listLabel')">
        <div class="run-config-dialog__list-toolbar">
          <el-tooltip :content="t('debug.configDialog.add')" placement="bottom">
            <el-button text native-type="button" data-action="add" :aria-label="t('debug.configDialog.add')" @click="startNew">
              <el-icon><Plus /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('debug.configDialog.copy')" placement="bottom">
            <el-button
              text
              native-type="button"
              data-action="copy"
              :aria-label="t('debug.configDialog.copy')"
              :disabled="pane !== 'edit' || !editing"
              @click="copySelected"
            >
              <el-icon><CopyDocument /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('debug.configDialog.delete')" placement="bottom">
            <el-button
              text
              native-type="button"
              data-action="delete"
              :aria-label="t('debug.configDialog.delete')"
              :disabled="pane !== 'edit' || !editing"
              @click="deleteSelected"
            >
              <el-icon><Delete /></el-icon>
            </el-button>
          </el-tooltip>
        </div>
        <el-scrollbar class="run-config-dialog__list-scroll">
          <el-menu
            v-if="state.configs.length"
            :key="menuKey"
            class="run-config-dialog__menu"
            :default-active="pane === 'edit' ? (editingId ?? undefined) : undefined"
            @select="selectFromList"
          >
            <el-menu-item v-for="cfg in state.configs" :key="cfg.id" :index="cfg.id" :title="cfg.name">
              <span class="run-config-dialog__menu-name">{{ cfg.name }}</span>
              <span class="run-config-dialog__menu-type">{{ cfg.type }}</span>
            </el-menu-item>
          </el-menu>
          <p v-else class="run-config-dialog__hint">{{ t("debug.run.none") }}</p>
        </el-scrollbar>
      </aside>

      <el-scrollbar class="run-config-dialog__editor" view-class="run-config-dialog__editor-content">
        <el-alert
          v-if="loadError"
          type="error"
          :closable="false"
          show-icon
          class="run-config-dialog__alert"
          :title="t('debug.configDialog.loadFailed', { error: loadError })"
        >
          <el-button size="small" native-type="button" data-action="retry" @click="loadDebuggers">{{ t("debug.configDialog.retry") }}</el-button>
        </el-alert>
        <p v-if="loadingDebuggers" class="run-config-dialog__hint" role="status">{{ t("debug.configDialog.loading") }}</p>

        <!-- New configuration: pick a debugger, then one of its snippets. -->
        <template v-else-if="pane === 'new'">
          <el-empty v-if="!loadError && !debuggers.length" :image-size="56" :description="t('debug.configDialog.noDebuggers')" />
          <el-form v-else-if="debuggers.length" label-position="top" class="run-config-dialog__form" @submit.prevent>
            <el-form-item :label="t('debug.configDialog.debugger')" data-field="debugger">
              <el-select v-model="newDebuggerKey" :aria-label="t('debug.configDialog.debugger')">
                <el-option v-for="item in debuggers" :key="debuggerKey(item)" :value="debuggerKey(item)" :label="debuggerTitle(item)" />
              </el-select>
            </el-form-item>
            <el-form-item :label="t('debug.configDialog.snippet')" data-field="snippet">
              <el-radio-group
                v-if="newSnippets.length"
                v-model="newSnippetIndex"
                class="run-config-dialog__snippets"
                :aria-label="t('debug.configDialog.snippet')"
              >
                <el-radio v-for="(snippet, index) in newSnippets" :key="index" :value="index" border :data-snippet="index">
                  {{ snippetLabel(snippet) }}
                  <span class="run-config-dialog__snippet-request">{{ t(`debug.configDialog.request.${snippet.request}`) }}</span>
                </el-radio>
              </el-radio-group>
              <p v-else class="run-config-dialog__hint">{{ t("debug.configDialog.noSnippets") }}</p>
            </el-form-item>
            <el-form-item :label="t('debug.configDialog.name')" data-field="new-name">
              <el-input
                v-model="newName"
                :placeholder="newSnippetIndex === null ? '' : snippetLabel(newSnippets[newSnippetIndex]!)"
                @keydown.enter.prevent="create"
              />
            </el-form-item>
            <div class="run-config-dialog__actions">
              <el-button v-if="state.configs.length" native-type="button" @click="cancelNew">{{ t("common.cancel") }}</el-button>
              <el-button type="primary" native-type="button" data-action="create" :disabled="newSnippetIndex === null" @click="create">
                {{ t("debug.configDialog.create") }}
              </el-button>
            </div>
          </el-form>
        </template>

        <!-- Edit the selected configuration. -->
        <el-form v-else-if="editing" label-position="top" class="run-config-dialog__form" @submit.prevent>
          <el-alert
            v-if="!loadError && !editingDebugger"
            type="warning"
            :closable="false"
            show-icon
            class="run-config-dialog__alert"
            :title="t('debug.configDialog.debuggerMissing', { type: editing.type, plugin: editing.pluginId })"
          />
          <p v-else-if="editingDebugger" class="run-config-dialog__debugger">{{ debuggerTitle(editingDebugger) }}</p>
          <el-form-item :label="t('debug.configDialog.name')" required :error="errors.name ? message(errors.name) : ''" data-field="name">
            <el-input :model-value="draftName" @update:model-value="setName(String($event))" />
          </el-form-item>
          <el-form-item :label="t('debug.configDialog.requestLabel')" data-field="request">
            <el-radio-group :model-value="draftRequest" @update:model-value="changeRequest($event as DebugRequestKind)">
              <el-radio-button v-for="request in requestOptions" :key="request" :value="request" :data-request="request">
                {{ t(`debug.configDialog.request.${request}`) }}
              </el-radio-button>
            </el-radio-group>
          </el-form-item>

          <el-form-item
            v-for="field in formFields"
            :key="`${draftRequest}:${field.key}`"
            :label="field.label"
            :required="field.required"
            :error="errors[field.key] ? message(errors[field.key]!) : ''"
            :data-field="field.key"
            :data-kind="field.kind"
          >
            <el-switch
              v-if="field.kind === 'boolean'"
              :model-value="drafts[field.key] === true"
              :aria-label="field.label"
              @update:model-value="setDraft(field.key, Boolean($event))"
            />
            <el-select
              v-else-if="field.kind === 'enum'"
              :model-value="textDraft(field.key)"
              clearable
              :placeholder="placeholder(field)"
              :aria-label="field.label"
              @update:model-value="setDraft(field.key, String($event ?? ''))"
            >
              <el-option v-for="option in field.options" :key="option" :value="option" :label="option" />
            </el-select>
            <el-input
              v-else-if="field.kind === 'stringList' || field.kind === 'stringMap' || field.kind === 'json'"
              type="textarea"
              :class="{ 'run-config-dialog__code': field.kind !== 'stringList' }"
              :autosize="{ minRows: 2, maxRows: 8 }"
              :model-value="textDraft(field.key)"
              :placeholder="placeholder(field)"
              :aria-label="field.label"
              spellcheck="false"
              @update:model-value="setDraft(field.key, String($event))"
              @blur="validateOne(field)"
            />
            <el-input
              v-else
              :model-value="textDraft(field.key)"
              :placeholder="placeholder(field)"
              :inputmode="field.kind === 'string' ? undefined : field.kind === 'integer' ? 'numeric' : 'decimal'"
              :aria-label="field.label"
              @update:model-value="setDraft(field.key, String($event))"
              @blur="validateOne(field)"
            />
            <p v-if="field.description" class="run-config-dialog__description">{{ field.description }}</p>
          </el-form-item>

          <div v-if="remote" class="run-config-dialog__remote">
            <el-form-item v-if="remoteArgs" :label="t('debug.configDialog.serverArgs')" data-field="remote-args">
              <div class="run-config-dialog__remote-row">
                <el-input :model-value="remoteArgs" readonly class="run-config-dialog__code" :aria-label="t('debug.configDialog.serverArgs')" />
                <el-button native-type="button" data-action="copy-remote-args" @click="copyRemoteArgs">{{ t("debug.configDialog.copyArgs") }}</el-button>
              </div>
              <p class="run-config-dialog__description">{{ t("debug.configDialog.serverArgsHint") }}</p>
            </el-form-item>
            <p class="run-config-dialog__hint">{{ t("debug.configDialog.sshLater") }}</p>
          </div>
        </el-form>
      </el-scrollbar>
    </div>

    <template #footer>
      <el-button native-type="button" @click="close">{{ t("common.close") }}</el-button>
      <el-button native-type="button" data-action="save" :disabled="!dirty" @click="save">{{ t("debug.configDialog.save") }}</el-button>
      <el-button type="primary" native-type="button" data-action="save-close" :disabled="pane !== 'edit' || !editing" @click="saveAndClose">
        {{ t("debug.configDialog.ok") }}
      </el-button>
    </template>
  </el-dialog>
</template>
