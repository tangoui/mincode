<script setup lang="ts">
import { Link, TopRight } from "@element-plus/icons-vue";
import { safeExternalUrl, type AiSourcePresentation } from "@/domain/ai/event-presentations";

defineProps<{ label: string; sources: AiSourcePresentation[] }>();
</script>

<template>
  <section class="ai-sources-list" :aria-label="label">
    <strong class="ai-presentation-heading">{{ label }}</strong>
    <ol>
      <li v-for="(source, index) in sources" :key="`${index}-${source.title}`">
        <a
          v-if="safeExternalUrl(source.url)"
          :href="safeExternalUrl(source.url)"
          target="_blank"
          rel="noopener noreferrer"
        >
          <span>{{ index + 1 }}</span>
          <b>{{ source.title }}</b>
          <TopRight aria-hidden="true" />
        </a>
        <div v-else>
          <span>{{ index + 1 }}</span>
          <Link aria-hidden="true" />
          <b>{{ source.title }}</b>
        </div>
        <small v-if="source.description">{{ source.description }}</small>
      </li>
    </ol>
  </section>
</template>
