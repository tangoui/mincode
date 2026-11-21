<script setup lang="ts">
/**
 * A lazy variable tree of one debug session: the given root nodes, children
 * loaded from the adapter when a node is expanded. Shared by the Variables
 * pane and the Evaluate dialog. A new `nodes` array rebuilds the tree; loads
 * started for an older one, or after the program moved on, resolve empty.
 * Nodes whose path is in `restorePaths` expand on their own as they appear,
 * within a budget per rebuild (RESTORE_EXPAND_LIMIT nodes, RESTORE_EXPAND_DEPTH
 * levels); `restored` tells when those loads are over (the owner can scroll
 * back then); `expand` / `collapse` report the user's toggles by path.
 * When `live` turns off (the program runs) the tree stays as it is, greyed,
 * until the owner passes new nodes: nodes no longer expand or collapse (their
 * references are gone) and values cannot be edited; Copy still works.
 * When `editable`, a variable's value can be changed (DAP setVariable): a
 * double-click on the value or "Set Value" in its context menu opens an
 * inline input, Enter sets the value, Esc (or leaving the input) cancels.
 * The context menu also opens from the keyboard (ContextMenu key or
 * Shift+F10 on the focused row). The row shows the adapter's answer at once; the owner reads the tree again
 * (the store's variablesVersion) so other rows showing it follow. A rejected
 * value keeps the input open with what was typed.
 * While the session is paused and its adapter declares
 * supportsDataBreakpoints, a variable's context menu also offers field
 * breakpoints ("Break When Value Changes", and "… Is Read" when the adapter
 * lists read access): the adapter is asked (dataBreakpointInfo) only when the
 * menu opens, and a variable it gives no id offers none. A set one is
 * checked; choosing it again removes it.
 */
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Check, Close, CopyDocument, Edit } from "@element-plus/icons-vue";
import { ElMessage, type InputInstance, type LoadFunction, type TreeInstance } from "element-plus";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { claimContextMenuKey, contextMenuAnchor, isContextMenuKey } from "@/components/ui/app-context-menu";
import { offeredAccessTypes, type DataBreakpointInfo } from "@/domain/debug/data-breakpoints";
import type { DataBreakpointAccessType } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { debugErrorText } from "@/domain/debug/errors";
import {
  displayValue,
  RESTORE_EXPAND_DEPTH,
  RESTORE_EXPAND_LIMIT,
  siblingIndexes,
  titleValue,
  variableNode,
  type DebugVariableNode,
} from "@/domain/debug/variable-tree";
import { copyText } from "@/services/desktop-bridge";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const props = defineProps<{
  sessionId: string;
  nodes: DebugVariableNode[];
  /** Paths to expand again as their nodes load (see the budget above). */
  restorePaths?: string[];
  /** Children can be loaded (the session is paused). */
  live: boolean;
  /** Variable values can be changed (paused, and the adapter supports setVariable). */
  editable: boolean;
  emptyText?: string;
}>();
const emit = defineEmits<{
  "remove-watch": [index: number];
  expand: [path: string];
  collapse: [path: string];
  /** The nodes of this rebuild that expand on their own have loaded their children. */
  restored: [];
}>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

const tree = ref<TreeInstance>();
/** The variable whose value is being edited (by path) and the text typed. */
const editing = ref<{ path: string; draft: string } | null>(null);
/** A setVariable request is in flight (the input waits for it). */
const saving = ref(false);
const editInput = ref<InputInstance>();
/**
 * The open context menu: the variable's path and name, where it opened,
 * whether it offers Set Value, and the adapter's field breakpoint answer
 * (once it came; null while asked or when the menu offers none).
 */
const menu = ref<{ path: string; name: string; x: number; y: number; edit: boolean; field: DataBreakpointInfo | null } | null>(null);
/** Bumped per context menu opened: a field answer for an older one is dropped. */
let menuSeq = 0;

