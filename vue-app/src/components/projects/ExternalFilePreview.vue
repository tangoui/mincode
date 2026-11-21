<script setup lang="ts">
import { computed, defineAsyncComponent } from "vue";
import { Document } from "@element-plus/icons-vue";
import type { ProjectEditorDocument } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";

const props = withDefaults(defineProps<{ document: ProjectEditorDocument; active?: boolean }>(), { active: true });
const LargeTextPreview = defineAsyncComponent(() => import("@/components/projects/LargeTextPreview.vue"));
const emit = defineEmits<{ openSystem: [] }>();
const { t } = useVueI18n();
const formattedSize = computed(() => {
  const size = props.document.size ?? 0;
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / 1024 / 1024).toFixed(1)} MB`;
});
</script>

<template>
  <LargeTextPreview
    v-if="document.textPreviewAvailable && document.externalKind === 'binary'"
    :document="document" :active="active" @open-system="emit('openSystem')"
  />
  <iframe
    v-else-if="document.externalKind === 'pdf' && document.content"
    class="project-external-pdf"
    :src="document.content"
    :title="document.name"
  />
  <div v-else class="project-external-binary">
    <el-icon class="project-external-binary__icon"><Document /></el-icon>
    <strong>{{ document.name }}</strong>
    <span>{{ document.mimeType || t("project.external.binaryFile") }} · {{ formattedSize }}</span>
    <el-button type="primary" native-type="button" @click="emit('openSystem')">
      {{ t("project.external.openSystem") }}
    </el-button>
  </div>
</template>
