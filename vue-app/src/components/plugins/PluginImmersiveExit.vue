<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { Close } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { usePluginTakeoverStore } from "@/stores/plugin-takeover";

// The host's own way out of a plugin's immersive mode; it never depends on the plugin page.
const store = usePluginTakeoverStore();
const { t } = useVueI18n();
const shown = ref(true);
let hideTimer: ReturnType<typeof setTimeout> | undefined;

function reveal() {
  shown.value = true;
  if (hideTimer) clearTimeout(hideTimer);
  hideTimer = setTimeout(() => { shown.value = false; }, 3000);
}

watch(() => store.immersive, (value) => { if (value) reveal(); }, { immediate: true });
onBeforeUnmount(() => { if (hideTimer) clearTimeout(hideTimer); });
</script>

<template>
  <div
    v-if="store.immersive"
    class="app-plugin-immersive-exit"
    :class="{ 'is-shown': shown }"
    @mouseenter="reveal"
    @focusin="reveal"
  >
    <el-button size="small" :icon="Close" :aria-label="t('plugins.immersive.exit')" @click="store.exitAll()">
      {{ t('plugins.immersive.hint', { chord: 'Ctrl+Alt+Enter' }) }}
    </el-button>
  </div>
</template>
