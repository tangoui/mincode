<script setup lang="ts">
// Host confirmation for ai.config (spec §7): a plugin page proposed AI
// settings and picked what to write; this dialog, owned by the host frame and
// outside the plugin's sandbox, lists every item before anything is saved.
// Plain text only (rows are already made visible by domain/plugin-ui/ai-config);
// secret values are never shown, connectors show their full command line or URL.
// Required connector values the plugin left empty are typed in here: they go
// straight to the host's write and never back to the plugin; the inputs are
// cleared as soon as the dialog closes.
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElAlert, ElButton, ElCollapse, ElCollapseItem, ElDialog, ElInput, ElScrollbar, ElTag } from "element-plus";
import {
  AI_CONFIG_SKILL_EXCERPT_LENGTH,
  isAiConfigFieldName,
  visibleReviewText,
  type AiConfigReviewPrompt,
  type AiConfigReviewRow,
} from "@/domain/plugin-ui/ai-config";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  pluginName: string;
  rows: AiConfigReviewRow[];
}>();
/** Per sync key, "env:NAME" / "header:NAME" to the value typed in. */
type AiConfigPromptValues = Record<string, Record<string, string>>;
const emit = defineEmits<{
  confirm: [values: AiConfigPromptValues];
  cancel: [];
}>();

const { t } = useVueI18n();

const visible = computed({
  get: () => true,
  set: (next: boolean) => {
    if (!next) cancel();
  },
});

// Prompts this device has no value for yet; filled ones keep the saved value and are not asked again.
function askedPrompts(row: AiConfigReviewRow): AiConfigReviewPrompt[] {
  return row.kind === "mcp" ? row.prompts.filter((prompt) => !prompt.filled) : [];
}
function promptField(prompt: AiConfigReviewPrompt) {
  return `${prompt.target}:${prompt.name}`;
}
// JSON keeps the (sync key, field) pair unambiguous whatever characters either holds.
function inputId(row: AiConfigReviewRow, prompt: AiConfigReviewPrompt) {
  return JSON.stringify([row.key, promptField(prompt)]);
}
function promptLabel(prompt: AiConfigReviewPrompt) {
  return prompt.label || prompt.name;
}

const inputs = ref<Record<string, string>>({});
const promptsComplete = computed(() =>
  props.rows.every((row) => askedPrompts(row).every((prompt) => (inputs.value[inputId(row, prompt)] ?? "").trim() !== "")),
);

function clearInputs() {
  inputs.value = {};
}
watch(() => props.rows, clearInputs);
onBeforeUnmount(clearInputs);

function setInput(row: AiConfigReviewRow, prompt: AiConfigReviewPrompt, value: string) {
  inputs.value = { ...inputs.value, [inputId(row, prompt)]: value };
}

function confirm() {
  if (!promptsComplete.value) return;
  const values: AiConfigPromptValues = {};
  for (const row of props.rows) {
    for (const prompt of askedPrompts(row)) {
      (values[row.key] ??= {})[promptField(prompt)] = inputs.value[inputId(row, prompt)] ?? "";
    }
  }
  clearInputs();
  emit("confirm", values);
}

function cancel() {
  clearInputs();
  emit("cancel");
}
// The plugin's display name comes from its manifest: make hidden characters visible here too.
const title = computed(() => t("plugins.aiConfig.title", { plugin: visibleReviewText(props.pluginName) }));
const addsConnector = computed(() => props.rows.some((row) => row.kind === "mcp" && row.action === "add"));

function keyList(keys: readonly string[]) {
  return keys.length ? keys.join(", ") : t("plugins.aiConfig.noKeys");
}

function fieldList(names: readonly string[]) {
  return names.map((name) => (isAiConfigFieldName(name) ? t(`plugins.aiConfig.field.${name}`) : name)).join(", ");
}
</script>

