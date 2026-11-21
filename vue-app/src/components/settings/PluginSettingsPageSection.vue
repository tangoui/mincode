<script setup lang="ts">
/**
 * Settings > Plugins: one plugin's own settings page (contributes.settingsPages).
 * The host draws the header outside the iframe from the manifest name and ID
 * only; the plugin cannot remove or restyle it. The page itself runs in the
 * standard plugin view sandbox (PluginViewFrame, variant "settings"): no file
 * drop, no open requests, no AI settings writes and never parked in the warm
 * pool, so leaving Settings or switching plugins destroys the session. The page
 * gets no host settings access beyond the view's normal transport.
 */
import "@/styles/workspaces/plugins/workspace.css";
import "@/styles/workspaces/settings/plugin-settings-page.css";
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Box } from "@element-plus/icons-vue";
import PluginViewFrame from "@/components/plugins/PluginViewFrame.vue";
import { useVueI18n } from "@/i18n/messages";
import { useWorkbenchStore } from "@/stores/workbench";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type { PluginSettingsPage } from "@/domain/plugins/settings-pages";

/** `autofocus`: move focus to the host header on mount (the user just switched to this page). */
const props = defineProps<{ page: PluginSettingsPage; autofocus?: boolean }>();
/** "Open in tab": the tab opens behind the Settings dialog, so Settings closes itself. */
const emit = defineEmits<{ navigate: [] }>();

const { t, localeChain } = useVueI18n();
const router = useRouter();
const workbench = useWorkbenchStore();

type FrameStatus = "loading" | "ready" | "unavailable" | "error";
const status = ref<FrameStatus>("loading");
// Bumped by "retry": a new key mounts a fresh frame and session.
const frameKey = ref(0);
const failure = ref("");
const header = ref<HTMLElement>();
const frameBox = ref<HTMLElement>();

const pageTitle = computed(() => resolvePluginTitle({ title: props.page.title, titleLocalized: props.page.titleLocalized }, localeChain.value) || props.page.view);
const pluginName = computed(() => props.page.pluginName || props.page.pluginId);
const failed = computed(() => status.value === "error" || status.value === "unavailable");

watch(
  () => [props.page.pluginId, props.page.view, props.page.activeInWorkspace] as const,
  () => {
    status.value = "loading";
    failure.value = "";
  },
);

function onFrameStatus(next: FrameStatus, description: string) {
  status.value = next;
  failure.value = next === "error" || next === "unavailable" ? description : "";
}

// The retry button disappears with the alert: keep focus in the page box instead of <body>.
async function retry() {
  status.value = "loading";
  failure.value = "";
  frameKey.value += 1;
  await nextTick();
  frameBox.value?.focus();
}

function openAsTab() {
  openPluginViewTab(workbench, router, props.page.pluginId, props.page.view, pageTitle.value);
  emit("navigate");
}

onMounted(() => {
  if (props.autofocus) header.value?.focus();
});
</script>

<template>
  <section class="app-plugin-settings-page" :aria-label="pageTitle">
    <header ref="header" class="app-plugin-settings-page__header" role="note" tabindex="-1">
      <el-icon class="app-plugin-settings-page__icon" aria-hidden="true"><Box /></el-icon>
      <span class="app-plugin-settings-page__source">{{ t("pluginContrib.settingsPages.header", { name: pluginName, id: page.pluginId }) }}</span>
      <el-button size="small" native-type="button" @click="openAsTab">{{ t("pluginContrib.settingsPages.openAsTab") }}</el-button>
    </header>

    <el-empty
      v-if="!page.activeInWorkspace"
      class="app-plugin-settings-page__inactive"
      :image-size="64"
      :description="t('pluginContrib.settingsPages.inactive')"
    />
    <template v-else>
      <el-alert v-if="failed" type="error" :closable="false" :title="t('pluginContrib.settingsPages.loadFailed')">
        <p v-if="failure" class="app-plugin-settings-page__failure">{{ failure }}</p>
        <el-button size="small" native-type="button" @click="retry">{{ t("pluginContrib.settingsPages.retry") }}</el-button>
      </el-alert>
      <div ref="frameBox" class="app-plugin-settings-page__frame" tabindex="-1">
        <PluginViewFrame
          :key="frameKey"
          :plugin-id="page.pluginId"
          :view-id="page.view"
          :active="true"
          drop-target-id=""
          variant="settings"
          :frame-title="t('pluginContrib.settingsPages.frameTitle', { name: pluginName, title: pageTitle })"
          @status="onFrameStatus"
        />
      </div>
    </template>
  </section>
</template>
