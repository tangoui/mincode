<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { InputInstance } from "element-plus";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { useVueI18n } from "@/i18n/messages";
import type { ProjectEditorConditionDraft, ProjectEditorEntryMenu } from "@/composables/use-project-editor-debug";

/** Breakpoint condition popover and entry-point run / debug menu of the project editor. */
const props = defineProps<{
  condition: ProjectEditorConditionDraft | null;
  entryMenu: ProjectEditorEntryMenu | null;
}>();
const emit = defineEmits<{
  "save-condition": [values: { condition: string; hitCondition: string; logMessage: string }];
  "cancel-condition": [];
  run: [key: string, noDebug: boolean];
  "close-entry-menu": [];
}>();

const { t } = useVueI18n();
const anchor = ref<HTMLElement>();
const conditionInput = ref<InputInstance>();
const logMessageInput = ref<InputInstance>();
const form = ref({ condition: "", hitCondition: "", logMessage: "" });

watch(
  () => props.condition,
  async (draft) => {
    if (!draft) return;
    form.value = { condition: draft.condition, hitCondition: draft.hitCondition, logMessage: draft.logMessage };
    await nextTick();
    if (draft.focusLogMessage) logMessageInput.value?.focus();
    else conditionInput.value?.focus();
  },
  { immediate: true }
);

function save() {
  emit("save-condition", { ...form.value });
}

/** A controlled popover does not close itself; a press outside it cancels the edit. */
function handleOutsideMouseDown(event: MouseEvent) {
  const target = event.target as HTMLElement | null;
  if (target?.closest?.(".project-debug-popups__condition")) return;
  emit("cancel-condition");
}

watch(
  () => Boolean(props.condition),
  (open) => {
    if (open) window.addEventListener("mousedown", handleOutsideMouseDown, true);
    else window.removeEventListener("mousedown", handleOutsideMouseDown, true);
  },
  { immediate: true, flush: "post" }
);

onBeforeUnmount(() => window.removeEventListener("mousedown", handleOutsideMouseDown, true));
</script>

<template>
  <span
    ref="anchor"
    class="project-debug-popups__anchor"
    :style="condition ? { left: `${condition.anchor.x}px`, top: `${condition.anchor.y}px` } : undefined"
    aria-hidden="true"
  />
  <el-popover
    :visible="Boolean(condition)"
    virtual-triggering
    :virtual-ref="anchor"
    placement="bottom-start"
    :width="320"
    popper-class="project-debug-popups__condition"
  >
    <el-form v-if="condition" label-position="top" size="small" @submit.prevent="save" @keydown.esc.stop="emit('cancel-condition')">
      <div class="project-debug-popups__title">{{ t(condition.focusLogMessage ? "debug.editor.logpointTitle" : "debug.editor.conditionTitle", { line: condition.line }) }}</div>
      <el-form-item :label="t('debug.editor.conditionLabel')">
        <el-input
          ref="conditionInput"
          v-model="form.condition"
          :placeholder="t('debug.editor.conditionPlaceholder')"
          @keydown.enter.prevent="save"
        />
      </el-form-item>
      <el-form-item :label="t('debug.editor.hitConditionLabel')">
        <el-input
          v-model="form.hitCondition"
          :placeholder="t('debug.editor.hitConditionPlaceholder')"
          @keydown.enter.prevent="save"
        />
      </el-form-item>
      <el-form-item :label="t('debug.editor.logMessageLabel')" data-field="logMessage">
        <el-input
          ref="logMessageInput"
          v-model="form.logMessage"
          :placeholder="t('debug.editor.logMessagePlaceholder')"
          @keydown.enter.prevent="save"
        />
      </el-form-item>
      <div class="project-debug-popups__hint">{{ t("debug.editor.logMessageHint") }}</div>
      <div class="project-debug-popups__hint">{{ t("debug.editor.conditionClearHint") }}</div>
      <div class="project-debug-popups__actions">
        <el-button size="small" @click="emit('cancel-condition')">{{ t("common.cancel") }}</el-button>
        <el-button size="small" type="primary" @click="save">{{ t("common.save") }}</el-button>
      </div>
    </el-form>
  </el-popover>
  <AppContextMenu
    v-if="entryMenu"
    :open="true"
    :x="entryMenu.anchor.x"
    :y="entryMenu.anchor.y"
    :width="240"
    @close="emit('close-entry-menu')"
  >
    <template v-for="(item, index) in entryMenu.items" :key="item.key">
      <el-dropdown-item :divided="index > 0" @click="emit('run', item.key, true)">
        {{ t("debug.editor.runEntry", { label: item.label }) }}
      </el-dropdown-item>
      <el-dropdown-item @click="emit('run', item.key, false)">
        {{ t("debug.editor.debugEntry", { label: item.label }) }}
      </el-dropdown-item>
    </template>
  </AppContextMenu>
</template>

<style scoped>
.project-debug-popups__anchor {
  position: fixed;
  width: 0;
  height: 0;
  pointer-events: none;
}

.project-debug-popups__title {
  margin-bottom: 8px;
  color: var(--app-text);
  font-weight: 600;
}

.project-debug-popups__hint {
  margin: -4px 0 10px;
  color: var(--app-text-muted);
  font-size: 12px;
}

.project-debug-popups__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
