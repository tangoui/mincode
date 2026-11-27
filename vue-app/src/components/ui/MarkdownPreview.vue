<script setup lang="ts">
import { ElMessage } from "element-plus";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { useVueI18n } from "@/i18n/messages";
import { classifyMarkdownLinkHref } from "@/domain/markdown-links";
import { openWorkspaceLink } from "@/services/markdown-link-navigation";
import { cachedMarkdownHtml, renderMarkdownHtml } from "@/services/markdown-render";
import { createStreamingMarkdownRenderer, type StreamingMarkdownRenderer } from "@/services/streaming-markdown-render";
import { copyText, isDesktopRuntime } from "@/services/desktop-bridge";
import { aiFileRevealLabel, openAiLocalPath, type AiLocalFile } from "@/services/ai-local-files";
import { linkAiMarkdownFiles } from "@/services/ai-markdown-file-links";

// Streaming content changes up to once per frame; re-parsing the whole
// document each time is quadratic, so bursts render at most this often and a
// trailing render always shows the final content.
const MIN_RENDER_INTERVAL_MS = 120;

const props = withDefaults(defineProps<{
  content: string;
  inline?: boolean;
  /** Adds a language label and a copy button to every fenced code block. */
  codeCopy?: boolean;
  /**
   * The content is a reply still being generated: only its last block is re-rendered, so it shows
   * as formatted markdown while it grows. When this turns false the full document is rendered once.
   */
  streaming?: boolean;
  /** Present on AI replies, including replies with no bound working directory. */
  aiFileDirectory?: string;
}>(), {
  inline: false,
  codeCopy: false,
  streaming: false,
});
const { t } = useVueI18n();
const emit = defineEmits<{ localFiles: [files: readonly AiLocalFile[]] }>();
const aiFileLinks = computed(() => props.aiFileDirectory !== undefined && isDesktopRuntime());

let streamingRenderer: StreamingMarkdownRenderer | undefined;
function streamedHtml(content: string) {
  streamingRenderer ??= createStreamingMarkdownRenderer();
  return streamingRenderer.render(content);
}
const renderedHtml = ref(props.streaming ? streamedHtml(props.content) : renderMarkdownHtml(props.content, { aiFileLinks: aiFileLinks.value }));
let lastRenderAt = performance.now();
let trailingRender: ReturnType<typeof setTimeout> | undefined;

function cancelTrailingRender() {
  if (trailingRender === undefined) return;
  clearTimeout(trailingRender);
  trailingRender = undefined;
}

function renderContent(memoize: boolean) {
  cancelTrailingRender();
  renderedHtml.value = props.streaming ? streamedHtml(props.content) : renderMarkdownHtml(props.content, { memoize, aiFileLinks: aiFileLinks.value });
  lastRenderAt = performance.now();
}

// The finished reply is rendered once as a whole document, which also matches what reopening the
// conversation shows; the per-message block cache is no longer needed.
watch(
  () => props.streaming,
  (streaming) => {
    if (streaming) return;
    streamingRenderer = undefined;
    renderContent(true);
  },
);

watch(
  () => props.content,
  (content) => {
    const cached = props.streaming || aiFileLinks.value ? undefined : cachedMarkdownHtml(content);
    if (cached !== undefined) {
      cancelTrailingRender();
      renderedHtml.value = cached;
      return;
    }
    const wait = MIN_RENDER_INTERVAL_MS - (performance.now() - lastRenderAt);
    if (wait <= 0 && trailingRender === undefined) {
      renderContent(true);
      return;
    }
    // Throttled content is still changing, so it is not worth memoizing.
    trailingRender ??= setTimeout(() => renderContent(false), Math.max(0, wait));
  },
);

onBeforeUnmount(cancelTrailingRender);

const article = ref<HTMLElement>();
const COPIED_RESET_MS = 1_500;
const linked = shallowRef<Awaited<ReturnType<typeof linkAiMarkdownFiles>>>();
watch(linked, (result) => emit("localFiles", result?.links ?? []));
const displayHtml = computed(() => linked.value?.html ?? renderedHtml.value);
const fileMenu = shallowRef<{ button: HTMLButtonElement; file: AiLocalFile; x: number; y: number }>();

function closeFileMenu() {
  fileMenu.value?.button.setAttribute("aria-expanded", "false");
  fileMenu.value = undefined;
}

watch(
  () => [aiFileLinks.value, props.streaming, props.aiFileDirectory, renderedHtml.value, t("ai.files.menu")] as const,
  ([enabled, streaming, directory, html, menuLabel], _, onCleanup) => {
    closeFileMenu();
    linked.value = undefined;
    if (!enabled || streaming) return;
    const controller = new AbortController();
    onCleanup(() => controller.abort());
    void linkAiMarkdownFiles(html, directory ?? "", menuLabel, controller.signal).then((result) => {
      if (!controller.signal.aborted) linked.value = result;
    }).catch(() => undefined);
  },
  { immediate: true },
);

