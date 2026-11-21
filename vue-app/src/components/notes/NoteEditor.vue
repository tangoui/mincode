<script setup lang="ts">
// 笔记页与关联笔记抽屉共用的编辑区：标题栏、关联与标签栏、格式工具栏、编辑 / 预览区。
// 标题与正文只经 store.editNote 修改（共享草稿，防抖保存），从不直接改 store 中的笔记对象。
// 切换笔记先 flush 旧笔记；组件卸载或 KeepAlive 停用（路由离开）时 flush 当前笔记。
import { computed, defineAsyncComponent, onBeforeUnmount, onDeactivated, ref, watch, type Component } from "vue";
import { useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import {
  ChatDotRound,
  Checked,
  Coin,
  Expand,
  Fold,
  Folder,
  Grid,
  Link,
  Minus,
  Monitor,
  Picture,
  Plus,
  Star,
  StarFilled,
} from "@element-plus/icons-vue";
import NoteMarkdownEditor from "@/components/notes/NoteMarkdownEditor.vue";
import NotePreview from "@/components/notes/NotePreview.vue";
import NoteTagPicker from "@/components/notes/NoteTagPicker.vue";
import { openLinkTarget, useLinkObjectSnapshot } from "@/composables/use-note-link-snapshot";
import { noteLinkKey, resolveLink, type LinkStatus } from "@/domain/notes/link-targets";
import type { MarkdownFormatKind } from "@/domain/notes/markdown-format";
import type { NoteLink, NoteLinkType } from "@/domain/notes/types";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { noteErrorMessage } from "@/services/notes-service";
import { NOTE_TITLE_MAX_LENGTH, useNotesStore } from "@/stores/notes";

const NoteLinkDialog = defineAsyncComponent(() => import("@/components/notes/NoteLinkDialog.vue"));

type NoteEditorMode = "source" | "split" | "preview";

/** 上次使用的编辑模式。笔记是全局资源，模式偏好同样全局，不走工作区作用域持久化。 */
const MODE_STORAGE_KEY = "notes.editorMode";
/** 抽屉（紧凑模式）单独记住模式，不影响笔记页；抽屉太窄，不提供分栏。 */
const COMPACT_MODE_STORAGE_KEY = "notes.drawerEditorMode";
const MODES: readonly NoteEditorMode[] = ["source", "split", "preview"];
const COMPACT_MODES: readonly NoteEditorMode[] = ["source", "preview"];

const props = withDefaults(defineProps<{ noteId: string; compact?: boolean; sidebarCollapsed?: boolean }>(), {
  compact: false,
  sidebarCollapsed: false,
});
const emit = defineEmits<{ (e: "toggle-sidebar"): void }>();
const { t, intlLocale } = useVueI18n();
const store = useNotesStore();

const note = computed(() => store.loadedNotes[props.noteId]);
const loadError = ref("");
const favoriteBusy = ref(false);
const editorRef = ref<InstanceType<typeof NoteMarkdownEditor> | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);

const availableModes = props.compact ? COMPACT_MODES : MODES;
const modeStorageKey = props.compact ? COMPACT_MODE_STORAGE_KEY : MODE_STORAGE_KEY;
const defaultMode: NoteEditorMode = props.compact ? "source" : "split";

function readMode(): NoteEditorMode {
  try {
    const stored = localStorage.getItem(modeStorageKey);
    return availableModes.find((mode) => mode === stored) ?? defaultMode;
  } catch {
    return defaultMode;
  }
}

const mode = ref<NoteEditorMode>(readMode());
watch(mode, (value) => {
  try {
    localStorage.setItem(modeStorageKey, value);
  } catch {
    // 存储不可用时只是不记住模式。
  }
});

let openRequest = 0;
function open(id: string) {
  const request = ++openRequest;
  loadError.value = "";
  if (!id) return;
  store.openNote(id).catch((error: unknown) => {
    if (request === openRequest) loadError.value = noteErrorMessage(error, t);
  });
}

watch(
  () => props.noteId,
  (id, previous) => {
    if (previous) void store.flushNote(previous);
    open(id);
  },
  { immediate: true },
);

