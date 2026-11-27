<script setup lang="ts">
import { ref } from "vue";
import { Document } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";

defineProps<{ root: string; sources: string[] }>();

const { t } = useVueI18n();
const expanded = ref(false);
</script>

<template>
  <div class="ai-project-instructions">
    <el-button
      text
      size="small"
      :icon="Document"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      {{ t("ai.projectInstructions.loaded", { files: sources.join(", ") }) }}
    </el-button>
    <div v-if="expanded" class="ai-project-instructions__sources">
      <div>{{ root }}</div>
      <ul>
        <li v-for="path in sources" :key="path">{{ path }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.ai-project-instructions {
  min-width: 0;
  padding-inline: 10px;
}

.ai-project-instructions :deep(.el-button) {
  max-width: 100%;
}

.ai-project-instructions :deep(.el-button > span) {
  overflow: hidden;
  text-overflow: ellipsis;
}

.ai-project-instructions__sources {
  color: var(--app-text-muted);
  padding: 2px 8px 6px;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.ai-project-instructions__sources ul {
  margin: 2px 0 0;
  padding-inline-start: 16px;
}
</style>
