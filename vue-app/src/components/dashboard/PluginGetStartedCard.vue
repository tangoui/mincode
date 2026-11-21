<script setup lang="ts">
/**
 * Dashboard "Get started" card: featured walkthroughs of plugins active in
 * this workspace that the user has neither completed nor dismissed. Nothing
 * opens automatically; each walkthrough opens on click.
 */
import { onMounted, ref } from "vue";
import { Close, Guide } from "@element-plus/icons-vue";
import { completedWalkthroughSteps, walkthroughTitle } from "@/domain/plugins/walkthroughs";
import { useVueI18n } from "@/i18n/messages";
import { usePluginWalkthroughsStore } from "@/stores/plugin-walkthroughs";
import PluginWalkthroughDialog from "@/components/plugins/PluginWalkthroughDialog.vue";

const { t, localeChain } = useVueI18n();
const store = usePluginWalkthroughsStore();
const openKey = ref("");
const dialogOpen = ref(false);

onMounted(() => void store.hydrate());

function openWalkthrough(key: string) {
  openKey.value = key;
  dialogOpen.value = true;
}
</script>

<template>
  <article v-if="store.hydrated && store.featured.length" class="get-started-card">
    <header class="get-started-card__header">
      <div>
        <h2>{{ t("pluginContrib.walkthroughs.getStarted") }}</h2>
        <p>{{ t("pluginContrib.walkthroughs.getStartedDescription") }}</p>
      </div>
      <el-icon class="get-started-card__icon"><Guide /></el-icon>
    </header>
    <ul>
      <li v-for="item in store.featured" :key="item.key">
        <el-button text native-type="button" class="get-started-card__open" @click="openWalkthrough(item.key)">
          <span class="get-started-card__main">
            <strong>{{ walkthroughTitle(item, localeChain) }}</strong>
            <small>{{ item.pluginName }} · {{ t("pluginContrib.walkthroughs.progress", { done: completedWalkthroughSteps(item, store.progress).size, total: item.steps.length }) }}</small>
          </span>
        </el-button>
        <el-button
          text
          size="small"
          native-type="button"
          :icon="Close"
          :aria-label="t('pluginContrib.walkthroughs.dismiss')"
          :title="t('pluginContrib.walkthroughs.dismiss')"
          @click="store.setDismissed(item.key, true)"
        />
      </li>
    </ul>
    <PluginWalkthroughDialog v-if="openKey" v-model="dialogOpen" :walkthrough-key="openKey" />
  </article>
</template>

<style scoped>
.get-started-card {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
}
.get-started-card__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 17px 19px 9px;
}
.get-started-card__header h2 {
  margin: 0 0 4px;
  color: var(--app-text);
  font-size: 14px;
  font-weight: 600;
}
.get-started-card__header p {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
.get-started-card__icon {
  margin-top: 3px;
  color: var(--app-text-muted);
  font-size: 18px;
}
.get-started-card ul {
  margin: 0;
  padding: 0 11px 11px;
  list-style: none;
}
.get-started-card li {
  display: flex;
  align-items: center;
  gap: 4px;
}
.get-started-card__open.el-button {
  flex: 1;
  min-width: 0;
  height: auto;
  justify-content: flex-start;
  padding: 7px 8px;
}
.get-started-card__open.el-button :deep(> span) {
  min-width: 0;
}
.get-started-card__main {
  display: grid;
  min-width: 0;
  gap: 2px;
  text-align: left;
}
.get-started-card__main strong {
  overflow: hidden;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.get-started-card__main small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
