<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from "vue";
import { Check, Close, DocumentChecked, FullScreen, Loading, ScaleToOriginal, View } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { useAiPendingEditsStore } from "@/stores/ai-pending-edits";
import { pendingEditStats, pendingEditsForProjectRoot } from "@/domain/ai/pending-edits";
import { useLocalProjectStore } from "@/stores/local-project";

const props = defineProps<{ projectRoot: string }>();
const { t } = useVueI18n();
const pendingEdits = useAiPendingEditsStore();
const project = useLocalProjectStore();
const MonacoDiffEditor = defineAsyncComponent(() => import("@/components/projects/MonacoDiffEditor.vue"));
const reviewDiffReady = ref(false);
const readyReviewFileId = ref<string>();
const reviewFullscreen = ref(false);
const reviewSize = ref({ width: 1040, height: 760 });
const actionable = computed(() => pendingEditsForProjectRoot(
  pendingEdits.actionable,
  props.projectRoot,
));
const stats = computed(() => pendingEditStats(actionable.value));
const selected = computed(() =>
  actionable.value.find((edit) => edit.id === pendingEdits.selectedId) ?? actionable.value[0]
);

const REVIEW_MIN_WIDTH = 720;
const REVIEW_MIN_HEIGHT = 460;
const REVIEW_VIEWPORT_GAP = 32;
const reviewDialogWidth = computed(() => `${clampReviewWidth(reviewSize.value.width)}px`);
const reviewDialogStyle = computed(() => ({
  "--ai-edit-review-height": `${clampReviewHeight(reviewSize.value.height)}px`,
}));
let activeResize: {
  pointerId: number;
  startX: number;
  startY: number;
  startWidth: number;
  startHeight: number;
} | undefined;
let previousBodyCursor = "";
let previousBodyUserSelect = "";

function availableReviewWidth() {
  return typeof window === "undefined" ? 1040 : Math.max(320, window.innerWidth - REVIEW_VIEWPORT_GAP);
}

function availableReviewHeight() {
  return typeof window === "undefined" ? 760 : Math.max(320, window.innerHeight - REVIEW_VIEWPORT_GAP);
}

function clampReviewWidth(width: number) {
  const maximum = availableReviewWidth();
  return Math.min(Math.max(width, Math.min(REVIEW_MIN_WIDTH, maximum)), maximum);
}

function clampReviewHeight(height: number) {
  const maximum = availableReviewHeight();
  return Math.min(Math.max(height, Math.min(REVIEW_MIN_HEIGHT, maximum)), maximum);
}

function resizeReviewTo(width: number, height: number) {
  reviewSize.value = {
    width: clampReviewWidth(width),
    height: clampReviewHeight(height),
  };
}

function beginReviewResize(event: PointerEvent) {
  if (reviewFullscreen.value || event.button !== 0) return;
  const handle = event.currentTarget as HTMLElement;
  const dialog = handle.closest<HTMLElement>(".ai-edit-review-dialog");
  if (!dialog) return;
  const bounds = dialog.getBoundingClientRect();
  activeResize = {
    pointerId: event.pointerId,
    startX: event.clientX,
    startY: event.clientY,
    startWidth: bounds.width,
    startHeight: bounds.height,
  };
  handle.setPointerCapture(event.pointerId);
  previousBodyCursor = document.body.style.cursor;
  previousBodyUserSelect = document.body.style.userSelect;
  document.body.style.cursor = "nwse-resize";
  document.body.style.userSelect = "none";
  event.preventDefault();
}

function continueReviewResize(event: PointerEvent) {
  if (!activeResize || activeResize.pointerId !== event.pointerId) return;
  resizeReviewTo(
    activeResize.startWidth + event.clientX - activeResize.startX,
    activeResize.startHeight + event.clientY - activeResize.startY
  );
}

