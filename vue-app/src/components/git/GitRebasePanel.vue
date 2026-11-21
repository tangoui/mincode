<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { useVueI18n } from "@/i18n/messages";
import {
  getGitLog,
  runInteractiveGitRebase,
  type GitLogEntry,
  type GitRebaseAction,
} from "@/services/git-service";

defineOptions({ name: "GitRebasePanel" });

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ completed: [] }>();

const { t } = useVueI18n();
const logs = ref<GitLogEntry[]>([]);
const baseHash = ref("");
const rows = ref<Array<{ commit: GitLogEntry; action: GitRebaseAction }>>([]);
const loading = ref(false);
// 仓库切换后旧仓库的提交列表必须丢弃，否则变基会拿 A 的提交作用在 B 上。
const refreshGate = createLatestRequestGate(() => props.repoPath);

// 最新一条提交不能作为变基起点，它本身就是要被重写的范围。
const baseOptions = computed(() => logs.value.slice(1));

function prepareRows(hash = baseHash.value) {
  baseHash.value = hash;
  const baseIndex = logs.value.findIndex((entry) => entry.hash === hash);
  rows.value =
    baseIndex > 0
      ? logs.value
          .slice(0, baseIndex)
          .reverse()
          .map((commit) => ({ commit, action: "pick" as GitRebaseAction }))
      : [];
}

async function refresh() {
  const request = refreshGate.begin();
  if (!request.target) {
    logs.value = [];
    rows.value = [];
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const next = await getGitLog(request.target, 200);
    if (!refreshGate.isCurrent(request)) return;
    logs.value = next;
    const stillPresent = logs.value.some((entry) => entry.hash === baseHash.value);
    prepareRows(stillPresent ? baseHash.value : (logs.value[1]?.hash ?? ""));
  } catch (error) {
    if (refreshGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (refreshGate.isCurrent(request)) loading.value = false;
  }
}

async function executeRebase() {
  if (!baseHash.value || rows.value.length === 0) return;
  const repoPath = props.repoPath;
  try {
    await ElMessageBox.confirm(
      t("git.rebase.confirm").replace("{count}", String(rows.value.length)),
      t("git.inspector.rebase"),
      {
        type: "warning",
        confirmButtonText: t("common.continue"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  loading.value = true;
  try {
    await runInteractiveGitRebase(
      repoPath,
      baseHash.value,
      rows.value.map(({ commit, action }) => ({
        hash: commit.hash,
        action,
        message: commit.message,
      }))
    );
    ElMessage.success(t("git.rebase.completed"));
    emit("completed");
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.repoPath,
  () => {
    baseHash.value = "";
    logs.value = [];
    rows.value = [];
    void refresh();
  },
  { immediate: true }
);
onBeforeUnmount(() => refreshGate.invalidate());

defineExpose({ refresh });
</script>

<template>
  <section class="git-inspector">
    <div class="git-rebase-warning">{{ t("git.rebase.warning") }}</div>
    <label class="git-advanced-field git-advanced-field--wide">
      <span>{{ t("git.rebase.base") }}</span>
      <el-select
        v-model="baseHash"
        size="small"
        filterable
        :placeholder="t('git.rebase.selectBase')"
        @change="prepareRows"
      >
        <el-option
          v-for="entry in baseOptions"
          :key="entry.hash"
          :label="`${entry.short_hash} · ${entry.message}`"
          :value="entry.hash"
        />
      </el-select>
    </label>
    <el-scrollbar class="git-rebase-list">
      <div v-for="row in rows" :key="row.commit.hash" class="git-rebase-row">
        <el-select v-model="row.action" size="small" class="git-rebase-row__action">
          <el-option label="pick" value="pick" />
          <el-option label="squash" value="squash" />
          <el-option label="fixup" value="fixup" />
          <el-option label="drop" value="drop" />
        </el-select>
        <code>{{ row.commit.short_hash }}</code>
        <div>
          <strong>{{ row.commit.message }}</strong>
          <small>{{ row.commit.author }}</small>
        </div>
      </div>
    </el-scrollbar>
    <p v-if="baseHash && rows.length === 0" class="git-inspector-empty">
      {{ t("git.rebase.empty") }}
    </p>
    <div class="git-inspector__actions git-inspector__actions--end">
      <el-button
        type="danger"
        size="small"
        :loading="loading"
        :disabled="!baseHash || rows.length === 0"
        @click="executeRebase"
      >
        {{ t("git.rebase.execute") }}
      </el-button>
    </div>
  </section>
</template>
