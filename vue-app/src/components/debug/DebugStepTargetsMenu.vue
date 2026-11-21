<script setup lang="ts">
/**
 * Smart Step Into's choice: the calls a stepIn can enter (DAP stepInTargets),
 * opened at (x, y). The first target takes the focus when the menu opens, so
 * the arrow keys move between targets, Enter chooses one and Escape (or a
 * click elsewhere) cancels.
 */
import { onBeforeUnmount, onMounted } from "vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import type { DapStepInTarget } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";

defineProps<{ targets: readonly DapStepInTarget[]; x: number; y: number }>();

const emit = defineEmits<{ select: [targetId: number]; close: [] }>();

const { t } = useVueI18n();

const POPPER_CLASS = "debug-step-targets__menu";

/**
 * Element Plus focuses the list of a menu it opens without a keydown on its
 * trigger (this one has none), not an item; the focus goes on to the first
 * target, so Enter right away steps into it.
 */
function onFocusIn(event: FocusEvent) {
  const list = event.target;
  if (!(list instanceof HTMLElement) || !list.matches(`.${POPPER_CLASS} .el-dropdown-menu`)) return;
  list.querySelector<HTMLElement>(".el-dropdown-menu__item:not(.is-disabled)")?.focus();
}

onMounted(() => document.addEventListener("focusin", onFocusIn));
onBeforeUnmount(() => document.removeEventListener("focusin", onFocusIn));
</script>

<template>
  <AppContextMenu
    :open="true"
    :x="x"
    :y="y"
    :width="240"
    :popper-class="POPPER_CLASS"
    :aria-label="t('debug.stepTargets.title')"
    @close="emit('close')"
  >
    <li class="debug-step-targets__title" role="presentation">{{ t("debug.stepTargets.title") }}</li>
    <el-dropdown-item v-for="target in targets" :key="target.id" @click="emit('select', target.id)">
      {{ target.label }}
    </el-dropdown-item>
  </AppContextMenu>
</template>