// v-html replaces the rendered DOM on every change, so the code block headers are
// added again after each render; clicks are handled by delegation in handleClick.
function decorateCodeBlocks() {
  const root = article.value;
  if (!props.codeCopy || !root) return;
  for (const pre of root.querySelectorAll<HTMLPreElement>("pre")) {
    if (pre.parentElement?.classList.contains("markdown-code-block")) continue;
    const code = pre.querySelector("code");
    const language = code?.className.match(/language-([\w+#.-]+)/)?.[1] ?? "";
    const block = document.createElement("div");
    block.className = "markdown-code-block";
    const header = document.createElement("div");
    header.className = "markdown-code-block__header";
    const label = document.createElement("span");
    label.className = "markdown-code-block__language";
    label.textContent = language || t("project.markdown.codeBlock");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "markdown-code-block__copy";
    button.textContent = t("project.markdown.copyCode");
    button.setAttribute("aria-label", t("project.markdown.copyCodeLabel", { language: language || t("project.markdown.codeBlock") }));
    header.append(label, button);
    pre.replaceWith(block);
    block.append(header, pre);
  }
}

watch(displayHtml, () => void nextTick(decorateCodeBlocks));
onMounted(decorateCodeBlocks);

async function copyCodeBlock(button: HTMLButtonElement) {
  const code = button.closest(".markdown-code-block")?.querySelector("pre")?.textContent ?? "";
  try {
    await copyText(code);
    button.textContent = t("project.markdown.codeCopied");
    setTimeout(() => {
      if (button.isConnected) button.textContent = t("project.markdown.copyCode");
    }, COPIED_RESET_MS);
  } catch {
    ElMessage.error(t("project.markdown.copyCodeFailed"));
  }
}

async function runFileAction(file: AiLocalFile, action: "open" | "reveal" | "copy") {
  closeFileMenu();
  try {
    if (action === "copy") await copyText(file.path);
    else await openAiLocalPath(file, action === "reveal");
  } catch {
    ElMessage.error(t(action === "copy" ? "ai.files.copyFailed" : "ai.files.openFailed"));
  }
}

function runMenuAction(action: "reveal" | "copy") {
  if (fileMenu.value) void runFileAction(fileMenu.value.file, action);
}

// Delegate actions without navigating the WebView away from the conversation.
function handleClick(event: MouseEvent) {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return;
  const copyButton = target.closest<HTMLButtonElement>(".markdown-code-block__copy");
  if (copyButton) {
    void copyCodeBlock(copyButton);
    return;
  }
  const menuButton = target.closest<HTMLButtonElement>("button[data-ai-file-menu]");
  if (menuButton) {
    const file = linked.value?.links[Number(menuButton.dataset.aiFileMenu)];
    if (!file) return;
    const { left, bottom } = menuButton.getBoundingClientRect();
    menuButton.focus({ preventScroll: true });
    menuButton.setAttribute("aria-expanded", "true");
    fileMenu.value = { button: menuButton, file, x: left, y: bottom };
    return;
  }
  const anchor = target.closest("a");
  if (!anchor) return;
  if (anchor.hasAttribute("data-ai-file-link")) {
    event.preventDefault();
    const file = linked.value?.links[Number(anchor.dataset.aiFileLink)];
    if (file) void runFileAction(file, "open");
    return;
  }
  const classification = classifyMarkdownLinkHref(anchor.getAttribute("href"));
  if (classification.kind === "anchor") return;
  event.preventDefault();
  if (classification.kind === "http") void openWorkspaceLink(classification.url);
}
</script>

<template>
  <article
    v-if="inline"
    ref="article"
    class="project-markdown-preview"
    v-html="displayHtml"
    @click="handleClick"
  />
  <el-scrollbar v-else class="project-markdown-scroll">
    <article ref="article" class="project-markdown-preview" v-html="displayHtml" @click="handleClick" />
  </el-scrollbar>
  <AppContextMenu
    v-if="fileMenu"
    open
    :x="fileMenu.x"
    :y="fileMenu.y"
    popper-class="ai-markdown-file-menu"
    :aria-label="t('ai.files.menu')"
    @close="closeFileMenu"
  >
    <el-dropdown-item class="ai-markdown-file-reveal" @click="runMenuAction('reveal')">
      {{ t(aiFileRevealLabel()) }}
    </el-dropdown-item>
    <el-dropdown-item @click="runMenuAction('copy')">{{ t('ai.files.copyPath') }}</el-dropdown-item>
  </AppContextMenu>
</template>
