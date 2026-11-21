<script setup lang="ts">
import { computed } from "vue";
import { avatarColor, avatarInitials } from "@/domain/connections/overview";

export type ConnectionAvatarStatus = "online" | "pending" | "offline";

const props = withDefaults(
  defineProps<{
    name: string;
    seed: string;
    status?: ConnectionAvatarStatus;
    size?: 26 | 32;
  }>(),
  { status: undefined, size: 32 },
);

const initials = computed(() => avatarInitials(props.name));
const style = computed(() => ({ "--connection-avatar-color": avatarColor(props.seed) }));
</script>

<template>
  <!-- Decorative: the owning card/row carries the connection name and status text. -->
  <span class="connection-avatar" :class="`is-size-${size}`" :style="style" aria-hidden="true">
    <span class="connection-avatar__label">
      <slot>{{ initials }}</slot>
    </span>
    <span v-if="status" class="connection-avatar__status" :class="`is-${status}`" />
  </span>
</template>
