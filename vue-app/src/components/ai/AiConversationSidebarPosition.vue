<script setup lang="ts">
import { computed } from "vue";
import { Back, Right } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { useAiConversationPreferencesStore, type AiConversationSidebarPosition } from "@/stores/ai-conversation-preferences";

const props = defineProps<{ compact?: boolean }>();
const { t } = useVueI18n();
const preferences = useAiConversationPreferencesStore();
void preferences.hydrate();
const nextPosition = computed(() => preferences.sidebarPosition === "left" ? "right" : "left");
const toggleLabel = computed(() => t(nextPosition.value === "left" ? "ai.conversation.moveListLeft" : "ai.conversation.moveListRight"));
async function select(position: AiConversationSidebarPosition) {
  try {
    await preferences.setSidebarPosition(position);
  } catch {
    ElMessage.error(t("ai.conversation.positionSaveFailed"));
  }
}
</script>

<template>
  <el-tooltip v-if="props.compact" :content="toggleLabel" placement="bottom">
    <el-button
      text
      :icon="nextPosition === 'left' ? Back : Right"
      :aria-label="toggleLabel"
      @click="select(nextPosition)"
    />
  </el-tooltip>
  <el-segmented
    v-else
    class="settings-ai-scope-tabs"
    :model-value="preferences.sidebarPosition"
    :options="[
      { label: t('settings.ai.sidebarPositionLeft'), value: 'left' },
      { label: t('settings.ai.sidebarPositionRight'), value: 'right' },
    ]"
    :aria-label="t('settings.ai.sidebarPosition')"
    @update:model-value="select($event as AiConversationSidebarPosition)"
  />
</template>
