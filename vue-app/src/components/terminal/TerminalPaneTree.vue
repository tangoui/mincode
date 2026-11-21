<script setup lang="ts">
import AppSplitGroup from "@/components/ui/AppSplitGroup.vue";
import TerminalPaneSlot from "@/components/terminal/TerminalPaneSlot.vue";
import type { TerminalPaneNode } from "@/domain/terminal/types";
import { isTerminalLeafPane } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";

// The split layout only: each leaf is a slot that TerminalPaneLayout fills with its terminal.
const props = defineProps<{ node: TerminalPaneNode }>();

const emit = defineEmits<{
  resize: [splitId: string, sizes: number[]];
  resizeEnd: [];
}>();

const { t } = useVueI18n();

function resizeSplit(sizes: number[]) {
  if (isTerminalLeafPane(props.node)) return;
  emit("resize", props.node.id, sizes);
}
</script>

<template>
  <TerminalPaneSlot v-if="props.node.type !== 'split'" :pane-id="props.node.id" />

  <AppSplitGroup
    v-else
    :sizes="props.node.sizes"
    :panel-count="props.node.children.length"
    :layout="props.node.direction"
    :min="props.node.direction === 'horizontal' ? 160 : 110"
    :aria-label="t('terminal.resizePane')"
    @resize="resizeSplit"
    @resize-end="emit('resizeEnd')"
  >
    <template #default="{ index }">
      <TerminalPaneTree
        v-if="props.node.children[index]"
        :key="props.node.children[index].id"
        :node="props.node.children[index]"
        @resize="(splitId, sizes) => emit('resize', splitId, sizes)"
        @resize-end="emit('resizeEnd')"
      />
    </template>
  </AppSplitGroup>
</template>
