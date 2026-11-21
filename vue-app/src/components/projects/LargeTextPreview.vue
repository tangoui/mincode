<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, ref, shallowRef, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import type { ProjectEditorDocument } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";
import { readExternalTextChunk, type ExternalTextChunk } from "@/services/external-text-preview";

const props = withDefaults(defineProps<{ document: ProjectEditorDocument; active?: boolean }>(), { active: true });
const emit = defineEmits<{ openSystem: [] }>();
const { t } = useVueI18n();
const chunk = shallowRef<ExternalTextChunk>();
const loading = ref(false);
const error = ref<"changed" | "encoding" | "read" | null>(null);
const history = ref<number[]>([]);
const visible = ref(true);
const scroll = ref<ScrollbarInstance>();
let abort: AbortController | undefined;
let request = 0;
const enabled = computed(() => props.active && visible.value);
const range = computed(() => chunk.value ? t("project.external.largeText.range", {
  start: chunk.value.text.length ? chunk.value.startOffset + 1 : 0,
  end: chunk.value.nextOffset, total: chunk.value.totalBytes,
}) : "");

function cancel() {
  request++;
  abort?.abort();
  abort = undefined;
}

async function load(offset: number, direction: "next" | "previous" | "reload") {
  if (!enabled.value) return;
  cancel();
  const generation = request;
  const controller = new AbortController();
  abort = controller;
  const previous = chunk.value;
  loading.value = true;
  error.value = null;
  try {
    const result = await readExternalTextChunk(props.document.path, offset, direction === "reload" ? undefined : previous?.version, controller.signal);
    if (generation !== request || controller.signal.aborted) return;
    if (direction === "next" && previous) {
      // Only offsets are retained, never previous page text. Reload always returns to byte zero.
      history.value = [...history.value, previous.startOffset].slice(-2048);
    } else if (direction === "previous") history.value.pop();
    chunk.value = result;
    await nextTick();
    if (generation === request) {
      scroll.value?.setScrollTop(0);
      scroll.value?.setScrollLeft(0);
    }
  } catch (failure) {
    if (generation !== request || controller.signal.aborted) return;
    const message = failure instanceof Error ? failure.message : String(failure);
    error.value = message.includes("file_changed") ? "changed" : message.includes("not_a_text_file") ? "encoding" : "read";
    chunk.value = undefined;
  } finally {
    if (generation === request) { loading.value = false; abort = undefined; }
  }
}

function reload() {
  cancel();
  history.value = [];
  chunk.value = undefined;
  void load(0, "reload");
}

function previous() {
  const offset = history.value[history.value.length - 1];
  if (!loading.value && !error.value && offset !== undefined) void load(offset, "previous");
}
function next() {
  if (!loading.value && !error.value && chunk.value && !chunk.value.eof) void load(chunk.value.nextOffset, "next");
}
function preventSave(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
    event.preventDefault(); event.stopPropagation();
  }
}

watch(() => [props.document, props.document.path, props.document.sessionId, enabled.value], () => {
  cancel(); chunk.value = undefined; history.value = []; error.value = null; loading.value = false;
  if (enabled.value) void load(0, "reload");
}, { immediate: true });
onDeactivated(() => { visible.value = false; });
onActivated(() => { visible.value = true; });
onBeforeUnmount(cancel);
</script>

<template>
  <section class="project-large-text" :aria-label="t('project.external.largeText.title')" @keydown="preventSave">
    <header class="project-large-text__toolbar">
      <strong>{{ t("project.external.largeText.title") }}</strong>
      <span class="project-large-text__range" aria-live="polite">{{ range }}</span>
      <el-button native-type="button" :disabled="loading || !!error || !history.length" @click="previous">{{ t("project.external.largeText.previous") }}</el-button>
      <el-button native-type="button" :disabled="loading || !!error || !chunk || chunk.eof" @click="next">{{ t("project.external.largeText.next") }}</el-button>
      <el-button native-type="button" @click="reload">{{ t("project.external.largeText.reload") }}</el-button>
      <el-button native-type="button" @click="emit('openSystem')">{{ t("project.external.openSystem") }}</el-button>
    </header>
    <p class="project-large-text__hint">{{ t("project.external.largeText.hint") }}</p>
    <el-alert v-if="error" :title="t(`project.external.largeText.${error}`)" type="warning" :closable="false" show-icon />
    <span v-if="loading" class="project-large-text__status" role="status">{{ t("project.external.largeText.loading") }}</span>
    <el-scrollbar v-if="chunk && !error" ref="scroll" class="project-large-text__scroll">
      <pre class="project-large-text__content" tabindex="0">{{ chunk.text }}</pre>
    </el-scrollbar>
  </section>
</template>
