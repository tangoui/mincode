<script setup lang="ts">
import { computed, watch, type Component } from "vue";
import {
  Box,
  Brush,
  ChromeFilled,
  Coin,
  DataAnalysis,
  Document,
  Folder,
  FolderOpened,
  Grid,
  Headset,
  Monitor,
  Notebook,
  Operation,
  Picture,
  Setting,
  Tickets,
  VideoCamera,
} from "@element-plus/icons-vue";
import {
  resolveProjectFileIconKind,
  type ProjectFileIconKind,
} from "@/domain/projects/file-icon";
import { useFileIconThemeStore } from "@/stores/file-icon-theme";

const props = withDefaults(
  defineProps<{
    name: string;
    isDir?: boolean;
    expanded?: boolean;
    /** A project root folder (uses rootFolder icons of a plugin theme). */
    root?: boolean;
  }>(),
  {
    isDir: false,
    expanded: false,
    root: false,
  }
);

// Plugin file icon themes (contributes.fileIcons). Icons are host-sanitized
// SVG/PNG served as blob: URLs and rendered ONLY in image context (<img> or
// a CSS mask). Never inject icon bytes as markup: the main WebView has no CSP.
const fileIconThemes = useFileIconThemeStore();
const pluginIcon = computed(() =>
  fileIconThemes.resolve({ name: props.name, isDir: props.isDir, expanded: props.expanded, root: props.root })
);
watch(
  pluginIcon,
  (icon) => {
    if (icon) fileIconThemes.ensureIcon(icon.pluginId, icon.key);
  },
  { immediate: true }
);
const pluginIconURL = computed(() =>
  pluginIcon.value ? fileIconThemes.iconURL(pluginIcon.value.pluginId, pluginIcon.value.key) ?? null : null
);
const maskStyle = computed(() =>
  pluginIconURL.value ? { maskImage: `url("${pluginIconURL.value}")`, WebkitMaskImage: `url("${pluginIconURL.value}")` } : undefined
);

const iconKind = computed(() => resolveProjectFileIconKind(props.name, props.isDir));

const iconByKind: Readonly<Record<Exclude<ProjectFileIconKind, "folder">, Component>> = {
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

const iconComponent = computed<Component>(() => {
  if (iconKind.value === "folder") return props.expanded ? FolderOpened : Folder;
  return iconByKind[iconKind.value];
});
</script>

<template>
  <span
    v-if="pluginIconURL && pluginIcon?.monochrome"
    class="project-file-icon project-file-icon__mask"
    :style="maskStyle"
    aria-hidden="true"
  />
  <img
    v-else-if="pluginIconURL"
    class="project-file-icon project-file-icon__img"
    :src="pluginIconURL"
    alt=""
    aria-hidden="true"
    draggable="false"
  />
  <el-icon
    v-else
    class="project-file-icon"
    :class="`project-file-icon--${iconKind}`"
    aria-hidden="true"
  >
    <component :is="iconComponent" />
  </el-icon>
</template>
