<script setup lang="ts">
import { computed, type Component } from "vue";
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

const props = withDefaults(
  defineProps<{
    name: string;
    isDir?: boolean;
    expanded?: boolean;
  }>(),
  {
    isDir: false,
    expanded: false,
  }
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
  <el-icon
    class="project-file-icon"
    :class="`project-file-icon--${iconKind}`"
    aria-hidden="true"
  >
    <component :is="iconComponent" />
  </el-icon>
</template>
