<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from "vue";
import { ElMessageBox } from "element-plus";
import {
  Aim,
  Clock,
  Close,
  CopyDocument,
  Document,
  FullScreen,
  MagicStick,
  Minus,
  Refresh,
  ArrowDown,
  VideoPause,
  ZoomIn,
  Upload,
} from "@element-plus/icons-vue";
import SqlCodeEditor from "@/components/database/query/SqlCodeEditor.vue";
import type { DbType } from "@/domain/database/types";
import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import type { DatabaseQueryHistoryItem } from "@/domain/database/types";
import type { SqlCompletionItem } from "@/domain/database/sql-completion";
import type { ProjectEditorViewState } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";

// Only the non-SQL query languages (JSON, plain text) use Monaco here: load it when one is shown.
const MonacoTextEditor = defineAsyncComponent(() => import("@/components/ui/MonacoTextEditor.vue"));

defineOptions({ name: "DatabaseQueryWorkspace" });

const props = defineProps<{
  activeQueryTab: QueryTab | undefined;
  queryText: string;
  queryEditorPath: string;
  editorLanguage: string;
  sqlCompletions: SqlCompletionItem[] | undefined;
  vectorQueryHintVisible: boolean;
  queryRunning: boolean;
  desktopAvailable: boolean;
  editorFontSize: number;
  editorExpanded: boolean;
  queryHistory: DatabaseQueryHistoryItem[];
  /** SQL editor plugin menu context: database engine and schema name (never connection details). */
  /** Connection type: the SQL editor's dialect. */
  dbType?: DbType;
}>();

const emit = defineEmits<{
  (e: "update:queryText", value: string): void;
  (e: "view-state", path: string, state: ProjectEditorViewState): void;
  (e: "run", mode?: "smart" | "all"): void;
  (e: "cancel"): void;
  (e: "explain"): void;
  (e: "format"): void;
  (e: "font-size", delta: number): void;
  (e: "clear"): void;
  (e: "toggle-expanded"): void;
  (e: "open-history", item: DatabaseQueryHistoryItem): void;
  (e: "clear-history"): void;
  (e: "copy"): void;
  (e: "save-script"): void;
  (e: "import", file: File): void;
  (e: "regenerate"): void;
  (e: "ai-generate"): void;
}>();

const { t } = useVueI18n();
const activeQueryRunning = computed(() => props.activeQueryTab?.status === "running");
const activeQueryError = computed(() => props.activeQueryTab?.error);
const historyVisible = ref(false);
const fileInput = ref<HTMLInputElement>();

function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (file) emit("import", file);
  input.value = "";
}

async function confirmClearHistory() {
  try {
    await ElMessageBox.confirm(t("database.clearHistoryConfirm"), t("database.clearHistory"), {
      type: "warning",
      confirmButtonText: t("database.clearHistory"),
      cancelButtonText: t("common.cancel"),
    });
    emit("clear-history");
  } catch {
    // 用户取消清空历史。
  }
}
</script>

