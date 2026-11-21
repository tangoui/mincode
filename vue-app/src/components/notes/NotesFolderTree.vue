<script setup lang="ts">
// 笔记页「全部」视图：文件夹与笔记树。拖放改变位置，右键菜单提供新建、重命名、移动、导出与删除。
// 树的增删改都经 notes store；导出与在文件夹中新建笔记交给父组件（需要原生目录选择与选中切换）。
import { computed, nextTick, ref, watch } from "vue";
import { ElMessage, ElMessageBox, type TreeInstance } from "element-plus";
import { Document, Folder } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { claimContextMenuKey, contextMenuAnchor } from "@/components/ui/app-context-menu";
import {
  buildNoteTreeNodes,
  canDropNoteNode,
  folderNodeKey,
  noteDropTarget,
  noteNodeKey,
  type NoteDropType,
  type NotesExportTarget,
  type NoteTreeNode,
} from "@/domain/notes/folder-tree";
import type { NoteFolder } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import { noteErrorMessage } from "@/services/notes-service";
import { useNotesStore } from "@/stores/notes";

const props = defineProps<{ selectedId: string; activeFolderId: string }>();
const emit = defineEmits<{
  (e: "select", noteId: string): void;
  (e: "activate-folder", folderId: string): void;
  (e: "create-note", folderId: string): void;
  (e: "export", target: NotesExportTarget): void;
}>();

const { t } = useVueI18n();
const store = useNotesStore();

const treeRef = ref<TreeInstance>();
const treeRoot = ref<HTMLElement | null>(null);
/** 「全部折叠」时自增以重建 el-tree，展开状态清零。 */
const treeGeneration = ref(0);
const expandedKeys = ref<string[]>([]);

const nodes = computed(() => (store.tree ? buildNoteTreeNodes(store.tree) : []));
const notesById = computed(() => new Map((store.tree?.notes ?? []).map((note) => [note.id, note])));
const folderById = computed(() => new Map((store.tree?.folders ?? []).map((folder) => [folder.id, folder])));
const currentKey = computed(() => {
  if (props.activeFolderId) return folderNodeKey(props.activeFolderId);
  return props.selectedId ? noteNodeKey(props.selectedId) : undefined;
});

function noteTitle(id: string) {
  return notesById.value.get(id)?.title || t("notes.editor.untitled");
}

function nodeLabel(node: NoteTreeNode) {
  return node.kind === "folder" ? node.name : noteTitle(node.id);
}

function expand(key: string) {
  if (!expandedKeys.value.includes(key)) expandedKeys.value = [...expandedKeys.value, key];
}

function syncCurrent() {
  void nextTick(() => treeRef.value?.setCurrentKey(currentKey.value ?? null, false));
}

// 外部选中（搜索结果、收藏、关联抽屉）时展开所在文件夹并高亮。
watch(
  () => props.selectedId,
  (id) => {
    const folderId = id ? notesById.value.get(id)?.folderId : "";
    if (folderId) expand(folderNodeKey(folderId));
  },
  { immediate: true },
);
watch([currentKey, nodes], syncCurrent);

function onNodeClick(data: NoteTreeNode) {
  if (data.kind === "note") emit("select", data.id);
  else emit("activate-folder", data.id);
}

function onExpand(data: NoteTreeNode) {
  expand(data.key);
}

function onCollapse(data: NoteTreeNode) {
  expandedKeys.value = expandedKeys.value.filter((key) => key !== data.key);
}

function collapseAll() {
  expandedKeys.value = [];
  treeGeneration.value++;
}

defineExpose({ collapseAll });

// ---- 拖放 ----

type ElDropType = "prev" | "inner" | "next";
interface TreeNodeLike {
  data: NoteTreeNode;
}

function allowDrop(dragging: TreeNodeLike, drop: TreeNodeLike, type: ElDropType) {
  const mapped: NoteDropType = type === "prev" ? "before" : type === "next" ? "after" : "inner";
  return canDropNoteNode(dragging.data.kind, drop.data.kind, mapped);
}

async function onDrop(dragging: TreeNodeLike, drop: TreeNodeLike, type: NoteDropType) {
  const tree = store.tree;
  if (!tree) return;
  const dragged = dragging.data;
  const target = noteDropTarget(tree, dragged, drop.data, type);
  try {
    if (!target) throw new Error("note_invalid_request");
    if (dragged.kind === "note") await store.moveNote(dragged.id, target.parentId, target.position);
    else await store.moveFolder(dragged.id, target.parentId, target.position);
    if (target.parentId) expand(folderNodeKey(target.parentId));
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
    // el-tree 已在本地挪动了节点，失败时以后端为准重新加载。
    await store.loadTree().catch(() => undefined);
  }
}

// ---- 右键菜单 ----

const menu = ref<{ open: boolean; x: number; y: number; node: NoteTreeNode | null }>({ open: false, x: 0, y: 0, node: null });

function onContextMenu(event: MouseEvent, data: NoteTreeNode) {
  event.preventDefault();
  menu.value = { open: true, x: event.clientX, y: event.clientY, node: data };
}

