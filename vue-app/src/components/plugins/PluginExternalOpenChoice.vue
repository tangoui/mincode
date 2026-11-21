<script setup lang="ts">
import { ElCheckbox, ElOption, ElSelect } from "element-plus";
import type { ExternalCandidate } from "@/domain/plugins/external-open";
import { useVueI18n } from "@/i18n/messages";
defineProps<{ candidates: ExternalCandidate[]; name: string }>();
const selected = defineModel<string>("selected", { required: true });
const remember = defineModel<boolean>("remember", { required: true });
const { t } = useVueI18n();
</script>
<template>
  <div>
    <p>{{ t("pluginContrib.externalOpenHandlers.openBody", { name }) }}</p>
    <ElSelect v-model="selected" :aria-label="t('pluginContrib.externalOpenHandlers.choose')" style="width: 100%">
      <ElOption v-for="c in candidates" :key="c.pluginId + '/' + c.handlerId" :value="c.pluginId + '/' + c.handlerId" :label="c.pluginName + ' (' + c.pluginId + ')'" />
    </ElSelect>
    <ElCheckbox v-model="remember">{{ t("pluginContrib.externalOpenHandlers.remember") }}</ElCheckbox>
    <p>{{ t("pluginContrib.externalOpenHandlers.noDownload") }}</p>
  </div>
</template>