<template>
  <ElDialog
    v-model="visible"
    :title="title"
    width="560px"
    :close-on-click-modal="false"
    append-to-body
    class="app-plugin-ai-config"
  >
    <div class="app-plugin-ai-config__body">
      <p class="app-plugin-ai-config__intro">{{ t("plugins.aiConfig.intro", { count: rows.length }) }}</p>
      <ElAlert v-if="addsConnector" type="warning" :closable="false" show-icon :title="t('plugins.aiConfig.mcpDisabled')" />
      <ElScrollbar max-height="50vh">
        <ul class="app-plugin-ai-config__list" :aria-label="t('plugins.aiConfig.listLabel')">
          <li v-for="row in rows" :key="row.key" class="app-plugin-ai-config__item">
            <div class="app-plugin-ai-config__head">
              <ElTag size="small" type="info" disable-transitions>{{ t(`plugins.aiConfig.kind.${row.kind}`) }}</ElTag>
              <ElTag size="small" :type="row.action === 'add' ? 'success' : 'warning'" disable-transitions>
                {{ row.secretsOnly ? t("plugins.aiConfig.action.fill") : t(`plugins.aiConfig.action.${row.action}`) }}
              </ElTag>
              <strong class="app-plugin-ai-config__name">{{ row.name }}</strong>
            </div>
            <p class="app-plugin-ai-config__meta">
              <span>{{ t("plugins.aiConfig.targetName", { name: row.targetName }) }}</span>
              <span>{{ t("plugins.aiConfig.source", { app: row.sourceApp }) }}</span>
            </p>
            <p v-if="row.duplicateName" class="app-plugin-ai-config__note">{{ t("plugins.aiConfig.duplicate") }}</p>
            <p v-if="row.conflict" class="app-plugin-ai-config__note is-warning">{{ t("plugins.aiConfig.conflict") }}</p>
            <p v-if="row.secretsOnly" class="app-plugin-ai-config__note">{{ t(`plugins.aiConfig.secretsOnly.${row.kind}`) }}</p>
            <dl v-else class="app-plugin-ai-config__spec">
              <template v-if="row.changedFields.length">
                <dt>{{ t("plugins.aiConfig.changedFields") }}</dt>
                <dd class="app-plugin-ai-config__changed">{{ fieldList(row.changedFields) }}</dd>
              </template>
              <template v-if="row.kind === 'profile'">
                <dt>{{ t("plugins.aiConfig.baseUrl") }}</dt>
                <dd><code dir="ltr">{{ row.baseUrl }}</code></dd>
                <dt>{{ t("plugins.aiConfig.model") }}</dt>
                <dd><code dir="ltr">{{ row.model }}</code></dd>
                <dt>{{ t("plugins.aiConfig.apiFormat") }}</dt>
                <dd><code dir="ltr">{{ row.apiFormat }}</code></dd>
                <template v-if="row.anthropicAuth">
                  <dt>{{ t("plugins.aiConfig.anthropicAuthLabel") }}</dt>
                  <dd><code dir="ltr">{{ t(`plugins.aiConfig.anthropicAuth.${row.anthropicAuth}`) }}</code></dd>
                </template>
                <dt>{{ t("plugins.aiConfig.apiKeyLabel") }}</dt>
                <dd class="app-plugin-ai-config__secret">{{ t(`plugins.aiConfig.apiKey.${row.apiKey}`) }}</dd>
              </template>
              <template v-else-if="row.kind === 'mcp'">
                <template v-if="row.transport === 'stdio'">
                  <dt>{{ t("plugins.aiConfig.command") }}</dt>
                  <dd><code dir="ltr">{{ row.commandLine }}</code></dd>
                </template>
                <template v-else>
                  <dt>{{ t("plugins.aiConfig.url") }}</dt>
                  <dd><code dir="ltr">{{ row.url }}</code></dd>
                </template>
                <dt>{{ t("plugins.aiConfig.envKeys") }}</dt>
                <dd><code dir="ltr">{{ keyList(row.envKeys) }}</code></dd>
                <dt>{{ t("plugins.aiConfig.headerKeys") }}</dt>
                <dd><code dir="ltr">{{ keyList(row.headerKeys) }}</code></dd>
                <template v-if="row.secretValues !== 'none'">
                  <dt>{{ t("plugins.aiConfig.secretValuesLabel") }}</dt>
                  <dd class="app-plugin-ai-config__secret">{{ t(`plugins.aiConfig.secretValues.${row.secretValues}`) }}</dd>
                </template>
              </template>
              <template v-else>
                <template v-if="row.description">
                  <dt>{{ t("plugins.aiConfig.description") }}</dt>
                  <dd>{{ row.description }}</dd>
                </template>
                <dt>{{ t("plugins.aiConfig.excerpt", { count: AI_CONFIG_SKILL_EXCERPT_LENGTH }) }}</dt>
                <dd><code class="app-plugin-ai-config__excerpt">{{ row.excerpt }}{{ row.truncated ? "…" : "" }}</code></dd>
                <dd v-if="row.truncated">
                  <ElCollapse class="app-plugin-ai-config__full">
                    <ElCollapseItem :title="t('plugins.aiConfig.fullText')" :name="row.key">
                      <ElScrollbar max-height="240px">
                        <code class="app-plugin-ai-config__full-text">{{ row.fullText }}</code>
                      </ElScrollbar>
                    </ElCollapseItem>
                  </ElCollapse>
                </dd>
                <dd v-if="row.extraFiles" class="app-plugin-ai-config__note">{{ t("plugins.aiConfig.extraFiles") }}</dd>
              </template>
            </dl>
            <p v-if="row.kind === 'mcp' && row.auth === 'oauth'" class="app-plugin-ai-config__note is-warning">
              {{ t("plugins.aiConfig.oauthAfterInstall") }}
            </p>
            <div v-if="askedPrompts(row).length" class="app-plugin-ai-config__prompts">
              <p class="app-plugin-ai-config__prompts-title">{{ t("plugins.aiConfig.promptsTitle") }}</p>
              <div v-for="prompt in askedPrompts(row)" :key="promptField(prompt)" class="app-plugin-ai-config__prompt">
                <span class="app-plugin-ai-config__prompt-label">
                  {{ promptLabel(prompt) }}
                  <code v-if="prompt.label" dir="ltr">{{ t(`plugins.aiConfig.promptTarget.${prompt.target}`, { name: prompt.name }) }}</code>
                </span>
                <ElInput
                  :model-value="inputs[inputId(row, prompt)] ?? ''"
                  :type="prompt.secret ? 'password' : 'text'"
                  :show-password="prompt.secret"
                  :aria-label="t('plugins.aiConfig.promptInputLabel', { label: promptLabel(prompt), name: row.name })"
                  :placeholder="t('plugins.aiConfig.promptPlaceholder')"
                  autocomplete="off"
                  spellcheck="false"
                  size="small"
                  @update:model-value="setInput(row, prompt, $event)"
                />
              </div>
            </div>
          </li>
        </ul>
      </ElScrollbar>
    </div>
    <template #footer>
      <ElButton @click="cancel">{{ t("common.cancel") }}</ElButton>
      <ElButton type="primary" :disabled="!promptsComplete" @click="confirm">{{ t("plugins.aiConfig.write") }}</ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
