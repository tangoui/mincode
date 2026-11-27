<script setup lang="ts">
import { Box, Brush, ChromeFilled, Coin, CopyDocument, DataAnalysis, Document, Folder, FolderOpened, Grid, Headset, Monitor, Notebook, Operation, Picture, Setting, Tickets, TopRight, VideoCamera } from "@element-plus/icons-vue";
import { resolveFileIconKind } from "@/domain/ai/file-icon";
import { ElMessage } from "element-plus";
import { computed, shallowRef, watch } from "vue";
import { formatDataBytes } from "@/domain/settings/data-usage";
import { useVueI18n } from "@/i18n/messages";
import { aiFileRevealLabel, openAiLocalPath, readAiImageThumbnail, type AiLocalFile } from "@/services/ai-local-files";
import { copyText } from "@/services/desktop-bridge";

const props = defineProps<{ files: readonly AiLocalFile[] }>();
const { t } = useVueI18n();

const fileIcons = {
  folder: Folder,
  vue: Grid,
  typescript: Tickets,
  javascript: Operation,
  markup: ChromeFilled,
  style: Brush,
  data: DataAnalysis,
  config: Setting,
  markdown: Notebook,
  image: Picture,
  video: VideoCamera,
  audio: Headset,
  database: Coin,
  archive: Box,
  terminal: Monitor,
  code: Operation,
  document: Document,
};

const cards = computed(() => {
  const unique = new Set<string>();
  const result = [];
  for (const file of props.files) {
    if (!file.isFile) continue;
    const key = /^[a-z]:[\\/]/i.test(file.canonicalPath) ? file.canonicalPath.toLowerCase() : file.canonicalPath;
    if (unique.has(key)) continue;
    unique.add(key);
    const name = file.path.slice(file.path.lastIndexOf("/") + 1);
    result.push({ file, key, name: name || file.path, icon: resolveFileIconKind(name) });
    if (result.length === 6) break;
  }
  return result;
});

const thumbnails = shallowRef(new Map<string, string>());
watch(cards, (files, _, onCleanup) => {
  const controller = new AbortController();
  onCleanup(() => controller.abort());
  thumbnails.value = new Map();
  void (async () => {
    for (const { file, key } of files) {
      if (controller.signal.aborted) return;
      const thumbnail = await readAiImageThumbnail(file, controller.signal).catch(() => undefined);
      if (thumbnail && !controller.signal.aborted) {
        thumbnails.value = new Map(thumbnails.value).set(key, thumbnail);
      }
    }
  })();
}, { immediate: true });

async function act(file: AiLocalFile, action: "open" | "reveal" | "copy") {
  try {
    if (action === "copy") await copyText(file.path);
    else await openAiLocalPath(file, action === "reveal");
  } catch {
    ElMessage.error(t(action === "copy" ? "ai.files.copyFailed" : "ai.files.openFailed"));
  }
}
</script>

<template>
  <ul v-if="cards.length" class="ai-message-file-cards" :aria-label="t('ai.files.attachments')">
    <li v-for="{ file, key, name, icon } in cards" :key="key" class="ai-message-file-card">
      <el-image
        v-if="thumbnails.has(key)"
        class="ai-message-file-card__thumbnail"
        :src="thumbnails.get(key)"
        :alt="name"
        :preview-src-list="[thumbnails.get(key)!]"
        preview-teleported
        hide-on-click-modal
        fit="cover"
      />
      <div v-else class="ai-message-file-card__icon" aria-hidden="true">
        <el-icon class="project-file-icon" :class="`project-file-icon--${icon}`"><component :is="fileIcons[icon]" /></el-icon>
      </div>
      <div class="ai-message-file-card__body">
        <div class="ai-message-file-card__name" :title="file.path">{{ name }}</div>
        <div class="ai-message-file-card__size">{{ formatDataBytes(file.size) }}</div>
        <div class="ai-message-file-card__actions">
          <el-button class="ai-message-file-card__open" text size="small" :icon="TopRight" @click="act(file, 'open')">
            {{ t("ai.files.open") }}
          </el-button>
          <el-button
            class="ai-message-file-card__reveal"
            text
            size="small"
            :icon="FolderOpened"
            :title="t(aiFileRevealLabel())"
            :aria-label="t(aiFileRevealLabel())"
            @click="act(file, 'reveal')"
          />
          <el-button
            class="ai-message-file-card__copy"
            text
            size="small"
            :icon="CopyDocument"
            :title="t('ai.files.copyPath')"
            :aria-label="t('ai.files.copyPath')"
            @click="act(file, 'copy')"
          />
        </div>
      </div>
    </li>
  </ul>
</template>
