<script setup lang="ts">
import { ref } from "vue";
import { ElButton, ElDialog, ElEmpty, ElScrollbar } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import type { ExternalCandidate } from "@/domain/plugins/external-open";
import { listAssociationHandlers, openAssociationSettings } from "@/services/plugin-associations-runtime";
const props = defineProps<{ pluginId: string }>();
const { t } = useVueI18n();
const visible = ref(false), loading = ref(false), failed = ref(false), candidates = ref<ExternalCandidate[]>([]);
async function open() {
  visible.value = true; loading.value = true; failed.value = false;
  try {
    candidates.value = await listAssociationHandlers(props.pluginId);
  } catch { failed.value = true; candidates.value = []; }
  finally { loading.value = false; }
}
</script>
<template>
  <ElButton text @click="open">{{ t('pluginContrib.externalOpenHandlers.settings') }}</ElButton>
  <ElDialog v-model="visible" :title="t('pluginContrib.externalOpenHandlers.settings')" width="min(520px, calc(100vw - 32px))" append-to-body>
    <ElScrollbar max-height="350px">
      <ElEmpty v-if="!loading && !candidates.length" :description="t(failed ? 'pluginContrib.externalOpenHandlers.failed' : 'pluginContrib.externalOpenHandlers.none')" />
      <ElButton v-for="c in candidates" :key="c.handlerId" :loading="loading" @click="openAssociationSettings(c.pluginId, c.handlerId)">{{ c.type }}</ElButton>
    </ElScrollbar>
  </ElDialog>
</template>
