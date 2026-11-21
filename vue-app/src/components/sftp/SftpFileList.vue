<script setup lang="ts" generic="T extends SftpFileListEntry">
import { computed, ref } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { Document, Folder } from "@element-plus/icons-vue";
import type { SftpFileListCells, SftpFileListEntry } from "./sftp-file-list";
import { useVirtualRows } from "./use-virtual-rows";

// Rendering the rows here (not in the workspace view) keeps unrelated view updates,
// such as transfer progress, from re-rendering every file row.
const props = defineProps<{
  entries: readonly T[];
  selectedPath?: string;
  renamingPath?: string;
  renameInputClass?: string;
  describe: (item: T) => SftpFileListCells;
}>();

const renameValue = defineModel<string>("renameValue", { default: "" });

const emit = defineEmits<{
  select: [item: T];
  open: [item: T];
  rowDragstart: [event: DragEvent, item: T];
  directoryDragover: [event: DragEvent, item: T];
  directoryDrop: [event: DragEvent, item: T];
  rowContextmenu: [event: MouseEvent, item: T];
  saveRename: [];
  cancelRename: [];
}>();

const scroller = ref<ScrollbarInstance>();
const { rows, paddingTop, paddingBottom, onScroll, scrollToIndex } = useVirtualRows({
  items: () => props.entries,
  scroller,
  rowSelector: ".sftp-file-row",
  estimatedRowHeight: 31,
});

const renderedRows = computed(() => rows.value.map((item) => ({ item, cells: props.describe(item) })));

function onDirectoryDragover(event: DragEvent, item: T) {
  if (item.isDir) emit("directoryDragover", event, item);
}

function onDirectoryDrop(event: DragEvent, item: T) {
  if (item.isDir) emit("directoryDrop", event, item);
}

/** Brings a row into the rendered window, e.g. before focusing its inline rename input. */
function scrollToPath(path: string) {
  scrollToIndex(props.entries.findIndex((item) => item.path === path));
}

defineExpose({ scrollToPath });
</script>

<template>
  <el-scrollbar ref="scroller" class="sftp-file-list" @scroll="onScroll">
    <div v-if="paddingTop" aria-hidden="true" :style="{ height: `${paddingTop}px` }" />
    <div
      v-for="{ item, cells } in renderedRows"
      :key="item.path"
      class="sftp-file-row"
      :class="{ 'is-active': selectedPath === item.path }"
      :role="renamingPath === item.path ? undefined : 'button'"
      :tabindex="renamingPath === item.path ? undefined : 0"
      draggable="true"
      @click="emit('select', item)"
      @keydown.enter.prevent="emit('select', item)"
      @keydown.space.prevent="emit('select', item)"
      @dblclick="emit('open', item)"
      @dragstart="emit('rowDragstart', $event, item)"
      @dragover="onDirectoryDragover($event, item)"
      @drop="onDirectoryDrop($event, item)"
      @contextmenu.prevent="emit('rowContextmenu', $event, item)"
    >
      <span class="sftp-file-row__icon">
        <el-icon><Folder v-if="item.isDir" /><Document v-else /></el-icon>
      </span>
      <el-input
        v-if="renamingPath === item.path"
        v-model="renameValue"
        class="app-inline-rename sftp-file-rename"
        :class="renameInputClass"
        maxlength="255"
        @click.stop
        @mousedown.stop
        @dblclick.stop
        @keydown.enter.stop.prevent="emit('saveRename')"
        @keydown.esc.stop.prevent="emit('cancelRename')"
        @keydown.space.stop
        @blur="emit('saveRename')"
      />
      <b v-else>{{ item.name }}</b>
      <small :title="cells.detailTitle">{{ cells.detail }}</small>
      <small>{{ cells.size }}</small>
    </div>
    <div v-if="paddingBottom" aria-hidden="true" :style="{ height: `${paddingBottom}px` }" />
    <slot />
  </el-scrollbar>
</template>