// 打开中的笔记被删除（store 丢弃了它而 noteId 未变）时重走打开流程，显示「不存在」而不是一直「正在打开」。
watch(note, (current, previous) => {
  if (previous && !current && previous.id === props.noteId) open(props.noteId);
});

function flushCurrent() {
  if (props.noteId) void store.flushNote(props.noteId);
}
onDeactivated(flushCurrent);
onBeforeUnmount(flushCurrent);

const saveError = computed(() => store.saveErrors[props.noteId] ?? "");
const isSaving = computed(() => Boolean(store.saving[props.noteId]));
const updatedText = computed(() => {
  const updatedAt = note.value?.updatedAt;
  if (!updatedAt) return "";
  const date = new Date(updatedAt);
  if (!props.compact) {
    return new Intl.DateTimeFormat(intlLocale.value, { dateStyle: "medium", timeStyle: "short" }).format(date);
  }
  // 抽屉里放不下完整日期：今天只显示时刻，否则只显示月日，完整时间放在悬停提示里。
  const today = new Date();
  const sameDay = date.toDateString() === today.toDateString();
  return new Intl.DateTimeFormat(
    intlLocale.value,
    sameDay ? { hour: "2-digit", minute: "2-digit" } : { month: "short", day: "numeric" },
  ).format(date);
});
const updatedTitle = computed(() => {
  const updatedAt = note.value?.updatedAt;
  if (!updatedAt) return "";
  return new Intl.DateTimeFormat(intlLocale.value, { dateStyle: "medium", timeStyle: "short" }).format(new Date(updatedAt));
});
const canFormat = computed(() => Boolean(note.value) && mode.value !== "preview");

interface FormatTool {
  kind: MarkdownFormatKind;
  label: MessageKey;
  text?: string;
  icon?: Component;
}

const FORMAT_TOOLS: readonly FormatTool[] = [
  { kind: "heading", label: "notes.format.heading", text: "H1" },
  { kind: "bold", label: "notes.format.bold", text: "B" },
  { kind: "italic", label: "notes.format.italic", text: "I" },
  { kind: "code", label: "notes.format.code", text: "`" },
  { kind: "codeBlock", label: "notes.format.codeBlock", text: "{ }" },
  { kind: "bulletList", label: "notes.format.bulletList", text: "•" },
  { kind: "orderedList", label: "notes.format.orderedList", text: "1." },
  { kind: "taskList", label: "notes.format.taskList", icon: Checked },
  { kind: "quote", label: "notes.format.quote", text: "❝" },
  { kind: "link", label: "notes.format.link", icon: Link },
  { kind: "table", label: "notes.format.table", icon: Grid },
  { kind: "rule", label: "notes.format.rule", icon: Minus },
];

function onTitle(value: string) {
  store.editNote(props.noteId, { title: value });
}

function onBody(value: string) {
  store.editNote(props.noteId, { body: value });
}

async function toggleFavorite() {
  const current = note.value;
  if (!current || favoriteBusy.value) return;
  favoriteBusy.value = true;
  try {
    await store.setFavorite(current.id, !current.favorite);
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  } finally {
    favoriteBusy.value = false;
  }
}

function applyFormat(kind: MarkdownFormatKind) {
  if (canFormat.value) editorRef.value?.apply(kind);
}

function chooseImages() {
  if (canFormat.value) fileInput.value?.click();
}

async function onFilesChosen(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files ?? []);
  input.value = "";
  // 逐张插入，保证引用顺序与选择顺序一致；编辑器在此期间被卸载（切到预览）则停止。
  for (const file of files) {
    const editor = editorRef.value;
    if (!editor) return;
    await editor.insertImage(file);
  }
}

function onUploadError(message: string) {
  ElMessage.error(message);
}

function retrySave() {
  void store.flushNote(props.noteId);
}

// ---- 关联栏 ----

const LINK_ICONS: Record<NoteLinkType, Component> = {
  ssh: Monitor,
  project: Folder,
  database: Coin,
  ai: ChatDotRound,
  http: Link,
};

interface LinkTag {
  key: string;
  link: NoteLink;
  icon: Component;
  label: string;
  suffix: string;
  status: LinkStatus;
}

const router = useRouter();
const linkSnapshot = useLinkObjectSnapshot();
const linkDialogOpen = ref(false);
const linkDialogMounted = ref(false);
const removingLinks = ref(new Set<string>());

