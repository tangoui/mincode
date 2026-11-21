<script setup lang="ts">
// 笔记标签选择：可选已有标签或输入新建。选项在下拉展开时（点击或键盘输入）从后端重新读取；
// 每次修改提交完整标签列表并按顺序排队，最后一次选择最终生效；提交期间显示待保存的值，失败时回到 store 中的值。
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { listNoteTags, noteErrorMessage } from "@/services/notes-service";
import { useNotesStore } from "@/stores/notes";

const props = defineProps<{ noteId: string }>();
const { t } = useVueI18n();
const store = useNotesStore();

const options = ref<string[]>([]);
const loadingOptions = ref(false);
const pending = ref<string[] | null>(null);
let optionsRequest = 0;
let queue: Promise<void> = Promise.resolve();
let changeRequest = 0;

const value = computed(() => pending.value ?? store.loadedNotes[props.noteId]?.tags ?? []);

watch(() => props.noteId, () => {
  pending.value = null;
  changeRequest += 1;
});

async function loadOptions() {
  const request = ++optionsRequest;
  loadingOptions.value = true;
  try {
    const tags = await listNoteTags();
    if (request === optionsRequest) options.value = tags.map((tag) => tag.name);
  } catch (error) {
    if (request === optionsRequest) ElMessage.error(noteErrorMessage(error, t));
  } finally {
    if (request === optionsRequest) loadingOptions.value = false;
  }
}

function onVisibleChange(visible: boolean) {
  if (visible) void loadOptions();
}

function onChange(next: string[]) {
  const id = props.noteId;
  const request = ++changeRequest;
  pending.value = next;
  queue = queue.then(async () => {
    try {
      await store.setTags(id, next);
    } catch (error) {
      ElMessage.error(noteErrorMessage(error, t));
    } finally {
      if (request === changeRequest) pending.value = null;
    }
  });
}
</script>

<template>
  <el-select
    class="note-tag-picker"
    :model-value="value"
    multiple
    filterable
    allow-create
    default-first-option
    collapse-tags
    collapse-tags-tooltip
    :max-collapse-tags="4"
    :reserve-keyword="false"
    :loading="loadingOptions"
    :placeholder="t('notes.tags.placeholder')"
    :aria-label="t('notes.tags.label')"
    size="small"
    @update:model-value="onChange"
    @visible-change="onVisibleChange"
  >
    <el-option v-for="tag in options" :key="tag" :label="tag" :value="tag" />
  </el-select>
</template>
