<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { MagicStick } from "@element-plus/icons-vue";
import { cleanDatabaseAiQueryOutput } from "@/domain/database/ai-query";
import type { DbType } from "@/domain/database/types";
import { normalizeAiApiFormat } from "@/domain/ai/api-format";
import { normalizeAnthropicAuth } from "@/domain/ai/anthropic-auth";
import { aiProviderAuthMessageKey, validateAiProviderAuth } from "@/domain/ai/provider-auth";
import { useVueI18n } from "@/i18n/messages";
import {
  generateAiDatabaseQuery,
  type DatabaseAiQueryContext,
} from "@/services/ai-database-query";
import {
  hydrateSettingsSnapshot,
  readSettingsSnapshot,
} from "@/services/settings-persistence";
import { nativeErrorMessage } from "@/services/desktop-bridge";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

defineOptions({ name: "DatabaseAiQueryDialog" });

const AI_QUERY_FIRST_TOKEN_TIMEOUT_MS = 30_000;
const AI_QUERY_GENERATION_TIMEOUT_MS = 90_000;

interface StoredAiProfile {
  id?: string;
  provider?: string;
  baseUrl?: string;
  apiKey?: string;
  model?: string;
  apiFormat?: string;
  anthropicAuth?: string;
  contextWindow?: number;
  maxInputTokens?: number;
  maxOutputTokens?: number;
  openaiStreamUsage?: "auto" | "always" | "never";
  noToolModels?: string[];
}

interface StoredAiSettings {
  activeAiProfileId?: string;
  aiProfiles?: StoredAiProfile[];
}

const props = defineProps<{
  modelValue: boolean;
  dbType: DbType;
  database?: string;
  table?: string;
  desktopAvailable: boolean;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  (e: "apply", query: string): void;
}>();

type AiQueryPhase = "input" | "generating" | "done" | "error";

const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();

const description = ref("");
const phase = ref<AiQueryPhase>("input");
const preview = ref("");
const errorMessage = ref("");
let controller: AbortController | undefined;

const generating = computed(() => phase.value === "generating");
const contextText = computed(() =>
  [props.dbType, props.database, props.table].filter(Boolean).join(" · "),
);
const canGenerate = computed(
  () => Boolean(description.value.trim()) && props.desktopAvailable,
);

function activeAiProfile() {
  const stored = readSettingsSnapshot<StoredAiSettings & Record<string, unknown>>();
  return (
    stored.aiProfiles?.find((item) => item.id === stored.activeAiProfileId) ??
    stored.aiProfiles?.[0]
  );
}

function currentAiConfig(profile = activeAiProfile()) {
  return {
    baseUrl: typeof profile?.baseUrl === "string" ? profile.baseUrl : "",
    apiKey: typeof profile?.apiKey === "string" ? profile.apiKey : "",
    model: typeof profile?.model === "string" ? profile.model : "",
    apiFormat: normalizeAiApiFormat(profile?.apiFormat),
    anthropicAuth: normalizeAnthropicAuth(profile?.anthropicAuth),
    contextWindow: typeof profile?.contextWindow === "number" ? profile.contextWindow : undefined,
    maxInputTokens: typeof profile?.maxInputTokens === "number" ? profile.maxInputTokens : undefined,
    maxOutputTokens:
      typeof profile?.maxOutputTokens === "number" ? profile.maxOutputTokens : undefined,
    openaiStreamUsage: profile?.openaiStreamUsage ?? ("auto" as const),
    noToolModels: Array.isArray(profile?.noToolModels) ? profile.noToolModels : [],
  };
}

/** 表字段与同库表清单为尽力而为的上下文，缺失时退化为仅按库类型生成。 */
async function resolveContext(): Promise<DatabaseAiQueryContext> {
  const context: DatabaseAiQueryContext = {
    dbType: props.dbType,
    database: props.database,
    table: props.table,
  };
  const sessionId = databaseStore.activeSessionId;
  if (sessionId && props.table) {
    try {
      const metadata = await databaseStore.loadTableMetadata(
        sessionId,
        props.database ?? "",
        props.table,
      );
      context.columns = metadata.columns.map((column) => ({
        name: column.name,
        dataType: column.dataType,
      }));
    } catch {
      // 字段加载失败不阻塞生成
    }
  }
  const schema = databaseStore.activeSchema;
  const databases = schema?.databases ?? [];
  const scoped = props.database
    ? databases.filter((database) => database.name === props.database)
    : databases;
  context.tables = scoped.flatMap((database) =>
    database.tables.map((table) => table.name),
  );
  return context;
}

function stopGeneration() {
  controller?.abort();
}

