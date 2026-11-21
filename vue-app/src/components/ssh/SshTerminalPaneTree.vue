<script setup lang="ts">
import AppSplitGroup from "@/components/ui/AppSplitGroup.vue";
import SshTerminal from "@/components/terminal/SshTerminal.vue";
import type { SshTerminalStatus } from "@/domain/ssh/types";
import type { TerminalSplitDirection } from "@/domain/terminal/types";
import type { SshWorkspacePaneNode } from "@/domain/ssh/workspace-session-layout";
import { isSshWorkspaceTerminalPane } from "@/domain/ssh/workspace-session-layout";
import { useVueI18n } from "@/i18n/messages";

defineOptions({ name: "SshTerminalPaneTree" });

const props = defineProps<{
  node: SshWorkspacePaneNode;
  activePaneId: string;
  canSplit: boolean;
}>();

const emit = defineEmits<{
  select: [paneId: string];
  close: [paneId: string];
  reload: [paneId: string];
  configure: [paneId: string];
  split: [paneId: string, direction: TerminalSplitDirection];
  statusChange: [paneId: string, status: SshTerminalStatus, detail?: string];
  resize: [splitId: string, sizes: number[]];
}>();

const { t } = useVueI18n();

function resizeSplit(sizes: number[]) {
  if (isSshWorkspaceTerminalPane(props.node)) return;
  emit("resize", props.node.id, sizes);
}
</script>

<template>
  <SshTerminal
    v-if="isSshWorkspaceTerminalPane(props.node)"
    :key="`${props.node.id}:${props.node.reloadVersion}`"
    :session-id="props.node.id"
    :profile="props.node.profile"
    :credentials="props.node.credentials"
    :active="props.activePaneId === props.node.id"
    can-close
    :can-split="props.canSplit"
    keep-alive-on-unmount
    @select="emit('select', props.node.id)"
    @close="emit('close', props.node.id)"
    @reload="emit('reload', props.node.id)"
    @configure="emit('configure', props.node.id)"
    @split="emit('split', props.node.id, $event)"
    @status-change="(status, detail) => emit('statusChange', props.node.id, status, detail)"
  />

  <AppSplitGroup
    v-else
    class="ssh-session-split-group"
    :sizes="props.node.sizes"
    :panel-count="props.node.children.length"
    :layout="props.node.direction"
    :min="props.node.direction === 'horizontal' ? 220 : 160"
    :aria-label="t('terminal.resizePane')"
    @resize="resizeSplit"
  >
    <template #default="{ index }">
      <SshTerminalPaneTree
        v-if="props.node.children[index]"
        :key="props.node.children[index].id"
        :node="props.node.children[index]"
        :active-pane-id="props.activePaneId"
        :can-split="props.canSplit"
        @select="emit('select', $event)"
        @close="emit('close', $event)"
        @reload="emit('reload', $event)"
        @configure="emit('configure', $event)"
        @split="(paneId, direction) => emit('split', paneId, direction)"
        @status-change="(paneId, status, detail) => emit('statusChange', paneId, status, detail)"
        @resize="(splitId, sizes) => emit('resize', splitId, sizes)"
      />
    </template>
  </AppSplitGroup>
</template>