.app-plugin-ai-config__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.app-plugin-ai-config__body p {
  margin: 0;
  overflow-wrap: anywhere;
}

.app-plugin-ai-config__intro {
  color: var(--app-text);
}

.app-plugin-ai-config__list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.app-plugin-ai-config__item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 10px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}

.app-plugin-ai-config__head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.app-plugin-ai-config__name {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--app-text);
}

.app-plugin-ai-config__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-plugin-ai-config__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-plugin-ai-config__note.is-warning {
  color: var(--el-color-warning);
}

.app-plugin-ai-config__spec {
  display: grid;
  gap: 2px;
  margin: 0;
}

.app-plugin-ai-config__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-plugin-ai-config__spec dd {
  margin: 0 0 4px;
  min-width: 0;
  overflow-wrap: anywhere;
}

.app-plugin-ai-config__spec code {
  display: block;
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-plugin-ai-config__secret {
  font-size: 12px;
  color: var(--app-text);
}

.app-plugin-ai-config__changed {
  font-size: 12px;
  color: var(--el-color-warning);
}

.app-plugin-ai-config__excerpt {
  max-height: 160px;
  overflow: auto;
}

.app-plugin-ai-config__full {
  border-top: none;
  border-bottom: none;
}

.app-plugin-ai-config__prompts {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-top: 6px;
  border-top: 1px solid var(--app-border);
}

.app-plugin-ai-config__prompts-title {
  color: var(--app-text);
  font-size: 12px;
}

.app-plugin-ai-config__prompt {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.app-plugin-ai-config__prompt-label {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 6px;
  color: var(--app-text-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.app-plugin-ai-config__prompt-label code {
  font-family: var(--app-font-mono, ui-monospace, monospace);
}

.app-plugin-ai-config__spec .app-plugin-ai-config__full-text {
  border: none;
  background: transparent;
}
</style>
