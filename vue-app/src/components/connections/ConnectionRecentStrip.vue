<script setup lang="ts">
import { useId } from "vue";
import ConnectionAvatar, { type ConnectionAvatarStatus } from "./ConnectionAvatar.vue";

export interface RecentStripItem {
  id: string;
  name: string;
  seed: string;
  detail: string;
  status: ConnectionAvatarStatus;
}

defineProps<{
  items: RecentStripItem[];
  title: string;
  clearLabel: string;
}>();

const emit = defineEmits<{
  open: [id: string];
  clear: [];
}>();

// Prefix for the detail element ids; the detail carries the time / "in use" state for screen readers.
const idPrefix = useId();

function detailId(index: number) {
  return `${idPrefix}-detail-${index}`;
}
</script>

<template>
  <section v-if="items.length" class="connection-overview__recent-block" :aria-label="title">
    <div class="connection-overview__section">
      <h3 class="connection-overview__section-title">{{ title }}</h3>
      <ElButton class="connection-overview__section-action" link type="primary" @click="emit('clear')">
        {{ clearLabel }}
      </ElButton>
    </div>
    <div class="connection-overview__recent">
      <button
        v-for="(item, index) in items"
        :key="item.id"
        type="button"
        class="connection-overview__recent-card"
        :aria-label="item.name"
        :aria-describedby="detailId(index)"
        :title="`${item.name} · ${item.detail}`"
        @click="emit('open', item.id)"
      >
        <slot name="avatar" :item="item">
          <ConnectionAvatar :name="item.name" :seed="item.seed" :status="item.status" />
        </slot>
        <span class="connection-overview__recent-text">
          <span class="connection-overview__recent-name">{{ item.name }}</span>
          <span :id="detailId(index)" class="connection-overview__recent-detail">{{ item.detail }}</span>
        </span>
      </button>
    </div>
  </section>
</template>
