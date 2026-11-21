<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import MonacoTextEditor from "@/components/projects/MonacoTextEditor.vue";
import {
  abortGitMerge,
  getGitConflictContent,
  getGitMergeState,
  resolveGitConflict,
  type GitConflictContent,
  type GitMergeState,
} from "@/services/git-service";
import {
  applyGitConflictChoice,
  hasGitConflictMarkers,
  parseGitConflictBlocks,
  type GitConflictChoice,
} from "@/domain/git/conflict-resolution";
import { DEFAULT_EDITOR_PREFERENCES } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ changed: []; close: [] }>();
const { t } = useVueI18n();

const state = ref<GitMergeState>({ active: false, conflicts: [] });
const selectedPath = ref("");
const content = ref<GitConflictContent | null>(null);
// 只有成功加载且仍被选中的文件才允许保存，防止把上一个文件的结果写入加载失败的文件。
const loadedPath = ref<string | null>(null);
const loadFailedPath = ref<string | null>(null);
const result = ref("");
const loading = ref(false);
const saving = ref(false);
const sourceNames = ["ours", "base", "theirs"] as const;
const editorPreferences = { ...DEFAULT_EDITOR_PREFERENCES, minimap: false };

const blocks = computed(() => parseGitConflictBlocks(result.value));
const remaining = computed(() => state.value.conflicts.length);
const canSave = computed(
  () =>
    Boolean(selectedPath.value) &&
    loadedPath.value === selectedPath.value &&
    content.value !== null,
);
const emptyDescription = computed(() => {
  if (!selectedPath.value) return t("git.conflict.allResolved");
  return loadFailedPath.value === selectedPath.value
    ? t("git.conflict.loadFailed")
    : t("common.loading");
});

