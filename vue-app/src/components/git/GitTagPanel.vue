<script setup lang="ts">
import type { BackendCommand } from "@/generated/backend-commands";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { CirclePlus, Delete, Download, PriceTag } from "@element-plus/icons-vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, nativeErrorMessage } from "@/services/desktop-bridge";
import { runGitCommand, type GitTagEntry } from "@/services/git-service";
import AppSearchField from "@/components/ui/AppSearchField.vue";

defineOptions({ name: "GitTagPanel" });

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ changed: [] }>();

const { t, intlLocale } = useVueI18n();
const tags = ref<GitTagEntry[]>([]);
const loading = ref(false);
const loaded = ref(false);
const fetching = ref(false);
const tagQuery = ref("");
// 仓库切换后旧仓库的列表响应必须丢弃，否则后续操作会拿 A 的标签作用在 B 上。
const refreshGate = createLatestRequestGate(() => props.repoPath);

const visibleTags = computed(() => {
  const query = tagQuery.value.trim().toLowerCase();
  if (!query) return tags.value;
  return tags.value.filter(
    (tag) => tag.name.toLowerCase().includes(query) || (tag.message ?? "").toLowerCase().includes(query)
  );
});

function shortHash(tag: GitTagEntry) {
  return (tag.commit_hash || tag.target_hash || "").slice(0, 8);
}

// 说明常是 Markdown 发布记录的第一行（"## 更新内容"、"**完整的更新**"），
// 列表里只显示文字，去掉标题、加粗、列表等记号。
function plainMessage(message: string | null | undefined) {
  return (message ?? "")
    .replace(/^\s*(?:#{1,6}|[-*+]|>)\s+/, "")
    .replace(/(\*\*|__|`)/g, "")
    .trim();
}

function tagDate(tag: GitTagEntry) {
  if (!tag.date) return "";
  const date = new Date(tag.date);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleDateString(intlLocale.value);
}

async function refresh() {
  const request = refreshGate.begin();
  if (!request.target) {
    tags.value = [];
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const next = await invokeBackend<GitTagEntry[]>("git_list_tags", { repoPath: request.target });
    if (!refreshGate.isCurrent(request)) return;
    tags.value = next;
    loaded.value = true;
  } catch (error) {
    if (refreshGate.isCurrent(request)) ElMessage.error(nativeErrorMessage(error));
  } finally {
    if (refreshGate.isCurrent(request)) loading.value = false;
  }
}

async function run(command: BackendCommand, args: Record<string, unknown> = {}) {
  loading.value = true;
  try {
    await runGitCommand(command, props.repoPath, args);
    await refresh();
    emit("changed");
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    loading.value = false;
  }
}

async function createTag() {
  const repoPath = props.repoPath;
  let name: string;
  try {
    const result = await ElMessageBox.prompt(t("git.createTagPrompt"), t("git.createTagTitle"), {
      inputPlaceholder: "v1.0.0",
      // 列表已经是这个仓库的全部本地标签，重名直接在输入框里拦下，不用等 git 报错。
      inputValidator: (input) => {
        const value = input.trim();
        if (!value) return t("git.inputRequired");
        if (tags.value.some((tag) => tag.name === value)) return t("git.tagExists", { tag: value });
        return true;
      },
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    name = result.value.trim();
  } catch {
    return;
  }
  if (name && repoPath === props.repoPath) await run("git_create_tag", { name });
}

async function fetchTags() {
  const repoPath = props.repoPath;
  if (!repoPath || fetching.value) return;
  fetching.value = true;
  try {
    await runGitCommand("git_fetch_tags", repoPath);
    if (repoPath !== props.repoPath) return;
    await refresh();
    emit("changed");
    ElMessage.success(t("git.fetchTagsSuccess", { count: tags.value.length }));
  } catch (error) {
    if (repoPath === props.repoPath) ElMessage.error(t("git.fetchTagsFailed", { reason: nativeErrorMessage(error) }));
  } finally {
    fetching.value = false;
  }
}

async function deleteTag(name: string) {
  const repoPath = props.repoPath;
  try {
    await ElMessageBox.confirm(t("git.deleteTagConfirm", { tag: name }), t("git.deleteTagTitle"), {
      type: "warning",
      confirmButtonText: t("common.continue"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  await run("git_delete_tag", { name });
}

watch(() => props.repoPath, () => {
  tags.value = [];
  loaded.value = false;
  tagQuery.value = "";
  void refresh();
}, { immediate: true });
onBeforeUnmount(() => refreshGate.invalidate());

defineExpose({ refresh });
</script>

<template>
  <section class="git-inspector">
    <div class="git-inspector__actions git-ref-toolbar">
      <div class="git-ref-toolbar__buttons">
        <el-button size="small" type="primary" :icon="CirclePlus" :disabled="loading || fetching" @click="createTag"
          >{{ t("git.createTagTitle") }}</el-button
        >
        <el-button size="small" :icon="Download" :loading="fetching" :disabled="loading" @click="fetchTags"
          >{{ t("git.fetchTags") }}</el-button
        >
      </div>
      <AppSearchField
        v-model="tagQuery"
        class="git-ref-toolbar__search"
        clearable
        :clear-label="t('common.clear')"
        :placeholder="t('git.tagSearchPlaceholder')"
      />
    </div>
    <section class="git-ref-list" :aria-label="t('git.inspector.tags')">
      <header class="git-ref-list__header">
        <span>{{ t("git.localTags") }}</span>
        <span class="git-ref-list__count">{{
          tagQuery.trim() ? `${visibleTags.length} / ${tags.length}` : tags.length
        }}</span>
      </header>
      <el-skeleton v-if="!loaded && loading" class="git-ref-list__skeleton" :rows="3" animated />
      <el-empty
        v-else-if="visibleTags.length === 0"
        class="git-ref-list__empty"
        :image-size="56"
        :description="tagQuery.trim() ? t('git.tagSearchEmpty') : t('git.noTagsHint')"
      />
      <div v-for="tag in visibleTags" :key="tag.name" class="git-inspector-row git-ref-list__row git-tag-row">
        <el-icon class="git-ref-list__icon git-tag-row__icon" aria-hidden="true"><PriceTag /></el-icon>
        <!-- 两行布局：名字和日期一行、说明和提交号一行，抽屉再窄名字也能用满整行。 -->
        <div class="git-tag-row__title">
          <span class="git-ref-list__name" :title="tag.name">{{ tag.name }}</span>
          <el-tag v-if="tag.annotated" size="small" type="info" effect="plain" disable-transitions>{{
            t("git.tagAnnotated")
          }}</el-tag>
        </div>
        <span class="git-tag-row__date">{{ tagDate(tag) }}</span>
        <span class="git-tag-row__message" :title="tag.message || undefined">{{ plainMessage(tag.message) }}</span>
        <code class="git-tag-row__hash" :title="tag.commit_hash || tag.target_hash">{{ shortHash(tag) }}</code>
        <el-button
          text
          size="small"
          class="git-inspector-row__action"
          :icon="Delete"
          :title="t('git.deleteTagTitle')"
          :aria-label="t('git.deleteTagTitle')"
          :disabled="loading || fetching"
          @click="deleteTag(tag.name)"
        />
      </div>
    </section>
  </section>
</template>