/** Bumped on new nodes to rebuild the lazy tree (its root load runs once per instance). */
const generation = ref(0);
/** Bumped on new nodes and whenever `live` changes: pending child loads are dropped. */
let loads = 0;
/** What this rebuild still expands on its own. */
let restore = new Set(props.restorePaths ?? []);
let restoreBudget = RESTORE_EXPAND_LIMIT;
/** A node of the el-tree model (not exported by Element Plus by name). */
type TreeNode = Parameters<LoadFunction>[0];
/** Nodes this rebuild expanded on its own whose children are still loading. */
let restoring = new Set<TreeNode>();
watch(
  () => [props.nodes, props.live] as const,
  ([nodes], [previousNodes]) => {
    loads += 1;
    editing.value = null;
    closeMenu();
    // Only new nodes rebuild: a tree whose program runs on stays as it was.
    if (nodes === previousNodes) return;
    generation.value += 1;
    restore = new Set(props.restorePaths ?? []);
    restoreBudget = RESTORE_EXPAND_LIMIT;
    restoring = new Set();
  }
);

/** Expands the just-loaded children of `parent` that are to be restored. */
function restoreChildren(parent: TreeNode) {
  // Without a pause there are no children to show: stay collapsed.
  if (!props.live) return;
  for (const child of parent.childNodes) {
    if (restoreBudget <= 0) return;
    const data = child.data as DebugVariableNode;
    if (child.isLeaf || child.level > RESTORE_EXPAND_DEPTH || !restore.has(data.path)) continue;
    restoreBudget -= 1;
    if (!child.loaded) restoring.add(child);
    child.expand();
  }
}

/** `node`'s children are in (and its own restores started): the restore may be over. */
function settled(node: TreeNode) {
  if (restoring.delete(node) && !restoring.size) emit("restored");
}

const loadNode: LoadFunction = (node, resolve) => {
  if (node.level === 0) {
    resolve(props.nodes);
    restoreChildren(node);
    if (!restoring.size) emit("restored");
    return;
  }
  const data = node.data as DebugVariableNode;
  if (!(data.reference > 0) || !props.live) {
    resolve([]);
    settled(node);
    return;
  }
  const token = loads;
  void sessions.loadVariables(props.sessionId, data.reference).then((variables) => {
    // A stale load (the program moved on) shows nothing rather than old values.
    if (token !== loads) {
      resolve([]);
      settled(node);
      return;
    }
    const indexes = siblingIndexes(variables.map((variable) => variable.name));
    resolve(variables.map((variable, position) => variableNode(variable, data.path, indexes[position]!, data.reference)));
    restoreChildren(node);
    settled(node);
  });
};

/**
 * Only variables read from a container (scopes, watches and Evaluate results
 * have no setVariable target) while the tree is editable and live.
 */
function canEdit(data: DebugVariableNode): boolean {
  return props.editable && props.live && data.kind === "variable" && !data.error && data.parentReference > 0 && data.value !== undefined;
}

/**
 * A variable read from a container while the tree is live and the session
 * is paused with supportsDataBreakpoints: its menu asks for a field breakpoint.
 */
function canWatch(data: DebugVariableNode): boolean {
  if (!props.live || data.kind !== "variable" || data.error || !(data.parentReference > 0)) return false;
  return sessions.canSetDataBreakpoints(sessions.sessions.find((view) => view.id === props.sessionId));
}

function startEdit(data: DebugVariableNode) {
  menu.value = null;
  if (!canEdit(data) || saving.value) return;
  editing.value = { path: data.path, draft: data.value ?? "" };
  void nextTick(() => {
    editInput.value?.focus();
    editInput.value?.select();
  });
}

function startEditFromMenu() {
  const path = menu.value?.path;
  const node = path === undefined ? undefined : tree.value?.getNode(path);
  if (node) startEdit(node.data as DebugVariableNode);
  menu.value = null;
}

function cancelEdit() {
  if (!saving.value) editing.value = null;
}

/**
 * Shows the adapter's answer in the edited row until the owner's refresh
 * (on the variablesVersion bump) rebuilds the tree with fresh children.
 */
function applyValue(path: string, result: { value: string; type?: string }) {
  const data = tree.value?.getNode(path)?.data as DebugVariableNode | undefined;
  if (!data) return;
  data.value = result.value;
  if (result.type !== undefined) data.type = result.type;
}

