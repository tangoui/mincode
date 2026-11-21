<script setup lang="ts">
// 对象名称后的关联笔记角标：计数 > 0 时显示，点击打开关联笔记抽屉。计数来自轻量的抽屉 store（同类型批量请求）。
import { computed, onBeforeUnmount, watch } from "vue";
import { Notebook } from "@element-plus/icons-vue";
import type { NoteLinkInput, NoteLinkType } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";

const props = defineProps<{
  type: NoteLinkType;
  id: string;
  /** 打开抽屉时作为对象名称快照。 */
  name?: string;
  parentId?: string;
  scopeId?: string;
  scopeName?: string;
}>();

const { t } = useVueI18n();
const drawer = useLinkedNotesDrawer();
const count = computed(() => drawer.counts(props.type).value[props.id] ?? 0);

let untrack: (() => void) | undefined;
watch(
  () => [props.type, props.id] as const,
  ([type, id]) => {
    untrack?.();
    untrack = drawer.track(type, id);
  },
  { immediate: true },
);
onBeforeUnmount(() => untrack?.());

function open() {
  const target: NoteLinkInput = { objectType: props.type, objectId: props.id };
  if (props.parentId !== undefined) target.parentId = props.parentId;
  if (props.scopeId !== undefined) target.scopeId = props.scopeId;
  if (props.name !== undefined) target.nameSnapshot = props.name;
  if (props.scopeName !== undefined) target.scopeNameSnapshot = props.scopeName;
  void drawer.open(target);
}
</script>

<template>
  <button
    v-if="count > 0"
    type="button"
    class="linked-notes-badge"
    :title="t('notes.link.badge', { count })"
    :aria-label="t('notes.link.badge', { count })"
    @click.stop="open"
    @dblclick.stop
    @mousedown.stop
    @keydown.stop
  >
    <el-icon class="linked-notes-badge__icon"><Notebook /></el-icon>
    <span class="linked-notes-badge__count">{{ count }}</span>
  </button>
</template>
