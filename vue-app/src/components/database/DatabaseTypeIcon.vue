<script setup lang="ts">
import { computed } from "vue";
import {
  databaseTypeLabels,
  type DbType,
} from "@/domain/database/types";
import { databaseConnectionBrand, type DatabaseTypeBrand } from "@/domain/database/type-brands";

defineOptions({ name: "DatabaseTypeIcon" });

const props = withDefaults(
  defineProps<{
    dbType: DbType;
    size?: "small" | "medium";
  }>(),
  { size: "medium" }
);

const iconTitle = computed(() => databaseTypeLabels[props.dbType]);
const brand = computed<DatabaseTypeBrand>(() => databaseConnectionBrand({ dbType: props.dbType }));
</script>

<template>
  <span
    class="database-type-icon"
    :class="`is-${size}`"
    :style="{ '--database-type-color': brand.color }"
    :title="iconTitle"
    aria-hidden="true"
  >
    <svg v-if="brand.icon === 'leaf'" viewBox="0 0 24 24" fill="none">
      <path d="M12 3c4.1 3.4 5.1 7.2 2.8 11.2-1 1.8-2 3.2-2.8 4.3-.8-1.1-1.8-2.5-2.8-4.3C6.9 10.2 7.9 6.4 12 3Z" fill="currentColor" />
      <path d="M12 7v13" stroke="var(--app-surface)" stroke-width="1.2" stroke-linecap="round" />
    </svg>
    <svg v-else-if="brand.icon === 'layers'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round">
      <path d="m12 4 8 4-8 4-8-4 8-4Z" />
      <path d="m4 12 8 4 8-4M4 16l8 4 8-4" />
    </svg>
    <svg v-else-if="brand.icon === 'star'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
      <path d="m12 3 2.1 5.3L20 10l-4.6 3.6.2 5.9L12 16.2l-3.6 3.3.2-5.9L4 10l5.9-1.7L12 3Z" />
    </svg>
    <svg v-else-if="brand.icon === 'nodes'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round">
      <circle cx="6" cy="12" r="2.2" /><circle cx="17.5" cy="6" r="2.2" /><circle cx="17.5" cy="18" r="2.2" />
      <path d="m8 11 7.4-3.8M8 13l7.4 3.8" />
    </svg>
    <svg v-else-if="brand.icon === 'bars'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
      <path d="M5 18V9M10 18V5M15 18v-7M20 18V7" />
    </svg>
    <svg v-else-if="brand.icon === 'snowflake'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round">
      <path d="M12 3v18M4.2 7.5l15.6 9M4.2 16.5l15.6-9M9.5 5.5 12 8l2.5-2.5M9.5 18.5 12 16l2.5 2.5" />
    </svg>
    <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65">
      <ellipse cx="12" cy="6" rx="7" ry="3" />
      <path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6" />
    </svg>
  </span>
</template>

<style scoped>
.database-type-icon {
  display: inline-grid;
  box-sizing: border-box;
  flex: none;
  place-items: center;
  color: var(--database-type-color);
  line-height: 1;
}

.database-type-icon svg {
  display: block;
  width: 100%;
  height: 100%;
  overflow: visible;
}

.database-type-icon img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.database-type-icon.is-medium {
  width: 22px;
  height: 22px;
}

.database-type-icon.is-small {
  width: 18px;
  height: 18px;
}
</style>
