<script setup lang="ts">
/**
 * Banner at the top of Settings → AI → Connectors / Skills → Discover pointing
 * to the official connector & skill market plugin. Opens its view when the
 * plugin is enabled in this workspace; otherwise opens the Plugin Manager
 * searching for it. Outside the desktop runtime the host is never asked and
 * the install button is shown.
 */
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import {
  AI_MARKET_PLUGIN_ID,
  AI_MARKET_VIEW_ID,
  aiMarketLinkAction,
  type AiMarketLinkAction,
} from "@/domain/ai/ai-market-link";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { useVueI18n } from "@/i18n/messages";
import { listPlugins } from "@/services/plugin-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useWorkbenchStore } from "@/stores/workbench";

/** Emitted before navigating away (the settings dialog closes itself). */
const emit = defineEmits<{ navigate: [] }>();

const { t } = useVueI18n();
const router = useRouter();
const workbench = useWorkbenchStore();
const action = ref<AiMarketLinkAction>("install");

onMounted(async () => {
  if (!isDesktopRuntime()) return;
  try {
    action.value = aiMarketLinkAction(await listPlugins());
  } catch {
    // Without the plugin list the safe choice is the Plugin Manager, which shows the real state.
    action.value = "install";
  }
});

async function follow() {
  if (action.value === "open") {
    if (!openPluginViewTab(workbench, router, AI_MARKET_PLUGIN_ID, AI_MARKET_VIEW_ID, t("settings.aiMarket.viewTitle"))) return;
    emit("navigate");
    return;
  }
  if (!workbench.openView("plugins")) return;
  emit("navigate");
  await router.push({ name: "plugins", query: { search: AI_MARKET_PLUGIN_ID } });
}
</script>

<template>
  <el-alert class="settings-ai-market-banner" type="info" :closable="false" show-icon>
    <template #title>
      <span class="settings-ai-market-banner__body">
        <span>{{ t("settings.aiMarket.banner") }}</span>
        <el-button
          data-testid="ai-market-banner-action"
          size="small"
          type="primary"
          plain
          @click="follow"
          >{{ t(action === "open" ? "settings.aiMarket.open" : "settings.aiMarket.install") }}</el-button
        >
      </span>
    </template>
  </el-alert>
</template>