async function generate() {
  if (!canGenerate.value || generating.value) return;
  const current = new AbortController();
  controller = current;
  phase.value = "generating";
  preview.value = "";
  errorMessage.value = "";
  let receivedText = false;
  const firstTokenTimeoutId = setTimeout(() => current.abort(), AI_QUERY_FIRST_TOKEN_TIMEOUT_MS);
  const generationTimeoutId = setTimeout(() => current.abort(), AI_QUERY_GENERATION_TIMEOUT_MS);

  try {
    const [context] = await Promise.all([resolveContext(), hydrateSettingsSnapshot()]);
    if (current.signal.aborted) return;
    const profile = activeAiProfile();
    const auth = validateAiProviderAuth({
      provider: profile?.provider,
      baseUrl: typeof profile?.baseUrl === "string" ? profile.baseUrl : "",
      apiKey: typeof profile?.apiKey === "string" ? profile.apiKey : "",
    });
    if (!auth.valid) throw new Error(t(aiProviderAuthMessageKey(auth)));

    const query = await generateAiDatabaseQuery({
      config: currentAiConfig(profile),
      description: description.value,
      context,
      signal: current.signal,
      onDelta: (delta) => {
        if (current.signal.aborted) return;
        if (!receivedText) {
          receivedText = true;
          clearTimeout(firstTokenTimeoutId);
        }
        preview.value += delta;
      },
    });
    if (current.signal.aborted) return;
    preview.value = query;
    phase.value = "done";
  } catch (error) {
    if (current.signal.aborted) {
      phase.value = "input";
      preview.value = "";
      return;
    }
    errorMessage.value = nativeErrorMessage(error);
    phase.value = "error";
  } finally {
    clearTimeout(firstTokenTimeoutId);
    clearTimeout(generationTimeoutId);
    if (controller === current) controller = undefined;
  }
}

function applyResult() {
  const query = cleanDatabaseAiQueryOutput(preview.value);
  if (!query) return;
  emit("apply", query);
  emit("update:modelValue", false);
  ElMessage.success(t("database.aiQueryApplied"));
}

function closeDialog() {
  stopGeneration();
  emit("update:modelValue", false);
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) {
      description.value = "";
      preview.value = "";
      errorMessage.value = "";
      phase.value = "input";
    } else {
      stopGeneration();
    }
  },
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('database.aiQueryTitle')"
    width="min(640px, calc(100vw - 32px))"
    class="db-ai-query-dialog"
    append-to-body
    align-center
    @update:model-value="emit('update:modelValue', $event)"
    @closed="stopGeneration"
  >
    <div class="db-ai-query-dialog__body">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        :title="t('database.aiQueryReviewNotice')"
      />
      <div class="db-ai-query-dialog__context">
        <span class="db-ai-query-dialog__context-label">{{ t("database.aiQueryContext") }}</span>
        <span class="db-ai-query-dialog__context-value">{{ contextText }}</span>
      </div>
      <el-input
        v-model="description"
        type="textarea"
        :rows="3"
        resize="none"
        :disabled="generating"
        :placeholder="t('database.aiQueryPlaceholder')"
        :aria-label="t('database.aiQueryTitle')"
        @keydown.meta.enter="generate"
        @keydown.ctrl.enter="generate"
      />
      <el-alert
        v-if="!props.desktopAvailable"
        type="warning"
        :closable="false"
        show-icon
        :title="t('database.aiQueryDesktopOnly')"
      />
      <el-input
        v-if="phase === 'generating' || phase === 'done'"
        :model-value="preview"
        type="textarea"
        :rows="8"
        resize="none"
        readonly
        :placeholder="t('database.aiQueryStreaming')"
        :aria-label="t('database.aiQueryPreview')"
      />
      <el-alert
        v-if="phase === 'error'"
        type="error"
        :closable="false"
        show-icon
        :title="t('database.aiQueryFailed', { reason: errorMessage })"
      />
    </div>
    <template #footer>
      <el-button native-type="button" @click="closeDialog">{{ t("common.cancel") }}</el-button>
      <el-button
        v-if="generating"
        type="danger"
        plain
        native-type="button"
        @click="stopGeneration"
      >
        {{ t("database.aiQueryStop") }}
      </el-button>
      <template v-else>
        <el-button
          v-if="phase === 'done'"
          native-type="button"
          @click="generate"
        >
          {{ t("database.aiQueryRegenerate") }}
        </el-button>
        <el-button
          v-else
          type="primary"
          native-type="button"
          :disabled="!canGenerate"
          :loading="false"
          @click="generate"
        >
          <el-icon><MagicStick /></el-icon>{{ t("database.aiQueryGenerate") }}
        </el-button>
        <el-button
          v-if="phase === 'done'"
          type="primary"
          native-type="button"
          :disabled="!preview.trim()"
          @click="applyResult"
        >
          {{ t("database.aiQueryApply") }}
        </el-button>
      </template>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-ai-query-dialog__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.db-ai-query-dialog__context {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  font-size: 12px;
}

.db-ai-query-dialog__context-label {
  flex: none;
  color: var(--app-text-muted);
}

.db-ai-query-dialog__context-value {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-text-regular);
}
</style>
