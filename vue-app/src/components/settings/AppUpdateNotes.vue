<script setup lang="ts">
import { computed } from "vue";
import { classifyMarkdownLinkHref } from "@/domain/markdown-links";
import { openWorkspaceLink } from "@/services/markdown-link-navigation";
import { renderMarkdownHtml } from "@/services/markdown-render";

// Release notes come from the update feed: render them as sanitized markdown.
const props = defineProps<{ notes: string }>();
const html = computed(() => renderMarkdownHtml(props.notes));

// Following an anchor would navigate the main webview, so every link is intercepted; only http(s) opens.
function handleClick(event: MouseEvent) {
  const anchor = event.target instanceof Element ? event.target.closest("a") : null;
  if (!anchor) return;
  event.preventDefault();
  const link = classifyMarkdownLinkHref(anchor.getAttribute("href"));
  if (link.kind === "http") void openWorkspaceLink(link.url);
}
</script>

<template>
  <div class="settings-update-notes" v-html="html" @click="handleClick" />
</template>
