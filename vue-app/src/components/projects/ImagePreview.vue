<script setup lang="ts">
import { ref, watch } from "vue";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{ src: string; alt: string }>();
const { t } = useVueI18n();
const failed = ref(false);

watch(
  () => props.src,
  () => {
    failed.value = false;
  }
);
</script>

<template>
  <el-scrollbar class="project-image-preview">
    <section class="project-image-preview__content">
      <img v-if="!failed" :src="src" :alt="alt" @error="failed = true" />
      <p v-else class="project-image-preview__error" role="alert">
        {{ t("project.image.loadFailed") }}
      </p>
    </section>
  </el-scrollbar>
</template>

<style scoped>
.project-image-preview {
  min-width: 0;
  min-height: 0;
  flex: 1;
  background: var(--app-canvas);
}

.project-image-preview__content {
  display: grid;
  min-width: 100%;
  min-height: 100%;
  place-items: center;
  padding: 24px;
}

.project-image-preview img {
  display: block;
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}

.project-image-preview__error {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 13px;
}
</style>
