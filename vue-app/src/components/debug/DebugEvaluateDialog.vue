<script lang="ts">
import { ref } from "vue";

/** Recent expressions of this app session (shared by every dialog, never persisted). */
const recentExpressions = ref<string[]>([]);
</script>

<script setup lang="ts">
/**
 * Evaluate an expression in the selected session's selected frame. Enter
 * evaluates (Shift+Enter adds a line; with the completion list open Enter
 * picks the highlighted item); the result is a lazy variable tree,
 * an adapter error is shown as its message. Results of an earlier stop stay
 * readable but no longer expand.
 */
import { computed, nextTick, onMounted, watch } from "vue";
import { ArrowDown } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { debugErrorText } from "@/domain/debug/errors";
import { rememberExpression } from "@/domain/debug/evaluate-history";
import type { DebugVariableNode } from "@/domain/debug/variable-tree";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import DebugCompletionInput from "@/components/debug/DebugCompletionInput.vue";
import DebugVariableTree from "@/components/debug/DebugVariableTree.vue";

const props = defineProps<{
  modelValue: boolean;
  /** Initial expression (the editor selection). */
  prefill?: string;
  /** Evaluate the prefilled expression on open (hover "inspect"). */
  evaluateOnOpen?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [visible: boolean] }>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

const expression = ref(props.prefill ?? "");
const pending = ref(false);
const result = ref<{ sessionId: string; stop: number; nodes: DebugVariableNode[]; restore: string[] } | null>(null);
const errorMessage = ref("");
const input = ref<{ focus: () => void } | null>(null);
/** Bumped whenever the selected session stops elsewhere or resumes; an older result no longer expands. */
const stop = ref(0);
let request = 0;

const session = computed(() => sessions.selectedSession);
const paused = computed(() => {
  const current = session.value;
  return Boolean(current && current.state === "paused" && !current.noDebug && current.selectedFrameId !== undefined);
});
const history = computed(() => recentExpressions.value);
const live = computed(() => paused.value && result.value !== null && result.value.stop === stop.value && result.value.sessionId === session.value?.id);

watch(
  () => [session.value?.id, session.value?.state, session.value?.selectedFrameId] as const,
  () => {
    stop.value += 1;
  }
);

async function evaluate() {
  const text = expression.value.trim();
  const current = session.value;
  if (!text || !current || !paused.value) return;
  const token = (request += 1);
  const at = stop.value;
  recentExpressions.value = rememberExpression(recentExpressions.value, text);
  pending.value = true;
  try {
    const value = await sessions.evaluate(current.id, text, "repl");
    if (token !== request) return;
    const node: DebugVariableNode = {
      key: "result",
      path: "result",
      name: text,
      value: value.result,
      type: value.type,
      reference: value.variablesReference,
      parentReference: 0,
      kind: "result",
      leaf: !(value.variablesReference > 0),
    };
    errorMessage.value = "";
    result.value = { sessionId: current.id, stop: at, nodes: [node], restore: [node.path] };
  } catch (error) {
    if (token !== request) return;
    result.value = null;
    errorMessage.value = debugErrorText(error);
  } finally {
    if (token === request) pending.value = false;
  }
}

function pickHistory(item: string) {
  expression.value = item;
  void evaluate();
}

onMounted(() => {
  void nextTick(() => input.value?.focus());
  if (props.evaluateOnOpen) void evaluate();
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('debug.evaluate.title')"
    width="620px"
    append-to-body
    class="debug-evaluate"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="debug-evaluate__bar">
      <DebugCompletionInput
        ref="input"
        v-model="expression"
        :session="session"
        multiline
        type="textarea"
        class="debug-evaluate__expression"
        :autosize="{ minRows: 1, maxRows: 6 }"
        resize="none"
        :placeholder="t('debug.evaluate.placeholder')"
        :aria-label="t('debug.evaluate.expression')"
        @submit="evaluate"
      />
      <el-dropdown
        v-if="history.length"
        trigger="click"
        class="debug-evaluate__history"
        @command="pickHistory"
      >
        <el-button native-type="button" :aria-label="t('debug.evaluate.history')" :title="t('debug.evaluate.history')">
          {{ t("debug.evaluate.history") }}<el-icon class="el-icon--right"><ArrowDown /></el-icon>
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item v-for="item in history" :key="item" :command="item" class="debug-evaluate__history-item">
              {{ item }}
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button
        type="primary"
        native-type="button"
        :loading="pending"
        :disabled="!paused || !expression.trim()"
        @click="evaluate"
      >
        {{ t("debug.evaluate.run") }}
      </el-button>
    </div>
    <p v-if="!paused" class="debug-evaluate__hint">{{ t("debug.evaluate.notPaused") }}</p>
    <p v-if="errorMessage" class="debug-evaluate__error" role="alert">{{ errorMessage }}</p>
    <el-scrollbar v-else-if="result" class="debug-evaluate__result" max-height="360px">
      <DebugVariableTree
        :session-id="result.sessionId"
        :nodes="result.nodes"
        :restore-paths="result.restore"
        :live="live"
        :editable="false"
      />
    </el-scrollbar>
  </el-dialog>
</template>