/**
 * 键盘打开右键菜单（ContextMenu 键 / Shift+F10）：作用于聚焦的树行，没有聚焦行时用当前高亮节点，
 * 菜单锚定在该行下方；AppContextMenu 关闭时把焦点还给打开前聚焦的行。
 */
function onTreeKeydown(event: KeyboardEvent) {
  if (!claimContextMenuKey(event)) return;
  const target = event.target instanceof Element ? event.target : null;
  let row = target?.closest<HTMLElement>('[role="treeitem"][data-key]') ?? null;
  const key = row?.dataset.key ?? currentKey.value;
  if (!key) return;
  if (!row) row = treeRoot.value?.querySelector<HTMLElement>(`[role="treeitem"][data-key="${CSS.escape(key)}"]`) ?? null;
  const data = treeRef.value?.getNode(key)?.data as NoteTreeNode | undefined;
  if (!data) return;
  const anchor = contextMenuAnchor(event, row?.querySelector(".el-tree-node__content") ?? row ?? undefined);
  menu.value = { open: true, ...anchor, node: data };
}

function closeMenu() {
  menu.value = { ...menu.value, open: false };
}

function isCancel(error: unknown) {
  return error === "cancel" || error === "close";
}

async function promptName(title: string, label: string, value = "") {
  try {
    const result = await ElMessageBox.prompt(label, title, {
      inputValue: value,
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
      inputValidator: (input: string) => (input.trim() ? true : t("notes.tree.nameRequired")),
    });
    return result.value.trim();
  } catch (error) {
    if (!isCancel(error)) throw error;
    return null;
  }
}

async function confirmDelete(title: string, message: string) {
  try {
    await ElMessageBox.confirm(message, title, {
      type: "warning",
      confirmButtonText: t("notes.tree.delete"),
      cancelButtonText: t("common.cancel"),
      confirmButtonClass: "el-button--danger",
    });
    return true;
  } catch (error) {
    if (!isCancel(error)) throw error;
    return false;
  }
}

async function run(action: () => Promise<void>) {
  try {
    await action();
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  }
}

async function createSubfolder(parent: NoteFolder) {
  const name = await promptName(t("notes.tree.newSubfolder"), t("notes.tree.folderName"));
  if (!name) return;
  await store.createFolder(name, parent.id);
  expand(folderNodeKey(parent.id));
}

async function renameFolder(folder: NoteFolder) {
  const name = await promptName(t("notes.tree.renameFolderTitle"), t("notes.tree.folderName"), folder.name);
  if (!name || name === folder.name) return;
  await store.renameFolder(folder.id, name);
}

async function deleteFolder(folder: NoteFolder) {
  if (!(await confirmDelete(t("notes.tree.deleteFolderTitle"), t("notes.tree.deleteFolderConfirm", { name: folder.name })))) return;
  await store.deleteFolder(folder.id);
  if (props.activeFolderId === folder.id) emit("activate-folder", "");
}

async function renameNote(id: string) {
  const current = notesById.value.get(id)?.title ?? "";
  const title = await promptName(t("notes.tree.renameNoteTitle"), t("notes.tree.noteTitle"), current);
  if (!title || title === current) return;
  await store.openNote(id);
  store.editNote(id, { title });
  await store.flushNote(id);
  const error = store.saveErrors[id];
  if (error) throw new Error(error);
}

async function deleteNote(id: string) {
  if (!(await confirmDelete(t("notes.tree.deleteNoteTitle"), t("notes.tree.deleteNoteConfirm", { name: noteTitle(id) })))) return;
  await store.deleteNote(id);
}

function onMenu(command: "new-note" | "new-subfolder" | "rename" | "move" | "export" | "delete") {
  const node = menu.value.node;
  closeMenu();
  if (!node) return;
  if (node.kind === "folder") {
    const folder = folderById.value.get(node.id);
    if (!folder) return;
    if (command === "new-note") emit("create-note", folder.id);
    else if (command === "new-subfolder") void run(() => createSubfolder(folder));
    else if (command === "rename") void run(() => renameFolder(folder));
    else if (command === "move") openMoveDialog("folder", folder.id);
    else if (command === "export") emit("export", { folderId: folder.id });
    else if (command === "delete") void run(() => deleteFolder(folder));
    return;
  }
  if (command === "rename") void run(() => renameNote(node.id));
  else if (command === "move") openMoveDialog("note", node.id);
  else if (command === "export") emit("export", { noteIds: [node.id] });
  else if (command === "delete") void run(() => deleteNote(node.id));
}

// ---- 移动到… ----

const ROOT_VALUE = "root";
interface FolderOption {
  value: string;
  label: string;
  children: FolderOption[];
}

const moveDialog = ref<{ visible: boolean; kind: "note" | "folder"; id: string; target: string; busy: boolean }>({
  visible: false,
  kind: "note",
  id: "",
  target: ROOT_VALUE,
  busy: false,
});