/** A rejected value keeps the old one, shows the adapter's message and leaves the input open. */
async function commitEdit(data: DebugVariableNode) {
  const current = editing.value;
  if (!current || current.path !== data.path || saving.value) return;
  if (current.draft === data.value) {
    editing.value = null;
    return;
  }
  const token = loads;
  saving.value = true;
  try {
    const result = await sessions.setVariable(props.sessionId, data.parentReference, data.name, current.draft);
    saving.value = false;
    // After a step the tree is rebuilt with fresh values: nothing to update.
    if (token === loads) applyValue(data.path, result);
    if (editing.value?.path === data.path) editing.value = null;
  } catch (error) {
    saving.value = false;
    ElMessage.error({ message: t("debug.variables.setValueFailed", { error: debugErrorText(error) }), grouping: true });
    // Typed text stays for a correction; Esc shows the original value again.
    if (editing.value?.path === data.path) void nextTick(() => editInput.value?.focus());
  }
}

function onEditKeydown(event: KeyboardEvent, data: DebugVariableNode) {
  // Keys typed into the input are not tree navigation or a dialog's Esc.
  event.stopPropagation();
  if (event.isComposing) return;
  if (event.key === "Enter") {
    event.preventDefault();
    void commitEdit(data);
  } else if (event.key === "Escape") {
    event.preventDefault();
    cancelEdit();
  }
}

/**
 * Opens the variable's menu: at once with Set Value; the field breakpoint
 * items join once the adapter answered (a menu with only those opens then,
 * or not at all when it offers none). An answer that comes after the menu
 * was closed or another opened, or after the program moved on, is dropped.
 */
function openMenu(event: MouseEvent | KeyboardEvent, data: DebugVariableNode, anchor = contextMenuAnchor(event)) {
  const edit = canEdit(data);
  const watch = canWatch(data);
  if (!edit && !watch) return;
  event.preventDefault();
  const seq = (menuSeq += 1);
  const opened = { path: data.path, name: data.name, ...anchor, edit, field: null };
  menu.value = edit ? opened : null;
  if (!watch) return;
  const token = loads;
  void sessions.dataBreakpointInfo(props.sessionId, data.parentReference, data.name).then((field) => {
    if (seq !== menuSeq || token !== loads || offeredAccessTypes(field).length === 0) return;
    // Set Value's menu that was closed meanwhile stays closed.
    if (edit && menu.value?.path !== data.path) return;
    menu.value = { ...opened, field };
  });
}

/** The ContextMenu key or Shift+F10 on a focused row opens its menu below the row (when it offers one). */
function onTreeKeydown(event: KeyboardEvent) {
  if (!isContextMenuKey(event)) return;
  const item = event.target instanceof Element ? event.target.closest('[role="treeitem"]') : null;
  const key = item?.getAttribute("data-key");
  const data = key == null ? undefined : (tree.value?.getNode(key)?.data as DebugVariableNode | undefined);
  if (!item || !data || !(canEdit(data) || canWatch(data)) || !claimContextMenuKey(event)) return;
  openMenu(event, data, contextMenuAnchor(event, item.querySelector(":scope > .el-tree-node__content") ?? item));
}

function closeMenu() {
  menu.value = null;
  menuSeq += 1;
}

/**
 * Any press elsewhere drops a field answer still on its way: the menu must
 * not pop up later where the user right-clicked before. (The right-click
 * that opens a menu presses first, then asks.)
 */
function onPointerDown() {
  menuSeq += 1;
}

onMounted(() => document.addEventListener("pointerdown", onPointerDown, true));
onBeforeUnmount(() => document.removeEventListener("pointerdown", onPointerDown, true));

function watchSet(field: DataBreakpointInfo, accessType: DataBreakpointAccessType): boolean {
  return field.dataId !== null && sessions.hasDataBreakpoint(props.sessionId, field.dataId, accessType);
}

function watchText(accessType: DataBreakpointAccessType): string {
  return t(accessType === "read" ? "debug.variables.watchRead" : "debug.variables.watchWrite");
}

/** A set item is checked (a check mark) and, for screen readers, named as on (aria-label). */
function toggleWatch(accessType: DataBreakpointAccessType) {
  const field = menu.value?.field;
  closeMenu();
  if (field) void sessions.toggleDataBreakpoint(props.sessionId, field, accessType);
}

