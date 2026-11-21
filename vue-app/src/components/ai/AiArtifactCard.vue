<script setup lang="ts">
import { ref } from "vue";
import { Check, CopyDocument, Document } from "@element-plus/icons-vue";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import type { AiArtifactPresentation } from "@/domain/ai/event-presentations";

const props = defineProps<{
  artifact: AiArtifactPresentation;
  labels: { preview: string; collapse: string; copy: string; copied: string };
}>();
const expanded = ref(false);
const copied = ref(false);

async function copyArtifact() {
  if (!navigator.clipboard?.writeText) return;
  await navigator.clipboard.writeText(props.artifact.content);
  copied.value = true;
}
</script>

<template>
  <section class="ai-artifact-card">
    <header>
      <Document aria-hidden="true" />
      <span>
        <strong>{{ artifact.title }}</strong>
        <small v-if="artifact.language">{{ artifact.language }}</small>
      </span>
      <el-button
        class="ai-artifact-card__toggle"
        text
        size="small"
        native-type="button"
        data-action="toggle-artifact"
        :aria-expanded="expanded"
        :aria-label="expanded ? labels.collapse : labels.preview"
        :title="expanded ? labels.collapse : labels.preview"
        @click="expanded = !expanded"
      >
        {{ expanded ? labels.collapse : labels.preview }}
      </el-button>
      <el-button
        class="ai-artifact-card__copy"
        text
        size="small"
        native-type="button"
        data-action="copy-artifact"
        :aria-label="copied ? labels.copied : labels.copy"
        :title="copied ? labels.copied : labels.copy"
        @click="copyArtifact"
      >
        <Check v-if="copied" aria-hidden="true" />
        <CopyDocument v-else aria-hidden="true" />
      </el-button>
    </header>
    <el-scrollbar v-if="expanded" class="ai-artifact-card__preview">
      <MarkdownPreview v-if="artifact.language === 'markdown' || artifact.language === 'md'" :content="artifact.content" />
      <pre v-else>{{ artifact.content }}</pre>
    </el-scrollbar>
  </section>
</template>
