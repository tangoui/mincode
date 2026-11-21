<script setup lang="ts">
import { computed } from "vue";
import { ElTag } from "element-plus";
import {
  confirmTargetMatches,
  estimateMessage,
  reasonMessageKey,
  SQL_SAFETY_CONFIRM_LITERAL,
  type SqlSafetyCheck,
  type SqlSafetyConfirmNotice,
  type SqlSafetyReason,
  type SqlSafetyStatementCheck,
} from "@/domain/database/sql-safety";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  check: SqlSafetyCheck;
  /** 安全网是否开启；关闭时只显示通用确认与语句，不显示估算与留底信息。 */
  enabled: boolean;
  notice?: SqlSafetyConfirmNotice;
  /** 调用方给出的摘要行，显示在语句列表之前。 */
  summary?: readonly string[];
}>();

const { t } = useVueI18n();

const high = computed(() => props.check.risk === "high");
// AI 单行工具累计行数的确认：check 是前端合成的，行数是对话内累计值，不是某条语句的估算，
// 每行各自留底（视存储而定），因此不显示估算与撤回支持。
const rowThreshold = computed(() => props.notice === "aiRowThreshold");
const noticeText = computed(() => {
  if (!props.notice) return "";
  if (!rowThreshold.value) return t(`database.safety.${props.notice}`);
  const rows = props.check.statements[0]?.estimate.rows ?? 0;
  return t("database.safety.aiRowThreshold", { count: rows.toLocaleString() });
});
// 低风险只列摘要，高风险列出全部（有上限，避免超长脚本撑爆对话框）。
const limit = computed(() => (high.value ? 20 : 3));
const textLimit = computed(() => (high.value ? 1000 : 300));
const writes = computed(() => {
  const statements = props.check.statements.filter((statement) => statement.kind !== "read");
  return statements.length ? statements : props.check.statements;
});
const single = computed(() => writes.value.length === 1);
const shown = computed(() => writes.value.slice(0, limit.value));
const hidden = computed(() => writes.value.length - shown.value.length);
const showFacts = computed(() => props.enabled && !rowThreshold.value);

// 「无法估算」「估算值」已体现在影响行数里，不再在原因中重复。
const HIDDEN_REASONS: readonly SqlSafetyReason[] = ["noEstimate", "estimated"];
function visibleReasons(statement: SqlSafetyStatementCheck) {
  return statement.reasons.filter((reason) => !HIDDEN_REASONS.includes(reason));
}
// 单条语句时原因并入概要；多条语句时各自列在语句下。
const summaryReasons = computed(() => (single.value ? visibleReasons(writes.value[0]) : []));

const rowsText = computed(() =>
  single.value ? estimateText(writes.value[0]) : t("database.safety.statementCount", { count: writes.value.length }));

// 提示语拆成三段，把需要输入的目标单独加粗显示。
const typePrompt = computed(() => {
  const target = props.check.confirmTarget;
  const isTable = target !== SQL_SAFETY_CONFIRM_LITERAL && props.check.statements.some((statement) =>
    statement.table?.name && confirmTargetMatches(target, statement.table.name));
  const marker = "\u0000";
  const [before = "", after = ""] = (isTable
    ? t("database.safety.typeTableName", { name: marker })
    : t("database.safety.typeConfirmWord", { word: marker })).split(marker);
  return { before, target, after };
});

function preview(text: string) {
  return text.length > textLimit.value ? `${text.slice(0, textLimit.value)}…` : text;
}

function estimateText(statement: SqlSafetyStatementCheck) {
  const message = estimateMessage(statement.estimate);
  return "count" in message ? t(message.key, { count: message.count.toLocaleString() }) : t(message.key);
}
</script>

