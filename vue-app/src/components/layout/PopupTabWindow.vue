<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, ref, watchEffect } from "vue";
import { useRouter } from "vue-router";
import NotificationCenterButton from "@/components/layout/NotificationCenterButton.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { getPopupWindowData, isPopupTabData } from "@/services/popup-tab";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useWorkbenchStore } from "@/stores/workbench";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";

const AiAssistantDrawer = defineAsyncComponent(
  () => import("@/components/ai/AiAssistantDrawer.vue"),
);

const router = useRouter();
const workbench = useWorkbenchStore();
const assistant = useAiAssistantStore();
const { t } = useVueI18n();
const loading = ref(true);
const error = ref("");
const aiWindow = ref(false);
// Keep the message key (not translated text) so the title follows locale changes.
const popupTitle = ref<{ key: MessageKey } | { text: string }>();
const popupTitleText = computed(() => {
  const title = popupTitle.value;
  if (!title) return t("app.vueWorkspace");
  return "text" in title ? title.text : t(title.key);
});
watchEffect(() => {
  if (popupTitle.value) document.title = popupTitleText.value;
});

onMounted(async () => {
  try {
    const data = await getPopupWindowData();
    if (!data) {
      error.value = t("app.popup.noData");
      return;
    }
    if (!isPopupTabData(data)) {
      aiWindow.value = true;
      assistant.restoreSnapshot(data.snapshot);
      assistant.show(data.snapshot.mode);
      assistant.fullscreen = true;
      popupTitle.value = { key: "ai.drawer.windowTitle" };
      return;
    }

    workbench.registerPopupTab(data.tab);
    popupTitle.value = data.tab.title ? { text: data.tab.title } : { key: data.tab.titleKey as MessageKey };
    await router.replace(workspaceTabRoute(data.tab));
  } catch {
    error.value = t("app.popup.loadFailed");
  } finally {
    loading.value = false;
  }
});
</script>

<template>
  <div class="popup-window-frame">
    <header class="popup-window-titlebar">
      <span class="popup-window-titlebar__title">{{ popupTitleText }}</span>
      <div class="popup-window-titlebar__actions">
        <NotificationCenterButton button-class="popup-window-titlebar__button app-notification-button" />
      </div>
    </header>

    <div class="popup-window-content">
      <main v-if="loading" class="popup-tab-status">{{ t("app.popup.loading") }}</main>
      <main v-else-if="error" class="popup-tab-status is-error">{{ error }}</main>
      <template v-else-if="aiWindow">
        <AiAssistantDrawer />
      </template>
      <RouterView v-else v-slot="{ Component, route }">
        <KeepAlive include="TerminalWorkspaceView">
          <component :is="Component" :key="route.name" />
        </KeepAlive>
      </RouterView>
    </div>
  </div>
</template>