// 移动文件夹时不列出它自己（其子文件夹在它下面，随之排除），后端也会拒绝移动到自身或子孙。
const folderOptions = computed<FolderOption[]>(() => {
  const excluded = moveDialog.value.kind === "folder" ? moveDialog.value.id : "";
  const toOptions = (list: NoteTreeNode[]): FolderOption[] =>
    list.flatMap((node) =>
      node.kind === "folder" && node.id !== excluded ? [{ value: node.id, label: node.name, children: toOptions(node.children) }] : []);
  return [{ value: ROOT_VALUE, label: t("notes.tree.root"), children: toOptions(nodes.value) }];
});

function openMoveDialog(kind: "note" | "folder", id: string) {
  const parent = kind === "note" ? notesById.value.get(id)?.folderId : folderById.value.get(id)?.parentId;
  moveDialog.value = { visible: true, kind, id, target: parent || ROOT_VALUE, busy: false };
}

async function confirmMove() {
  const tree = store.tree;
  const { kind, id, target } = moveDialog.value;
  if (!tree || !id) return;
  const parentId = target === ROOT_VALUE ? "" : target;
  const siblings = kind === "note"
    ? tree.notes.filter((note) => note.folderId === parentId && note.id !== id)
    : tree.folders.filter((folder) => folder.parentId === parentId && folder.id !== id);
  const position = siblings.reduce((max, item) => Math.max(max, item.position), 0) + 1;
  moveDialog.value.busy = true;
  try {
    if (kind === "note") await store.moveNote(id, parentId, position);
    else await store.moveFolder(id, parentId, position);
    if (parentId) expand(folderNodeKey(parentId));
    moveDialog.value.visible = false;
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  } finally {
    moveDialog.value.busy = false;
  }
}
</script>

<template>
  <div ref="treeRoot" class="notes-folder-tree" @keydown="onTreeKeydown">
    <el-tree
      :key="treeGeneration"
      ref="treeRef"
      class="notes-folder-tree__tree"
      :data="nodes"
      node-key="key"
      :props="{ label: 'name', children: 'children' }"
      :current-node-key="currentKey"
      :default-expanded-keys="expandedKeys"
      :allow-drop="allowDrop"
      :aria-label="t('notes.tree.label')"
      draggable
      highlight-current
      @node-click="onNodeClick"
      @node-expand="onExpand"
      @node-collapse="onCollapse"
      @node-drop="onDrop"
      @node-contextmenu="onContextMenu"
    >
      <template #default="{ data }">
        <span
          class="notes-folder-tree__node"
          :class="`notes-folder-tree__node--${data.kind}`"
          :title="nodeLabel(data)"
        >
          <el-icon class="notes-folder-tree__icon" aria-hidden="true">
            <Folder v-if="data.kind === 'folder'" />
            <Document v-else />
          </el-icon>
          <span class="notes-folder-tree__label">{{ nodeLabel(data) }}</span>
          <span
            v-if="data.kind === 'folder'"
            class="notes-folder-tree__count"
            :title="t('notes.tree.noteCount', { count: data.count })"
            :aria-label="t('notes.tree.noteCount', { count: data.count })"
          >{{ data.count }}</span>
        </span>
      </template>
    </el-tree>

    <AppContextMenu
      :open="menu.open"
      :x="menu.x"
      :y="menu.y"
      :width="168"
      :aria-label="t('notes.tree.menuLabel')"
      @close="closeMenu"
    >
      <template v-if="menu.node?.kind === 'folder'">
        <el-dropdown-item @click="onMenu('new-note')">{{ t("notes.sidebar.newNote") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('new-subfolder')">{{ t("notes.tree.newSubfolder") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('rename')">{{ t("notes.tree.rename") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('move')">{{ t("notes.tree.moveTo") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('export')">{{ t("notes.sidebar.export") }}</el-dropdown-item>
        <el-dropdown-item divided class="notes-folder-tree__danger" @click="onMenu('delete')">{{ t("notes.tree.delete") }}</el-dropdown-item>
      </template>
      <template v-else-if="menu.node">
        <el-dropdown-item @click="onMenu('rename')">{{ t("notes.tree.rename") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('move')">{{ t("notes.tree.moveTo") }}</el-dropdown-item>
        <el-dropdown-item @click="onMenu('export')">{{ t("notes.sidebar.export") }}</el-dropdown-item>
        <el-dropdown-item divided class="notes-folder-tree__danger" @click="onMenu('delete')">{{ t("notes.tree.delete") }}</el-dropdown-item>
      </template>
    </AppContextMenu>

    <el-dialog v-model="moveDialog.visible" :title="t(moveDialog.kind === 'folder' ? 'notes.tree.moveFolderTitle' : 'notes.tree.moveTitle')" width="360px" append-to-body>
      <el-form label-position="top" @submit.prevent="confirmMove">
        <el-form-item :label="t('notes.tree.moveTarget')">
          <el-tree-select
            v-model="moveDialog.target"
            class="notes-folder-tree__move-select"
            :data="folderOptions"
            node-key="value"
            check-strictly
            default-expand-all
            :render-after-expand="false"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="moveDialog.visible = false">{{ t("common.cancel") }}</el-button>
        <el-button type="primary" :loading="moveDialog.busy" @click="confirmMove">{{ t("common.confirm") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
