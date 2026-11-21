<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { CopyDocument, Delete, Plus, Search, VideoPlay } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { copyText } from "@/services/desktop-bridge";
import {
  createTerminalBookmark,
  loadTerminalBookmarks,
  saveTerminalBookmarks,
  type TerminalCommandBookmark,
} from "@/services/terminal-tools";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    initialCommand?: string;
  }>(),
  { initialCommand: "" }
);

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  run: [command: string];
}>();

const { t } = useVueI18n();
const bookmarks = ref<TerminalCommandBookmark[]>([]);
const filter = ref("");
const adding = ref(false);
const title = ref("");
const command = ref("");
const loading = ref(false);

const visibleBookmarks = computed(() => {
  const query = filter.value.trim().toLowerCase();
  if (!query) return bookmarks.value;
  return bookmarks.value.filter(
    (item) =>
      item.title.toLowerCase().includes(query) || item.command.toLowerCase().includes(query)
  );
});

async function refreshBookmarks() {
  loading.value = true;
  try {
    bookmarks.value = await loadTerminalBookmarks();
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    void refreshBookmarks();
    const initialCommand = props.initialCommand.trim();
    if (initialCommand) {
      adding.value = true;
      command.value = initialCommand;
      title.value = initialCommand.replace(/\s+/g, " ").slice(0, 32);
    }
  },
  { immediate: true }
);

function close() {
  emit("update:modelValue", false);
}

async function addBookmark() {
  if (!title.value.trim() || !command.value.trim()) return;
  const next = [createTerminalBookmark(title.value, command.value), ...bookmarks.value];
  bookmarks.value = next;
  await saveTerminalBookmarks(next);
  title.value = "";
  command.value = "";
  adding.value = false;
}

async function removeBookmark(id: string) {
  const next = bookmarks.value.filter((item) => item.id !== id);
  bookmarks.value = next;
  await saveTerminalBookmarks(next);
}

function runBookmark(item: TerminalCommandBookmark) {
  emit("run", item.command);
  close();
}

async function copyBookmark(item: TerminalCommandBookmark) {
  try {
    await copyText(item.command);
    ElMessage.success(t("terminal.bookmarkCopied"));
  } catch {
    ElMessage.error(t("terminal.bookmarkCopyFailed"));
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('terminal.bookmarks')"
    width="min(520px, calc(100vw - 32px))"
    class="terminal-tools-dialog"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="terminal-bookmarks-dialog">
      <div class="terminal-bookmarks-dialog__toolbar">
        <el-input v-model="filter" :placeholder="t('terminal.bookmarkSearchPlaceholder')" clearable>
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button :type="adding ? 'primary' : 'default'" @click="adding = !adding">
          <el-icon><Plus /></el-icon>{{ adding ? t('common.cancel') : t('terminal.bookmarkAdd') }}
        </el-button>
      </div>

      <div v-if="adding" class="terminal-bookmarks-dialog__form">
        <el-input v-model="title" :placeholder="t('terminal.bookmarkNamePlaceholder')" />
        <el-input
          v-model="command"
          :placeholder="t('terminal.bookmarkCommandPlaceholder')"
          @keyup.enter="addBookmark"
        />
        <el-button type="primary" :disabled="!title.trim() || !command.trim()" @click="addBookmark">
          {{ t('common.save') }}
        </el-button>
      </div>

      <el-scrollbar v-loading="loading" class="terminal-bookmarks-dialog__list">
        <el-empty
          v-if="!loading && !visibleBookmarks.length"
          :image-size="56"
          :description="filter.trim() ? t('terminal.bookmarkNoMatches') : t('terminal.bookmarkEmpty')"
        />
        <article v-for="item in visibleBookmarks" :key="item.id" class="terminal-bookmark-item">
          <div class="terminal-bookmark-item__content">
            <strong>{{ item.title }}</strong>
            <code>{{ item.command }}</code>
          </div>
          <div class="terminal-bookmark-item__actions">
            <el-tooltip :content="t('terminal.bookmarkRun')">
              <el-button
                text
                native-type="button"
                :aria-label="t('terminal.bookmarkRun')"
                @click="runBookmark(item)"
              >
                <el-icon><VideoPlay /></el-icon>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('terminal.bookmarkCopy')">
              <el-button
                text
                native-type="button"
                :aria-label="t('terminal.bookmarkCopy')"
                @click="copyBookmark(item)"
              >
                <el-icon><CopyDocument /></el-icon>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('common.delete')">
              <el-button
                text
                native-type="button"
                class="is-danger"
                :aria-label="t('common.delete')"
                @click="removeBookmark(item.id)"
              >
                <el-icon><Delete /></el-icon>
              </el-button>
            </el-tooltip>
          </div>
        </article>
      </el-scrollbar>
    </div>
  </el-dialog>
</template>