async function copyValue(value: string | undefined) {
  if (value === undefined) return;
  try {
    await copyText(value);
    ElMessage.success({ message: t("debug.variables.copied"), grouping: true });
  } catch {
    ElMessage.error({ message: t("debug.variables.copyFailed"), grouping: true });
  }
}
</script>

<template>
  <el-tree
    ref="tree"
    :key="generation"
    :class="['debug-variable-tree', { 'is-stale': !live }]"
    lazy
    :load="loadNode"
    node-key="key"
    :props="{ label: 'name', isLeaf: 'leaf' }"
    :expand-on-click-node="live"
    :empty-text="emptyText"
    @node-expand="(data: DebugVariableNode) => emit('expand', data.path)"
    @node-collapse="(data: DebugVariableNode) => emit('collapse', data.path)"
    @node-contextmenu="(event: MouseEvent, data: DebugVariableNode) => openMenu(event, data)"
    @keydown="onTreeKeydown"
  >
    <template #default="{ data }">
      <span class="debug-variables__row" :class="[`is-${(data as DebugVariableNode).kind}`, { 'is-error': (data as DebugVariableNode).error }]">
        <span class="debug-variables__name">{{ (data as DebugVariableNode).name }}</span>
        <template v-if="(data as DebugVariableNode).value !== undefined">
          <span class="debug-variables__separator">=</span>
          <el-input
            v-if="editing?.path === (data as DebugVariableNode).path"
            ref="editInput"
            v-model="editing.draft"
            class="debug-variables__edit"
            size="small"
            :disabled="saving"
            :aria-label="t('debug.variables.setValue')"
            @click.stop
            @dblclick.stop
            @keydown="(event: KeyboardEvent) => onEditKeydown(event, data as DebugVariableNode)"
            @blur="cancelEdit"
          />
          <span
            v-else
            class="debug-variables__value"
            :title="titleValue((data as DebugVariableNode).value)"
            @dblclick.stop="startEdit(data as DebugVariableNode)"
          >
            {{ displayValue((data as DebugVariableNode).value) }}
          </span>
        </template>
        <span v-if="(data as DebugVariableNode).type" class="debug-variables__type">{{ (data as DebugVariableNode).type }}</span>
        <span class="debug-variables__actions">
          <el-button
            v-if="(data as DebugVariableNode).kind !== 'scope' && !(data as DebugVariableNode).error && (data as DebugVariableNode).value !== undefined"
            text
            size="small"
            native-type="button"
            :aria-label="t('debug.variables.copyValue')"
            :title="t('debug.variables.copyValue')"
            @click.stop="copyValue((data as DebugVariableNode).value)"
          >
            <el-icon><CopyDocument /></el-icon>
          </el-button>
          <el-button
            v-if="(data as DebugVariableNode).kind === 'watch'"
            text
            size="small"
            native-type="button"
            class="debug-variables__remove"
            :aria-label="t('debug.variables.removeWatch')"
            :title="t('debug.variables.removeWatch')"
            @click.stop="emit('remove-watch', (data as DebugVariableNode).watchIndex ?? -1)"
          >
            <el-icon><Close /></el-icon>
          </el-button>
        </span>
      </span>
    </template>
  </el-tree>
  <AppContextMenu v-if="menu" :open="true" :x="menu.x" :y="menu.y" :width="220" popper-class="debug-variables__menu" :aria-label="menu.name" @close="closeMenu">
    <el-dropdown-item v-if="menu.edit" :icon="Edit" @click="startEditFromMenu">{{ t("debug.variables.setValue") }}</el-dropdown-item>
    <template v-if="menu.field">
      <el-dropdown-item
        v-for="(accessType, index) in offeredAccessTypes(menu.field)"
        :key="accessType"
        :divided="menu.edit && index === 0"
        :icon="watchSet(menu.field, accessType) ? Check : undefined"
        :class="['debug-variables__watch', { 'is-checked': watchSet(menu.field, accessType) }]"
        :aria-label="watchSet(menu.field, accessType) ? t('debug.variables.watchOn', { action: watchText(accessType) }) : undefined"
        @click="toggleWatch(accessType)"
      >
        {{ watchText(accessType) }}
      </el-dropdown-item>
    </template>
  </AppContextMenu>
</template>