const linkTags = computed<LinkTag[]>(() =>
  (note.value?.links ?? []).map((link) => {
    const resolved = resolveLink(link, linkSnapshot.value);
    const status = resolved.status;
    const name = resolved.name || (link.objectType === "ai" ? t("notes.link.untitled") : link.objectId);
    return {
      key: noteLinkKey(link),
      link,
      icon: LINK_ICONS[link.objectType],
      label: t("notes.link.label", { type: t(`notes.linkType.${link.objectType}`), name }),
      suffix: resolved.status === "otherWorkspace" ? t("notes.link.otherWorkspaceSuffix", { name: resolved.workspaceName ?? "" }) : "",
      status,
    };
  }),
);

/** 已删除与状态未知（来源加载失败或仍在加载）的关联不可点击跳转。 */
function isOpenable(status: LinkStatus) {
  return status === "current" || status === "otherWorkspace";
}

function linkTitle(tag: LinkTag) {
  const text = `${tag.label}${tag.suffix}`;
  return tag.status === "unknown" ? `${text}\n${t("notes.link.unknownHint")}` : text;
}

function openLinkDialog() {
  linkDialogMounted.value = true;
  linkDialogOpen.value = true;
}

function openLink(tag: LinkTag) {
  if (!isOpenable(tag.status)) return;
  void openLinkTarget(tag.link, linkSnapshot.value, router);
}

async function removeLink(tag: LinkTag) {
  if (removingLinks.value.has(tag.key)) return;
  removingLinks.value.add(tag.key);
  try {
    await store.removeLink(props.noteId, tag.link.objectType, tag.link.objectId, tag.link.scopeId);
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  } finally {
    removingLinks.value.delete(tag.key);
  }
}
</script>

