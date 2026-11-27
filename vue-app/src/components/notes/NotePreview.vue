<script setup lang="ts">
// 笔记预览：renderMarkdownHtml（DOMPurify）→ restrictPreviewImages（只加载笔记附件图片）→ v-html。
// 链接点击沿用 MarkdownPreview 的分类：网页在内置浏览器中打开（与其他 Markdown 预览一致）；
// 笔记没有所在目录，相对 / 本地路径链接不打开；其他协议（javascript: 等）一律不导航，避免 主 WebView
// 被原地跳转。表单提交同样被拦截。
import { onBeforeUnmount, onDeactivated, ref, watch } from "vue";
import NoteSelectionHandoff from "@/components/notes/NoteSelectionHandoff.vue";
import { classifyMarkdownLinkHref } from "@/domain/markdown-links";
import { restrictPreviewImages } from "@/domain/notes/preview-images";
import { useVueI18n } from "@/i18n/messages";
import { openWorkspaceLink } from "@/services/markdown-link-navigation";
import { renderMarkdownHtml } from "@/services/markdown-render";

// 分栏模式下每次按键都会改变内容；连续输入时最多每隔这么久重新渲染一次，并保证最后一次内容被渲染。
const MIN_RENDER_INTERVAL_MS = 120;

const props = defineProps<{ content: string }>();
const { t, locale } = useVueI18n();

// 不写入全局共享的 Markdown 缓存：正文每次按键都不同，缓存只会占内存并挤掉其他预览的条目。
function render() {
  return restrictPreviewImages(renderMarkdownHtml(props.content, { memoize: false }), t("notes.preview.image"));
}

const html = ref(render());
const selectionMenu = ref<{ x: number; y: number; content: string }>();
let lastRenderAt = performance.now();
let trailing: ReturnType<typeof setTimeout> | undefined;

function renderNow() {
  if (trailing !== undefined) clearTimeout(trailing);
  trailing = undefined;
  selectionMenu.value = undefined;
  html.value = render();
  lastRenderAt = performance.now();
}

watch(
  () => props.content,
  () => {
    const wait = MIN_RENDER_INTERVAL_MS - (performance.now() - lastRenderAt);
    if (wait <= 0 && trailing === undefined) renderNow();
    else trailing ??= setTimeout(renderNow, Math.max(0, wait));
  },
);
watch(locale, renderNow);
onBeforeUnmount(() => {
  if (trailing !== undefined) clearTimeout(trailing);
});

function linkOf(event: MouseEvent) {
  return event.target instanceof Element ? event.target.closest("a") : null;
}

function handleClick(event: MouseEvent) {
  const anchor = linkOf(event);
  if (!anchor) return;
  const classification = classifyMarkdownLinkHref(anchor.getAttribute("href"));
  if (classification.kind === "anchor") return;
  event.preventDefault();
  if (classification.kind === "http") void openWorkspaceLink(classification.url);
}

// 中键等辅助点击也不能让 WebView 打开新窗口。
function handleAuxClick(event: MouseEvent) {
  if (linkOf(event)) event.preventDefault();
}

function openSelectionMenu(event: MouseEvent) {
  const article = event.currentTarget;
  const selection = window.getSelection();
  if (!(article instanceof HTMLElement) || !selection?.rangeCount
    || !article.contains(selection.anchorNode) || !article.contains(selection.focusNode)) return;
  const content = selection.toString();
  if (!content.trim()) return;
  event.preventDefault();
  selectionMenu.value = { x: event.clientX, y: event.clientY, content };
}

onDeactivated(() => { selectionMenu.value = undefined; });
</script>

<template>
  <el-scrollbar class="note-preview">
    <article
      class="project-markdown-preview note-preview__article"
      :aria-label="t('notes.preview.ariaLabel')"
      v-html="html"
      @click="handleClick"
      @auxclick="handleAuxClick"
      @contextmenu="openSelectionMenu"
      @submit.prevent
    />
    <NoteSelectionHandoff v-model="selectionMenu" />
  </el-scrollbar>
</template>
