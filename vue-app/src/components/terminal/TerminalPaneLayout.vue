<script setup lang="ts">
import { computed, nextTick, provide, shallowReactive } from "vue";
import LocalTerminal, { type LocalTerminalActivity } from "@/components/terminal/LocalTerminal.vue";
import SshTerminal from "@/components/terminal/SshTerminal.vue";
import TerminalPaneTree from "@/components/terminal/TerminalPaneTree.vue";
import { TERMINAL_PANE_SLOTS, terminalLeafPanes } from "@/domain/terminal/pane-slots";
import type { TerminalPaneNode, TerminalSplitDirection } from "@/domain/terminal/types";

// One terminal tab: the split tree lays out slots, and every pane's terminal is rendered here,
// flat and keyed by pane id, then teleported into its slot. Splitting moves a pane under a new
// split group; with the terminal outside the tree that moves its DOM instead of unmounting it
// and building a fresh xterm (and WebGL renderer) for it.
const props = withDefaults(
  defineProps<{
    node: TerminalPaneNode;
    activePaneId: string;
    canClose: boolean;
    canSplit: boolean;
    /** Terminal page only: the last pane's close button stays enabled (closing it closes its terminal tab). */
    closeLastPane?: boolean;
    /** False for a hidden terminal tab: its panes stay mounted but none is active (no WebGL, no focus). */
    visible?: boolean;
    /** Reason shown on a pane's disabled close button (last pane of a pinned workbench tab). */
    closeDisabledReason?: string;
  }>(),
  { closeLastPane: false, visible: true, closeDisabledReason: undefined }
);

const emit = defineEmits<{
  select: [paneId: string];
  close: [paneId: string];
  reload: [paneId: string];
  configureSsh: [paneId: string];
  split: [paneId: string, direction: TerminalSplitDirection];
  resize: [splitId: string, sizes: number[]];
  resizeEnd: [];
  activity: [paneId: string, snapshot: LocalTerminalActivity];
  useDefaultShell: [paneId: string];
}>();

const slots = shallowReactive(new Map<string, HTMLElement>());
provide(TERMINAL_PANE_SLOTS, {
  register(paneId, element) {
    slots.set(paneId, element);
  },
  // A moved pane's old slot unmounts in the same render that mounts its new slot, which
  // registers only once mounted. Dropping the old slot right away would unmount the terminal in
  // between; drop it after this render, and only if no newer slot took its place.
  unregister(paneId, element) {
    void nextTick(() => {
      if (slots.get(paneId) === element) slots.delete(paneId);
    });
  },
});

const panes = computed(() => terminalLeafPanes(props.node));
const paneActive = (paneId: string) => props.visible && props.activePaneId === paneId;
const paneCanClose = () => props.canClose || props.closeLastPane;
</script>

<template>
  <TerminalPaneTree :node="props.node" @resize="(splitId, sizes) => emit('resize', splitId, sizes)" @resize-end="emit('resizeEnd')" />
  <!-- A terminal mounts only once its slot exists, so it is never measured outside its place. -->
  <template v-for="pane in panes" :key="pane.id">
    <Teleport v-if="slots.get(pane.id)" :to="slots.get(pane.id)">
      <LocalTerminal
        v-if="pane.type === 'local'"
        :key="`${pane.id}:${pane.reloadVersion ?? 0}`"
        :session-id="pane.id"
        :shell-type="pane.shellType"
        :custom-path="pane.customPath"
        :initial-cwd="pane.cwd ?? pane.initialCwd"
        :initial-command="pane.initialCommand"
        :extra-env="pane.extraEnv"
        :profile="pane.profile"
        :task="pane.task"
        :active="paneActive(pane.id)"
        :can-close="paneCanClose()"
        :close-disabled-reason="props.closeDisabledReason"
        :visible="props.visible"
        :can-split="props.canSplit"
        keep-alive-on-unmount
        @select="emit('select', pane.id)"
        @close="emit('close', pane.id)"
        @reload="emit('reload', pane.id)"
        @split="emit('split', pane.id, $event)"
        @activity="emit('activity', pane.id, $event)"
        @use-default-shell="emit('useDefaultShell', pane.id)"
      />
      <SshTerminal
        v-else
        :key="`${pane.id}:${pane.reloadVersion ?? 0}`"
        :session-id="pane.id"
        :profile="pane.profile"
        :credentials="pane.credentials"
        :active="paneActive(pane.id)"
        :can-close="paneCanClose()"
        :close-disabled-reason="props.closeDisabledReason"
        :visible="props.visible"
        :can-split="props.canSplit"
        keep-alive-on-unmount
        @select="emit('select', pane.id)"
        @close="emit('close', pane.id)"
        @reload="emit('reload', pane.id)"
        @configure="emit('configureSsh', pane.id)"
        @split="emit('split', pane.id, $event)"
        @activity="emit('activity', pane.id, $event)"
      />
    </Teleport>
  </template>
</template>
