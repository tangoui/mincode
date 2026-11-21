<script setup lang="ts">
// 全局「关联笔记」抽屉：上方列出关联到当前对象的笔记，下方用紧凑编辑区编辑选中的笔记。
// 由 App 懒加载挂载；状态在 stores/linked-notes-drawer.ts。关闭（含 Esc / 点遮罩）前先保存。
import { computed, watch } from "vue";
import { useRouter } from "vue-router";
import { Plus, TopRight } from "@element-plus/icons-vue";
import NoteEditor from "@/components/notes/NoteEditor.vue";
import { useVueI18n } from "@/i18n/messages";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { useNotesStore } from "@/stores/notes";
import { useWorkbenchStore } from "@/stores/workbench";

const { t, intlLocale } = useVueI18n();
const drawer = useLinkedNotesDrawer();
const notesStore = useNotesStore();
const router = useRouter();

const objectName = computed(() => {
  const target = drawer.target;
  if (!target) return "";
  return target.nameSnapshot || (target.objectType === "ai" ? t("notes.link.untitled") : target.objectId);
});

/** 同名笔记（如多篇「无标题」）靠更新时间区分：今天显示时刻，否则显示月日。 */
function timeOf(updatedAt: number) {
  if (!updatedAt) return "";
  const date = new Date(updatedAt);
  const sameDay = date.toDateString() === new Date().toDateString();
  return new Intl.DateTimeFormat(
    intlLocale.value,
    sameDay ? { hour: "2-digit", minute: "2-digit" } : { month: "short", day: "numeric" },
  ).format(date);
}

/** 列表标题优先取已打开笔记的本地标题（可能尚未保存）。 */
function titleOf(id: string, fallback: string) {
  const title = notesStore.loadedNotes[id]?.title ?? fallback;
  return title || t("notes.editor.untitled");
}

// 抽屉内移除关联、或在别处删除笔记后刷新列表。
watch(
  () => notesStore.linksVersion,
  () => {
    if (drawer.visible) void drawer.reloadList();
  },
);

function onBeforeClose() {
  void drawer.close();
}

async function openInNotes() {
  const id = drawer.activeNoteId;
  if (!id) return;
  notesStore.selectNote(id);
  useWorkbenchStore().openView("notes");
  await router.push({ name: "notes" });
  await drawer.close();
}
</script>

<template>
  <el-drawer
    :model-value="drawer.visible"
    class="linked-notes-drawer"
    direction="rtl"
    size="min(520px, 100vw)"
    append-to-body
    :before-close="onBeforeClose"
  >
    <template #header>
      <div class="linked-notes-drawer__header">
        <h2 class="linked-notes-drawer__title" :title="t('notes.drawer.title', { name: objectName })">
          {{ t("notes.drawer.title", { name: objectName }) }}
        </h2>
        <el-button
          class="linked-notes-drawer__action"
          text
          size="small"
          :icon="Plus"
          :loading="drawer.creating"
          :title="t('notes.drawer.newNote')"
          :aria-label="t('notes.drawer.newNote')"
          @click="drawer.createLinkedNote()"
        />
        <el-button
          class="linked-notes-drawer__action"
          text
          size="small"
          :icon="TopRight"
          :disabled="!drawer.activeNoteId"
          :title="t('notes.drawer.openInNotes')"
          :aria-label="t('notes.drawer.openInNotes')"
          @click="openInNotes"
        />
      </div>
    </template>

    <div class="linked-notes-drawer__body">
      <!-- 多篇笔记时用一行下拉切换，不再用列表占掉编辑区的高度；只有一篇时不显示。 -->
      <p v-if="drawer.listError" class="linked-notes-drawer__state linked-notes-drawer__state--error" role="alert">
        {{ t("notes.drawer.loadFailed", { message: drawer.listError }) }}
      </p>
      <p v-else-if="drawer.listLoading && drawer.notes.length === 0" class="linked-notes-drawer__state">{{ t("notes.editor.loading") }}</p>
      <p v-else-if="drawer.notes.length === 0" class="linked-notes-drawer__state">{{ t("notes.drawer.empty") }}</p>
      <div v-else-if="drawer.notes.length > 1" class="linked-notes-drawer__picker">
        <span class="linked-notes-drawer__picker-label">{{ t("notes.drawer.pickerLabel", { count: drawer.notes.length }) }}</span>
        <el-select
          class="linked-notes-drawer__select"
          :model-value="drawer.activeNoteId"
          size="small"
          filterable
          :teleported="false"
          :aria-label="t('notes.drawer.listLabel')"
          @update:model-value="(id: string) => drawer.selectNote(id)"
        >
          <el-option
            v-for="item in drawer.notes"
            :key="item.id"
            :value="item.id"
            :label="`${titleOf(item.id, item.title)} · ${timeOf(notesStore.loadedNotes[item.id]?.updatedAt ?? item.updatedAt)}`"
          >
            <span class="linked-notes-drawer__option">
              <span class="linked-notes-drawer__option-title">{{ titleOf(item.id, item.title) }}</span>
              <span class="linked-notes-drawer__item-time">{{ timeOf(notesStore.loadedNotes[item.id]?.updatedAt ?? item.updatedAt) }}</span>
            </span>
          </el-option>
        </el-select>
      </div>
      <NoteEditor v-if="drawer.activeNoteId" class="linked-notes-drawer__editor" :note-id="drawer.activeNoteId" compact />
    </div>
  </el-drawer>
</template>
