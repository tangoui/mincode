<script setup lang="ts">
// Preview of one plugin context provider (contributes.aiContextProviders).
// The user fetches the text, reads it here (plain text only) and decides
// whether to insert it as an attachment of the next message. Nothing is sent
// from this dialog, and the fetched text is never rendered as HTML/Markdown.
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import {
  AI_CONTEXT_PROVIDER_LIMITS,
  aiContextProviderPlaceholder,
  aiContextProviderTitle,
  buildPluginContextAttachmentText,
  type PluginAiContextProvider,
} from "@/domain/ai/plugin-context";
import { useVueI18n } from "@/i18n/messages";
import type { PluginContextResult } from "@/services/ai-context-provider-service";

const props = defineProps<{
  provider: PluginAiContextProvider;
  /** Characters still available in the AI context budget. */
  remaining: number;
}>();

const emit = defineEmits<{
  insert: [value: { text: string; truncated: boolean }];
  close: [];
}>();

const { t, localeChain, localeTag } = useVueI18n();
const visible = ref(true);
const argument = ref("");
const loading = ref(false);
const error = ref("");
const result = ref<PluginContextResult>();
const fetchedArgument = ref<string>();
let controller: AbortController | undefined;

const title = computed(() => aiContextProviderTitle(props.provider, localeChain.value));
const placeholder = computed(() => aiContextProviderPlaceholder(props.provider.argument, localeChain.value));
const budgetTooSmall = computed(() => props.remaining < AI_CONTEXT_PROVIDER_LIMITS.minInsertChars);
const argumentMissing = computed(() => Boolean(props.provider.argument?.required) && !argument.value.trim());
const wrapped = computed(() => {
  if (!result.value) return undefined;
  return buildPluginContextAttachmentText(props.provider, result.value.title || title.value, result.value.text, {
    ...(fetchedArgument.value ? { argument: fetchedArgument.value } : {}),
    budget: props.remaining,
  });
});
const canInsert = computed(() => Boolean(wrapped.value && result.value?.text) && !budgetTooSmall.value && !loading.value);

async function fetchContext() {
  if (budgetTooSmall.value || argumentMissing.value || loading.value) return;
  controller?.abort();
  controller = new AbortController();
  const signal = controller.signal;
  loading.value = true;
  error.value = "";
  result.value = undefined;
  const requested = props.provider.argument ? argument.value.trim() || undefined : undefined;
  const { fetchPluginContext } = await import("@/services/ai-context-provider-service");
  const outcome = await fetchPluginContext(props.provider, requested, signal, localeTag.value);
  if (signal.aborted) return;
  loading.value = false;
  if (outcome.ok) {
    result.value = outcome.result;
    fetchedArgument.value = requested;
    return;
  }
  const { code, detail } = outcome.error;
  if (code === "cancelled") return;
  // Plugin error text is untrusted; it is shown as plain text and bounded.
  // eslint-disable-next-line no-control-regex -- removing control characters is the point
  const safe = detail.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g, " ").slice(0, 200);
  if (code === "timeout") error.value = t("pluginContrib.aiContextProviders.timeout", { title: title.value, plugin: props.provider.pluginName });
  else if (code === "unavailable" || code === "not_found" || code === "forbidden") error.value = t("pluginContrib.aiContextProviders.unavailable");
  else error.value = t("pluginContrib.aiContextProviders.failed", { detail: safe });
}

function insert() {
  if (!canInsert.value || !wrapped.value) return;
  emit("insert", wrapped.value);
  visible.value = false;
}

onMounted(() => {
  if (!props.provider.argument) void fetchContext();
});

onBeforeUnmount(() => controller?.abort());
</script>

<template>
  <el-dialog
    v-model="visible"
    class="ai-plugin-context-dialog"
    :title="t('pluginContrib.aiContextProviders.dialogTitle', { title })"
    width="min(760px, 92vw)"
    append-to-body
    destroy-on-close
    @closed="emit('close')"
  >
    <div class="ai-plugin-context">
      <p class="ai-plugin-context__intro">
        {{ t("pluginContrib.aiContextProviders.intro", { plugin: provider.pluginName, id: provider.pluginId }) }}
      </p>
      <el-alert
        v-if="budgetTooSmall"
        type="warning"
        :closable="false"
        show-icon
        :title="t('pluginContrib.aiContextProviders.budgetTooSmall', { remaining })"
      />
      <form v-else-if="provider.argument" class="ai-plugin-context__argument" @submit.prevent="fetchContext">
        <el-input
          v-model="argument"
          :maxlength="AI_CONTEXT_PROVIDER_LIMITS.argumentMaxRunes"
          :placeholder="placeholder"
          :aria-label="t('pluginContrib.aiContextProviders.argument')"
          clearable
        />
        <el-button native-type="submit" :loading="loading" :disabled="argumentMissing">
          {{ t("pluginContrib.aiContextProviders.fetch") }}
        </el-button>
      </form>
      <small v-if="!budgetTooSmall && argumentMissing" class="ai-plugin-context__muted">
        {{ t("pluginContrib.aiContextProviders.argumentRequired") }}
      </small>
      <el-alert v-if="error" type="error" :closable="false" show-icon :title="error" />
      <div v-if="loading" class="ai-plugin-context__muted" role="status">{{ t("pluginContrib.aiContextProviders.fetching") }}</div>
      <template v-else-if="result">
        <p v-if="!result.text" class="ai-plugin-context__muted">{{ t("pluginContrib.aiContextProviders.previewEmpty") }}</p>
        <pre v-else class="ai-plugin-context__preview" tabindex="0">{{ result.text }}</pre>
        <small v-if="result.truncated || wrapped?.truncated" class="ai-plugin-context__muted">
          {{ t("pluginContrib.aiContextProviders.previewTruncated") }}
        </small>
        <small v-if="result.removedCharacters" class="ai-plugin-context__muted">
          {{ t("pluginContrib.aiContextProviders.previewRemoved", { count: result.removedCharacters }) }}
        </small>
        <small v-if="wrapped" class="ai-plugin-context__muted">
          {{ t("pluginContrib.aiContextProviders.budget", { used: wrapped.text.length, remaining }) }}
        </small>
      </template>
    </div>
    <template #footer>
      <el-button @click="visible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!canInsert" @click="insert">{{ t("pluginContrib.aiContextProviders.insert") }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.ai-plugin-context {
  display: grid;
  gap: 10px;
  min-width: 0;
}

.ai-plugin-context__intro {
  margin: 0;
  color: var(--el-text-color-regular);
}

.ai-plugin-context__argument {
  display: flex;
  gap: 8px;
  min-width: 0;
}

.ai-plugin-context__muted {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.ai-plugin-context__preview {
  max-height: min(50vh, 420px);
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
  font-family: var(--el-font-family-mono, ui-monospace, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