<template>
  <div class="app-sql-safety">
    <p v-if="notice" class="app-sql-safety__notice" role="alert">{{ noticeText }}</p>
    <p v-for="line in summary" :key="line" class="app-sql-safety__summary">{{ line }}</p>
    <p v-if="!enabled">{{ t("database.safety.genericConfirm") }}</p>

    <dl v-if="showFacts" class="app-sql-safety__facts">
      <dt>{{ t("database.safety.facts.rows") }}</dt>
      <dd>{{ rowsText }}</dd>
      <dt>{{ t("database.safety.facts.undo") }}</dt>
      <dd :class="check.undoable ? 'app-sql-safety__undo--yes' : 'app-sql-safety__undo--no'">
        {{ t(check.undoable ? "database.safety.undoable" : "database.safety.notUndoable") }}
      </dd>
      <template v-if="summaryReasons.length">
        <dt>{{ t("database.safety.facts.reasons") }}</dt>
        <dd>
          <ul class="app-sql-safety__reasons">
            <li v-for="reason in summaryReasons" :key="reason">{{ t(reasonMessageKey(reason)) }}</li>
          </ul>
        </dd>
      </template>
    </dl>

    <ol class="app-sql-safety__statements">
      <li v-for="statement in shown" :key="statement.index" class="app-sql-safety__statement">
        <pre class="app-sql-safety__sql">{{ preview(statement.text) }}</pre>
        <template v-if="enabled && !single">
          <div class="app-sql-safety__meta">
            <ElTag v-if="statement.risk === 'high'" type="danger" size="small" disable-transitions>
              {{ t("database.safety.highRiskTitle") }}
            </ElTag>
            <span v-if="!rowThreshold">{{ estimateText(statement) }}</span>
          </div>
          <ul v-if="visibleReasons(statement).length" class="app-sql-safety__reasons">
            <li v-for="reason in visibleReasons(statement)" :key="reason">{{ t(reasonMessageKey(reason)) }}</li>
          </ul>
        </template>
      </li>
    </ol>
    <p v-if="hidden > 0" class="app-sql-safety__muted">
      {{ t("database.safety.moreStatements", { count: hidden }) }}
    </p>
    <p v-if="high" class="app-sql-safety__type">
      {{ typePrompt.before }}<strong class="app-sql-safety__target">{{ typePrompt.target }}</strong>{{ typePrompt.after }}
    </p>
  </div>
</template>

<style scoped>
.app-sql-safety {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
  font-size: var(--app-ui-font-size-small);
  color: var(--app-text-regular);
}

.app-sql-safety p {
  margin: 0;
}

.app-sql-safety__notice {
  padding: 6px 8px;
  border: 1px solid var(--app-warning-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-text);
}

.app-sql-safety__summary {
  color: var(--app-text);
  font-weight: 600;
}

.app-sql-safety__facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 16px;
  margin: 0;
}

.app-sql-safety__facts dt {
  color: var(--app-text-muted);
}

.app-sql-safety__facts dd {
  margin: 0;
  min-width: 0;
  color: var(--app-text);
}

.app-sql-safety__facts .app-sql-safety__undo--yes {
  color: var(--app-success);
}

.app-sql-safety__facts .app-sql-safety__undo--no {
  color: var(--app-warning);
}

.app-sql-safety__muted {
  color: var(--app-text-muted);
}

.app-sql-safety__statements {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  margin: 0;
  padding: 0;
  overflow: auto;
  list-style: none;
}

.app-sql-safety__statement {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.app-sql-safety__sql {
  margin: 0;
  padding: 8px 10px;
  max-height: 120px;
  overflow: auto;
  border: 1px solid var(--app-border-subtle);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-sunken);
  color: var(--app-text);
  font-family: var(--app-font-mono);
  font-size: var(--app-ui-font-size-small);
  white-space: pre-wrap;
  word-break: break-all;
}

.app-sql-safety__meta {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--app-text-muted);
}

.app-sql-safety__reasons {
  margin: 0;
  padding-left: 16px;
}

.app-sql-safety__type {
  color: var(--app-text);
}

.app-sql-safety__target {
  padding: 0 4px;
  border-radius: var(--app-radius-sm);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-family: var(--app-font-mono);
}
</style>
