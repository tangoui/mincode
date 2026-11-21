<script setup lang="ts">
/**
 * The action buttons of a plugin notification (host.notify actions). Loaded
 * on demand by NotificationCenterButton.vue only when a notification has
 * actions, so neither this template nor the runner's dependency list sits in
 * the entry chunk. The buttons stay outside the list item's main button (no
 * nested interactive elements); the button text is the plugin's action title,
 * the accessible name and tooltip add the plugin's name.
 */
import { ref } from "vue";
import { useVueI18n } from "@/i18n/messages";
import type { AppNotificationAction } from "@/domain/notifications/plugin-actions";

const props = defineProps<{ actions: readonly AppNotificationAction[]; pluginName: string }>();
const emit = defineEmits<{ activate: [] }>();
const { t } = useVueI18n();
const running = ref(false);

function actionLabel(action: AppNotificationAction) {
  return t("notifications.pluginActionLabel", { action: action.title, plugin: props.pluginName });
}

/**
 * The parent marks the notification read and closes the drawer first, so a
 * confirmation, parameter form or opened view is not hidden behind it. The
 * runner (confirm dialog, command runtime) loads on demand and reports its
 * own failures.
 */
function run(action: AppNotificationAction) {
  // One run per notification at a time: a fast double-click must not start a second run
  // (or a second confirmation) while the first is still loading, confirming or executing.
  if (running.value) return;
  running.value = true;
  emit("activate");
  void import("@/services/plugin-shell-commands")
    .then((module) => module.runShellCommand("notification", action.pluginId, action.command))
    .catch(() => undefined)
    .finally(() => {
      running.value = false;
    });
}
</script>

<template>
  <div
    class="notification-list__actions"
    role="group"
    :aria-label="t('notifications.pluginActionsGroup', { plugin: pluginName })"
  >
    <el-button
      v-for="action in actions"
      :key="action.id"
      size="small"
      native-type="button"
      :title="actionLabel(action)"
      :aria-label="actionLabel(action)"
      @click="run(action)"
    >
      {{ action.title }}
    </el-button>
  </div>
</template>
