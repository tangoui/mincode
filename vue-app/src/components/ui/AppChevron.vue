<script setup lang="ts">
import { ArrowRight, DArrowLeft, DArrowRight } from "@element-plus/icons-vue";

const props = withDefaults(
  defineProps<{
    direction?: "up" | "down" | "left" | "right" | "double-left" | "double-right";
  }>(),
  { direction: "right" }
);
</script>

<template>
  <el-icon
    aria-hidden="true"
    class="app-chevron"
    :class="`app-chevron--${props.direction}`"
  >
    <DArrowLeft v-if="props.direction === 'double-left'" />
    <DArrowRight v-else-if="props.direction === 'double-right'" />
    <ArrowRight v-else />
  </el-icon>
</template>

<style scoped>
.app-chevron {
  display: inline-flex;
  width: 1em;
  height: 1em;
  align-items: center;
  justify-content: center;
  vertical-align: middle;
  transition: transform 140ms ease;
}

.app-chevron--down {
  transform: rotate(90deg);
}

.app-chevron--left {
  transform: rotate(180deg);
}

.app-chevron--up {
  transform: rotate(-90deg);
}
</style>