<template>
  <section class="note-editor" :class="{ 'note-editor--compact': compact }">
    <header class="note-editor__header">
      <el-button
        v-if="!compact"
        class="note-editor__icon-button"
        text
        size="small"
        :icon="sidebarCollapsed ? Expand : Fold"
        :title="t(sidebarCollapsed ? 'notes.editor.expandSidebar' : 'notes.editor.collapseSidebar')"
        :aria-label="t(sidebarCollapsed ? 'notes.editor.expandSidebar' : 'notes.editor.collapseSidebar')"
        @click="emit('toggle-sidebar')"
      />
      <el-input
        class="note-editor__title"
        :model-value="note?.title ?? ''"
        :disabled="!note"
        :placeholder="t('notes.editor.untitled')"
        :aria-label="t('notes.editor.titleLabel')"
        :maxlength="NOTE_TITLE_MAX_LENGTH"
        @update:model-value="onTitle"
      />
      <el-button
        class="note-editor__icon-button note-editor__favorite"
        :class="{ 'is-favorite': note?.favorite }"
        text
        size="small"
        :icon="note?.favorite ? StarFilled : Star"
        :disabled="!note"
        :loading="favoriteBusy"
        :title="t(note?.favorite ? 'notes.editor.unfavorite' : 'notes.editor.favorite')"
        :aria-label="t(note?.favorite ? 'notes.editor.unfavorite' : 'notes.editor.favorite')"
        :aria-pressed="Boolean(note?.favorite)"
        @click="toggleFavorite"
      />
      <el-radio-group v-model="mode" class="note-editor__mode" size="small" :aria-label="t('notes.editor.modeLabel')">
        <el-radio-button v-for="item in availableModes" :key="item" :value="item">
          {{ t(`notes.editor.mode.${item}`) }}
        </el-radio-button>
      </el-radio-group>
    </header>

    <template v-if="note">
      <div class="note-editor__meta">
        <slot name="links" :note="note">
          <div class="note-editor__links" role="group" :aria-label="t('notes.link.barLabel')">
            <el-tag
              v-for="tag in linkTags"
              :key="tag.key"
              class="note-editor__link"
              :class="`is-${tag.status}`"
              :type="tag.status === 'deleted' ? 'info' : undefined"
              size="small"
              closable
              disable-transitions
              @close="removeLink(tag)"
            >
              <span
                class="note-editor__link-body"
                :role="isOpenable(tag.status) ? 'button' : undefined"
                :tabindex="isOpenable(tag.status) ? 0 : undefined"
                :title="linkTitle(tag)"
                :aria-label="isOpenable(tag.status) ? t('notes.link.open', { name: tag.label }) : undefined"
                @click="openLink(tag)"
                @keydown.enter.prevent="openLink(tag)"
                @keydown.space.prevent="openLink(tag)"
              >
                <el-icon class="note-editor__link-icon"><component :is="tag.icon" /></el-icon>
                <span class="note-editor__link-name">{{ tag.label }}</span>
                <span v-if="tag.suffix" class="note-editor__link-suffix">{{ tag.suffix }}</span>
                <span v-if="tag.status === 'deleted'" class="note-editor__link-deleted">{{ t("notes.link.deleted") }}</span>
                <span v-else-if="tag.status === 'unknown'" class="note-editor__link-unknown">{{ t("notes.link.unknown") }}</span>
              </span>
            </el-tag>
            <el-button
              class="note-editor__link-add"
              size="small"
              text
              :icon="Plus"
              :title="t('notes.link.addTitle')"
              @click="openLinkDialog"
            >
              {{ t("notes.link.add") }}
            </el-button>
          </div>
        </slot>
        <NoteTagPicker :note-id="noteId" />
      </div>

      <div class="note-editor__toolbar">
        <!-- 只有格式按钮在紧凑模式下横向滚动；保存状态（含失败重试）在滚动区外，始终可见。 -->
        <div class="note-editor__tools" role="toolbar" :aria-label="t('notes.editor.toolbarLabel')">
          <el-button
            v-for="tool in FORMAT_TOOLS"
            :key="tool.kind"
            class="note-editor__tool"
            :class="`note-editor__tool--${tool.kind}`"
            text
            size="small"
            :icon="tool.icon"
            :disabled="!canFormat"
            :title="t(tool.label)"
            :aria-label="t(tool.label)"
            @click="applyFormat(tool.kind)"
          >
            <template v-if="tool.text">{{ tool.text }}</template>
          </el-button>
          <el-button
            class="note-editor__tool"
            text
            size="small"
            :icon="Picture"
            :disabled="!canFormat"
            :title="t('notes.format.image')"
            :aria-label="t('notes.format.image')"
            @click="chooseImages"
          />
          <input
            ref="fileInput"
            class="note-editor__file-input"
            type="file"
            accept="image/png,image/jpeg,image/gif,image/webp"
            multiple
            hidden
            tabindex="-1"
            aria-hidden="true"
            @change="onFilesChosen"
          >
        </div>
        <div class="note-editor__status" aria-live="polite">
          <template v-if="saveError">
            <span class="note-editor__status-error" :title="saveError">{{ t("notes.editor.saveFailed", { message: saveError }) }}</span>
            <el-button link type="primary" size="small" @click="retrySave">{{ t("notes.editor.retry") }}</el-button>
          </template>
          <span v-else-if="isSaving">{{ t("notes.editor.saving") }}</span>
          <span v-else-if="updatedText" :title="updatedTitle">{{ t("notes.editor.updatedAt", { time: updatedText }) }}</span>
        </div>
      </div>

      <div class="note-editor__body" :class="`note-editor__body--${mode}`">
        <div v-if="mode !== 'preview'" class="note-editor__pane note-editor__pane--source">
          <NoteMarkdownEditor
            ref="editorRef"
            :model-value="note.body"
            :document-key="noteId"
            @update:model-value="onBody"
            @upload-error="onUploadError"
          />
        </div>
        <div v-if="mode !== 'source'" class="note-editor__pane note-editor__pane--preview">
          <p v-if="!note.body.trim()" class="note-editor__preview-empty">{{ t("notes.editor.previewEmpty") }}</p>
          <NotePreview v-else :content="note.body" />
        </div>
      </div>
      <NoteLinkDialog v-if="linkDialogMounted" v-model="linkDialogOpen" :note-id="noteId" />
    </template>
    <div v-else class="note-editor__state">
      <el-empty v-if="loadError" :image-size="64" :description="t('notes.editor.loadFailed', { message: loadError })" />
      <span v-else class="note-editor__state-text">{{ t("notes.editor.loading") }}</span>
    </div>
  </section>
</template>