<template>
  <template v-if="props.activeQueryTab">
    <div class="db-sql-editor" :class="{ 'is-expanded': props.editorExpanded }">
      <div class="db-sql-toolbar" role="toolbar" :aria-label="t('database.sqlToolbar')">
        <el-button-group>
          <el-button
            type="primary"
            plain
            size="small"
            native-type="button"
            :disabled="props.queryRunning || !props.desktopAvailable || !props.queryText.trim()"
            :title="t('database.runSelectedOrCurrent')"
            @click="emit('run', 'smart')"
          >
            <el-icon><Aim /></el-icon>{{ activeQueryRunning ? t("database.queryRunning") : t("database.query") }}
          </el-button>
          <el-dropdown @command="(command: string) => command === 'all' && emit('run', 'all')">
            <el-button type="primary" plain size="small" native-type="button" :disabled="props.queryRunning || !props.desktopAvailable || !props.queryText.trim()" :title="t('database.runAllSql')">
              <el-icon><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="all">{{ t("database.runAllSql") }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </el-button-group>
        <span class="db-sql-toolbar__divider" />
        <el-button text size="small" native-type="button" :title="t('database.executionPlan')" @click="emit('explain')">
          <el-icon><MagicStick /></el-icon>{{ t("database.executionPlan") }}
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.formatSql')" @click="emit('format')">
          <el-icon><Refresh /></el-icon>{{ t("database.formatSql") }}
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.decreaseFont')" @click="emit('font-size', -1)">
          <el-icon><Minus /></el-icon>A
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.increaseFont')" @click="emit('font-size', 1)">
          <el-icon><ZoomIn /></el-icon>A
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.saveScript')" :disabled="!props.queryText.trim()" @click="emit('save-script')">
          <el-icon><Document /></el-icon>{{ t("database.saveScript") }}
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.copySql')" :disabled="!props.queryText.trim()" @click="emit('copy')">
          <el-icon><CopyDocument /></el-icon>
        </el-button>
        <el-button text size="small" native-type="button" :title="t('database.importSql')" @click="fileInput?.click()">
          <el-icon><Upload /></el-icon>{{ t("database.importSql") }}
        </el-button>
        <input ref="fileInput" class="db-sql-toolbar__file" type="file" accept=".sql,.txt" @change="handleFileChange" />
        <el-popover v-model:visible="historyVisible" placement="bottom-start" :width="360" trigger="click">
          <template #reference>
            <el-button text size="small" native-type="button" :title="t('database.history')">
              <el-icon><Clock /></el-icon>{{ t("database.history") }}
            </el-button>
          </template>
          <div class="db-sql-history">
            <div class="db-sql-history__header">
              <strong>{{ t("database.history") }}</strong>
              <el-button text size="small" :disabled="!props.queryHistory.length" @click="confirmClearHistory">{{ t("database.clearHistory") }}</el-button>
            </div>
            <el-empty v-if="!props.queryHistory.length" :description="t('database.noHistory')" :image-size="48" />
            <el-scrollbar v-else max-height="280px">
              <button
                v-for="item in props.queryHistory.slice(0, 50)"
                :key="item.id"
                type="button"
                class="db-sql-history__item"
                @click="historyVisible = false; emit('open-history', item)"
              >
                <span class="db-sql-history__query">{{ item.query }}</span>
                <small>{{ item.success ? t('database.historyRowCount', { count: item.rowCount }) : t('database.queryFailed') }} · {{ new Date(item.startedAt).toLocaleString() }}</small>
              </button>
            </el-scrollbar>
          </div>
        </el-popover>
        <el-button text size="small" native-type="button" :title="t('database.clearEditor')" :disabled="!props.queryText.trim()" @click="emit('clear')">
          <el-icon><Close /></el-icon>
        </el-button>
        <el-button text size="small" native-type="button" :title="t(props.editorExpanded ? 'database.restoreEditor' : 'database.expandEditor')" @click="emit('toggle-expanded')">
          <el-icon><FullScreen /></el-icon>
        </el-button>
        <span class="db-sql-toolbar__spacer" />
        <span v-if="props.vectorQueryHintVisible" class="db-vector-query-hint">{{ t("database.vectorQueryHint") }}</span>
        <el-button text size="small" native-type="button" :disabled="props.queryRunning" @click="emit('regenerate')">
          <el-icon><Refresh /></el-icon>{{ t("database.generateQuery") }}
        </el-button>
        <el-button text size="small" native-type="button" :disabled="props.queryRunning || !props.desktopAvailable" @click="emit('ai-generate')">
          <el-icon><MagicStick /></el-icon>{{ t("database.aiGenerateQuery") }}
        </el-button>
        <el-button v-if="props.queryRunning" type="danger" size="small" native-type="button" @click="emit('cancel')">
          <el-icon><VideoPause /></el-icon>{{ t("database.cancel") }}
        </el-button>
      </div>
      <!-- SQL connections: CodeMirror (fast to create, small); JSON and plain-text query
           languages (MongoDB, vector stores, Redis, InfluxDB) keep Monaco. -->
      <SqlCodeEditor
        v-if="props.editorLanguage === 'sql'"
        :key="props.activeQueryTab.id"
        :model-value="props.queryText"
        :path="props.queryEditorPath"
        :view-state="props.activeQueryTab.viewState"
        :sql-completions="props.sqlCompletions"
        :db-type="props.dbType"
        :font-size="props.editorFontSize"
        @update:model-value="emit('update:queryText', $event)"
        @view-state="(path: string, state: ProjectEditorViewState) => emit('view-state', path, state)"
        @run="(mode?: 'smart' | 'all') => emit('run', mode ?? 'smart')"
        @format="emit('format')"
      />
      <MonacoTextEditor
        v-else
        :key="props.activeQueryTab.id"
        :model-value="props.queryText"
        :path="props.queryEditorPath"
        :view-state="props.activeQueryTab.viewState"
        :sql-completions="props.editorLanguage === 'sql' ? props.sqlCompletions : undefined"
        :preferences="{
          fontSize: props.editorFontSize,
          tabSize: 2,
          wordWrap: 'on',
          minimap: false,
          lineNumbers: 'on',
        }"
        @update:model-value="emit('update:queryText', $event)"
        kind="sql"
        @view-state="(path: string, state: ProjectEditorViewState) => emit('view-state', path, state)"
        @run="emit('run', 'smart')"
      />
    </div>
    <el-alert
      v-if="activeQueryError"
      class="db-query-result-error"
      :title="activeQueryError"
      type="error"
      :closable="false"
      show-icon
    />
  </template>
</template>