function finishReviewResize(event?: PointerEvent) {
  // Restoring body styles without an active drag would clobber cursors set by other UI.
  if (!activeResize) return;
  if (event && activeResize.pointerId !== event.pointerId) return;
  const handle = event?.currentTarget as HTMLElement | undefined;
  if (event && handle?.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
  activeResize = undefined;
  if (typeof document !== "undefined") {
    document.body.style.cursor = previousBodyCursor;
    document.body.style.userSelect = previousBodyUserSelect;
  }
}

function resizeReviewWithKeyboard(event: KeyboardEvent) {
  const step = event.shiftKey ? 48 : 16;
  const dialog = (event.currentTarget as HTMLElement).closest<HTMLElement>(".ai-edit-review-dialog");
  const bounds = dialog?.getBoundingClientRect();
  let width = bounds?.width ?? reviewSize.value.width;
  let height = bounds?.height ?? reviewSize.value.height;
  if (event.key === "ArrowLeft") width -= step;
  else if (event.key === "ArrowRight") width += step;
  else if (event.key === "ArrowUp") height -= step;
  else if (event.key === "ArrowDown") height += step;
  else return;
  event.preventDefault();
  resizeReviewTo(width, height);
}

function toggleReviewFullscreen() {
  reviewFullscreen.value = !reviewFullscreen.value;
}

onBeforeUnmount(() => finishReviewResize());

watch(
  () => [props.projectRoot, actionable.value.map((edit) => edit.id).join("\0")] as const,
  () => {
    if (!actionable.value.some((edit) => edit.id === pendingEdits.selectedId)) {
      pendingEdits.selectedId = actionable.value[0]?.id ?? "";
    }
    if (pendingEdits.reviewOpen && !actionable.value.length) pendingEdits.reviewOpen = false;
  },
  { immediate: true }
);

watch(
  () => [pendingEdits.reviewOpen, selected.value?.id] as const,
  ([reviewOpen, selectedId]) => {
    if (!reviewOpen) {
      reviewFullscreen.value = false;
      finishReviewResize();
      return;
    }
    reviewDiffReady.value = Boolean(selectedId && readyReviewFileId.value === selectedId);
  },
  { flush: "post" }
);

function markReviewDiffReady() {
  if (selected.value) readyReviewFileId.value = selected.value.id;
  reviewDiffReady.value = true;
}
const pendingFileNames = computed(() => actionable.value
  .map((edit) => edit.path.split(/[\\/]/).slice(-1)[0] || edit.path)
  .join(", "));
const pendingFilePaths = computed(() => actionable.value.map((edit) => edit.path).join("\n"));

function openReview(id?: string) {
  pendingEdits.openReview(props.projectRoot, id);
}
function apply(ids = actionable.value.map((edit) => edit.id)) {
  const result = pendingEdits.apply(props.projectRoot, ids);
  if (result.applied.length) ElMessage.success(t("ai.edits.applied", { count: result.applied.length }));
}
function localizedRevertError(error: string) {
  return error.includes("ai_edit_revert_conflict") ? t("ai.edits.revertConflict") : error;
}
async function revert(ids = actionable.value.map((edit) => edit.id)) {
  const result = await pendingEdits.revert(props.projectRoot, ids);
  if (result.reverted.length) ElMessage.success(t("ai.edits.reverted", { count: result.reverted.length }));
  if (result.failed.length) ElMessage.error(localizedRevertError(result.failed[0]?.error ?? t("ai.edits.revertFailed")));
}
async function revertSelected() {
  if (!selected.value) return;
  await revert([selected.value.id]);
}
</script>

<template>
  <section v-if="actionable.length" class="ai-edit-batch">
    <el-button class="ai-edit-batch__summary" text native-type="button" @click="openReview()">
      <span class="ai-edit-batch__summary-content">
        <el-icon><DocumentChecked /></el-icon>
        <span>
          <strong>{{ t("ai.edits.pending", { count: actionable.length }) }}</strong>
          <small>
            <span class="ai-edit-batch__file-names" :title="pendingFilePaths">{{ pendingFileNames }}</span>
            <b>+{{ stats.additions }}</b>
            <em>-{{ stats.deletions }}</em>
          </small>
        </span>
      </span>
    </el-button>
    <div class="ai-edit-batch__actions">
      <el-tooltip :content="t('ai.edits.review')" placement="top">
        <el-button text :icon="View" :disabled="pendingEdits.applying" :aria-label="t('ai.edits.review')" @click="openReview()" />
      </el-tooltip>
      <el-tooltip :content="t('ai.edits.rejectAll')" placement="top">
        <el-button text :icon="Close" :loading="pendingEdits.applying" :aria-label="t('ai.edits.rejectAll')" @click="revert()" />
      </el-tooltip>
      <el-tooltip :content="t('ai.edits.applyAll')" placement="top">
        <el-button
          type="primary"
          :icon="Check"
          :disabled="pendingEdits.applying"
          :aria-label="t('ai.edits.applyAll')"
          @click="apply()"
        />
      </el-tooltip>
    </div>
  </section>

  <el-dialog
    v-model="pendingEdits.reviewOpen"
    class="ai-edit-review-dialog"
    :width="reviewDialogWidth"
    :style="reviewDialogStyle"
    :fullscreen="reviewFullscreen"
    draggable
    align-center
    append-to-body
  >
    <template #header="{ titleId, titleClass }">
      <div class="ai-edit-review__header">
        <strong :id="titleId" :class="titleClass">{{ t("ai.edits.reviewTitle") }}</strong>
        <el-tooltip
          :content="t(reviewFullscreen ? 'ai.edits.exitFullscreen' : 'ai.edits.enterFullscreen')"
          placement="bottom"
        >
          <el-button
            class="ai-edit-review__fullscreen"
            text
            :icon="reviewFullscreen ? ScaleToOriginal : FullScreen"
            :aria-label="t(reviewFullscreen ? 'ai.edits.exitFullscreen' : 'ai.edits.enterFullscreen')"
            @mousedown.stop
            @dblclick.stop
            @click.stop="toggleReviewFullscreen"
          />
        </el-tooltip>
      </div>
    </template>
    <div class="ai-edit-review">
      <nav class="ai-edit-review__files" :aria-label="t('ai.edits.files')">
        <el-scrollbar>
          <el-menu
            class="ai-edit-review__file-menu"
            :default-active="pendingEdits.selectedId"
            @select="pendingEdits.selectedId = $event"
          >
            <el-menu-item v-for="edit in actionable" :key="edit.id" :index="edit.id">
              <span>{{ edit.path.split(/[\\/]/).slice(-1)[0] }}</span>
              <small>{{ edit.path }}</small>
              <em v-if="edit.error">{{ edit.error }}</em>
            </el-menu-item>
          </el-menu>
        </el-scrollbar>
      </nav>
      <div class="ai-edit-review__diff" :class="{ 'is-loading': !reviewDiffReady }">
        <MonacoDiffEditor
          v-if="selected"
          :key="selected.id"
          :path="selected.path"
          :original-content="selected.before ?? ''"
          :modified-content="selected.after"
          :preferences="project.editorPreferences"
          @ready="markReviewDiffReady"
        />
        <div v-else class="ai-edit-review__empty">{{ t("ai.edits.noPending") }}</div>
        <div v-if="selected && !reviewDiffReady" class="ai-edit-review__loading" role="status">
          <el-icon class="is-loading" :size="18" aria-hidden="true"><Loading /></el-icon>
          <span>{{ t("common.loading") }}</span>
        </div>
      </div>
    </div>
    <template #footer>
      <div class="ai-edit-review__footer">
        <span>{{ selected?.path ?? t("ai.edits.noPending") }}</span>
        <div>
          <el-button :loading="pendingEdits.applying" :disabled="!selected" @click="revertSelected">
            {{ t("ai.edits.reject") }}
          </el-button>
          <el-button
            type="primary"
            :disabled="!selected || pendingEdits.applying"
            @click="selected && apply([selected.id])"
          >
            {{ t("ai.edits.apply") }}
          </el-button>
        </div>
      </div>
    </template>
    <button
      v-show="!reviewFullscreen"
      class="ai-edit-review__resize-handle"
      type="button"
      :aria-label="t('ai.edits.resizeDialog')"
      :title="t('ai.edits.resizeDialog')"
      @pointerdown="beginReviewResize"
      @pointermove="continueReviewResize"
      @pointerup="finishReviewResize"
      @pointercancel="finishReviewResize"
      @keydown="resizeReviewWithKeyboard"
    />
  </el-dialog>
</template>
