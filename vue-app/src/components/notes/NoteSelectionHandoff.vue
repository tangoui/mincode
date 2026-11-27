<script setup lang="ts">
import { MagicStick } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { useVueI18n } from "@/i18n/messages";
import { sendToAi } from "@/services/ai-handoff";

const selection = defineModel<{ x: number; y: number; content: string }>();
const { t } = useVueI18n();

function handoff() {
  const content = selection.value?.content;
  selection.value = undefined;
  if (!content) return;
  void sendToAi({
    source: "notes",
    title: t("ai.handoff.noteSelection"),
    content,
    prompt: t("ai.handoff.noteSelectionPrompt"),
  });
}
</script>

<template>
  <AppContextMenu
    v-if="selection"
    :open="true"
    :x="selection.x"
    :y="selection.y"
    @close="selection = undefined"
  >
    <el-dropdown-item @click="handoff">
      <el-icon><MagicStick /></el-icon>{{ t("ai.handoff.sendNoteSelection") }}
    </el-dropdown-item>
  </AppContextMenu>
</template>
