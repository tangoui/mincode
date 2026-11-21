<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { CircleCheck, CircleClose, VideoPause, VideoPlay } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type {
  HttpCollectionRunResult,
  HttpCollectionRunSummary,
} from "@/domain/http/collection-runner";
import type { HttpRequestCollection } from "@/domain/http/request-collection";
import { useVueI18n } from "@/i18n/messages";
import { runHttpCollection } from "@/services/http-collection-runner-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{
  modelValue: boolean;
  collection?: HttpRequestCollection;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const { t } = useVueI18n();
const running = ref(false);
const results = ref<HttpCollectionRunResult[]>([]);
const summary = ref<HttpCollectionRunSummary>();
let runVersion = 0;

const progress = computed(() => {
  const total = props.collection?.requests.length ?? 0;
  return total ? Math.round((results.value.length / total) * 100) : 0;
});

async function run() {
  const collection = props.collection;
  if (!collection?.requests.length) {
    ElMessage.warning(t("http.collectionRunEmpty"));
    return;
  }
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }

  const version = ++runVersion;
  running.value = true;
  results.value = [];
  summary.value = undefined;
  try {
    summary.value = await runHttpCollection(collection, {
      shouldContinue: () => version === runVersion,
      onResult: (result) => {
        if (version === runVersion) results.value = [...results.value, result];
      },
    });
  } finally {
    if (version === runVersion) running.value = false;
  }
}

function stop() {
  runVersion += 1;
  running.value = false;
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return stop();
    results.value = [];
    summary.value = undefined;
  }
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('http.collectionRunnerTitle', { name: collection?.name ?? '' })"
    width="min(760px, 94vw)"
    append-to-body
    destroy-on-close
    top="4vh"
    class="http-collection-runner http-fit-dialog"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="stop"
  >
    <div class="http-collection-runner__toolbar">
      <div>
        <strong>{{ collection?.requests.length ?? 0 }}</strong>
        <span>{{ t("http.collectionRunRequests") }}</span>
      </div>
      <template v-if="summary && !running">
        <span class="is-passed">{{ t("http.collectionRunPassed", { count: summary.passed }) }}</span>
        <span class="is-failed">{{ t("http.collectionRunFailed", { count: summary.failed }) }}</span>
        <span>{{ summary.durationMs }} ms</span>
      </template>
      <el-button v-if="running" size="small" :icon="VideoPause" @click="stop">
        {{ t("http.stopCollectionRun") }}
      </el-button>
      <el-button v-else size="small" type="primary" :icon="VideoPlay" @click="run">
        {{ t("http.runCollection") }}
      </el-button>
    </div>

    <el-progress
      v-if="running || results.length"
      :percentage="progress"
      :status="summary?.failed ? 'exception' : summary ? 'success' : undefined"
      :stroke-width="5"
    />

    <el-scrollbar class="http-collection-runner__results">
      <article
        v-for="result in results"
        :key="result.requestId"
        :class="result.passed ? 'is-passed' : 'is-failed'"
      >
        <el-icon><CircleCheck v-if="result.passed" /><CircleClose v-else /></el-icon>
        <span class="http-method">{{ result.method }}</span>
        <div>
          <strong>{{ result.requestName }}</strong>
          <small>{{ result.error || result.url }}</small>
        </div>
        <span v-if="result.status">{{ result.status }}</span>
        <span>{{ result.elapsedMs }} ms</span>
      </article>
      <el-empty
        v-if="!results.length && !running"
        :description="t('http.collectionRunHint')"
        :image-size="48"
      />
    </el-scrollbar>
  </el-dialog>
</template>
