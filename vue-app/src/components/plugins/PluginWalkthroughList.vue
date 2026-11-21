<script setup lang="ts">
/**
 * Walkthroughs of one plugin, shown in the plugin details drawer. Opening
 * one is always a user action (PluginWalkthroughDialog).
 */
import { computed, onMounted, ref } from "vue";
import { Guide } from "@element-plus/icons-vue";
import { completedWalkthroughSteps, walkthroughDescription, walkthroughTitle } from "@/domain/plugins/walkthroughs";
import { useVueI18n } from "@/i18n/messages";
import { usePluginWalkthroughsStore } from "@/stores/plugin-walkthroughs";
import PluginWalkthroughDialog from "@/components/plugins/PluginWalkthroughDialog.vue";

const props = defineProps<{ pluginId: string }>();
const { t, localeChain } = useVueI18n();
const store = usePluginWalkthroughsStore();
const items = computed(() => store.forPlugin(props.pluginId));
const openKey = ref("");
const dialogOpen = ref(false);

onMounted(() => void store.hydrate());

function openWalkthrough(key: string) {
  openKey.value = key;
  dialogOpen.value = true;
}
</script>

<template>
  <section v-if="items.length" class="plugin-walkthrough-list" :aria-label="t('pluginContrib.walkthroughs.section')">
    <h3>{{ t("pluginContrib.walkthroughs.section") }}</h3>
    <ul>
      <li v-for="item in items" :key="item.key">
        <el-icon class="plugin-walkthrough-list__icon"><Guide /></el-icon>
        <div class="plugin-walkthrough-list__main">
          <strong>{{ walkthroughTitle(item, localeChain) }}</strong>
          <small v-if="walkthroughDescription(item, localeChain)">{{ walkthroughDescription(item, localeChain) }}</small>
          <small>{{ t("pluginContrib.walkthroughs.progress", { done: completedWalkthroughSteps(item, store.progress).size, total: item.steps.length }) }}</small>
        </div>
        <el-button size="small" native-type="button" @click="openWalkthrough(item.key)">{{ t("pluginContrib.walkthroughs.open") }}</el-button>
      </li>
    </ul>
    <PluginWalkthroughDialog v-if="openKey" v-model="dialogOpen" :walkthrough-key="openKey" />
  </section>
</template>

<style scoped>
.plugin-walkthrough-list h3 {
  margin: 0 0 8px;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
}
.plugin-walkthrough-list ul {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.plugin-walkthrough-list li {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}
.plugin-walkthrough-list__icon {
  flex: none;
  color: var(--app-accent);
}
.plugin-walkthrough-list__main {
  display: grid;
  flex: 1;
  min-width: 0;
  gap: 2px;
}
.plugin-walkthrough-list__main strong {
  overflow: hidden;
  color: var(--app-text);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plugin-walkthrough-list__main small {
  color: var(--app-text-muted);
  font-size: 11px;
  overflow-wrap: anywhere;
}
</style>