async function refresh(preferredPath = selectedPath.value) {
  if (!props.repoPath) return;
  loading.value = true;
  try {
    state.value = await getGitMergeState(props.repoPath);
    const path = state.value.conflicts.includes(preferredPath)
      ? preferredPath
      : (state.value.conflicts[0] ?? "");
    if (path) await selectConflict(path);
    else {
      selectedPath.value = "";
      clearLoadedConflict();
    }
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

function clearLoadedConflict() {
  content.value = null;
  result.value = "";
  loadedPath.value = null;
  loadFailedPath.value = null;
}

function selectConflict(path: string) {
  selectedPath.value = path;
  clearLoadedConflict();
  return loadConflict(path);
}

async function loadConflict(path: string) {
  if (!path) return;
  loading.value = true;
  try {
    const next = await getGitConflictContent(props.repoPath, path);
    if (path !== selectedPath.value) return;
    content.value = next;
    result.value = next.raw;
    loadedPath.value = path;
  } catch (error) {
    if (path !== selectedPath.value) return;
    loadFailedPath.value = path;
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

function applyChoice(index: number, choice: GitConflictChoice) {
  result.value = applyGitConflictChoice(result.value, index, choice);
}

function useWholeSource(source: "ours" | "theirs") {
  result.value = content.value?.[source] ?? "";
}

async function saveResolution() {
  const resolvedPath = loadedPath.value;
  if (!canSave.value || !resolvedPath) return;
  if (hasGitConflictMarkers(result.value)) {
    ElMessage.warning(t("git.conflict.unresolvedMarkers"));
    return;
  }
  saving.value = true;
  try {
    await resolveGitConflict(props.repoPath, resolvedPath, result.value);
    ElMessage.success(t("git.conflict.resolved", { path: resolvedPath }));
    emit("changed");
    await refresh();
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    saving.value = false;
  }
}

function finishResolution() {
  ElMessage.success(t("git.conflict.readyForManualCommit"));
  emit("changed");
  emit("close");
}

async function abortMerge() {
  try {
    await ElMessageBox.confirm(
      t("git.conflict.abortConfirm"),
      t("git.conflict.abort"),
      {
        type: "warning",
        confirmButtonText: t("git.conflict.abort"),
        cancelButtonText: t("common.cancel"),
      },
    );
  } catch {
    return;
  }
  saving.value = true;
  try {
    await abortGitMerge(props.repoPath);
    ElMessage.success(t("git.conflict.aborted"));
    emit("changed");
    emit("close");
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    saving.value = false;
  }
}

watch(
  () => props.repoPath,
  () => void refresh(""),
  { immediate: true },
);
</script>

<template>
  <div v-loading="loading" class="git-conflict-resolver">
    <aside class="git-conflict-resolver__sidebar">
      <header>
        <strong>{{ t("git.conflict.files") }}</strong>
        <el-tag type="danger" size="small">{{ remaining }}</el-tag>
      </header>
      <el-scrollbar>
        <button
          v-for="path in state.conflicts"
          :key="path"
          type="button"
          class="git-conflict-resolver__file"
          :class="{ 'is-active': path === selectedPath }"
          :title="path"
          @click="selectConflict(path)"
        >
          <span>{{ path.split('/').pop() }}</span>
          <small>{{ path }}</small>
        </button>
      </el-scrollbar>
      <div v-if="state.active && remaining === 0" class="git-conflict-resolver__ready">
        {{ t("git.conflict.allResolved") }}
      </div>
    </aside>

    <main class="git-conflict-resolver__main">
      <template v-if="selectedPath && content">
        <header class="git-conflict-resolver__toolbar">
          <strong :title="selectedPath">{{ selectedPath }}</strong>
          <el-button-group>
            <el-button size="small" @click="useWholeSource('ours')">
              {{ t("git.conflict.acceptAllCurrent") }}
            </el-button>
            <el-button size="small" @click="useWholeSource('theirs')">
              {{ t("git.conflict.acceptAllIncoming") }}
            </el-button>
          </el-button-group>
        </header>

        <section class="git-conflict-resolver__sources">
          <article v-for="source in sourceNames" :key="source">
            <header>{{ t(`git.conflict.${source}`) }}</header>
            <MonacoTextEditor
              kind="conflict"
              :model-value="content[source]"
              :path="`${selectedPath}.${source}`"
              :language-path="selectedPath"
              :preferences="editorPreferences"
              readonly
            />
          </article>
        </section>

        <section class="git-conflict-resolver__result">
          <header>
            <strong>{{ t("git.conflict.result") }}</strong>
            <small>{{ t("git.conflict.blocksRemaining", { count: blocks.length }) }}</small>
          </header>
          <div v-if="blocks.length" class="git-conflict-resolver__blocks">
            <div v-for="(block, index) in blocks" :key="`${block.start}:${index}`">
              <span>{{ t("git.conflict.block", { index: index + 1 }) }}</span>
              <el-button-group>
                <el-button size="small" @click="applyChoice(index, 'ours')">
                  {{ t("git.conflict.acceptCurrent") }}
                </el-button>
                <el-button size="small" @click="applyChoice(index, 'theirs')">
                  {{ t("git.conflict.acceptIncoming") }}
                </el-button>
                <el-button size="small" @click="applyChoice(index, 'both')">
                  {{ t("git.conflict.acceptBoth") }}
                </el-button>
              </el-button-group>
            </div>
          </div>
          <MonacoTextEditor
            v-model="result"
            kind="conflict"
            :path="`${selectedPath}.resolved`"
            :language-path="selectedPath"
            :preferences="editorPreferences"
            @save="saveResolution"
          />
        </section>
      </template>
      <el-empty v-else :description="emptyDescription" />
    </main>

    <footer class="git-conflict-resolver__footer">
      <el-button type="danger" plain :disabled="saving || !state.active" @click="abortMerge">
        {{ t("git.conflict.abort") }}
      </el-button>
      <span>{{ t("git.conflict.remaining", { count: remaining }) }}</span>
      <el-button
        v-if="remaining > 0"
        type="primary"
        :loading="saving"
        :disabled="!canSave || blocks.length > 0"
        @click="saveResolution"
      >
        {{ t("git.conflict.markResolved") }}
      </el-button>
      <el-button
        v-else
        type="primary"
        :loading="saving"
        :disabled="!state.active"
        @click="finishResolution"
      >
        {{ t("git.conflict.finishResolution") }}
      </el-button>
    </footer>
  </div>
</template>
