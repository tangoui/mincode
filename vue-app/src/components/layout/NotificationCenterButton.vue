<script setup lang="ts">
import { Bell } from "@element-plus/icons-vue";
import { computed, ref } from "vue";
import NotificationListItem from "./NotificationListItem.vue";
import { useVueI18n } from "@/i18n/messages";
import { requestNotificationNavigation } from "@/services/notification-navigation";
import {
  type AppNotification,
  type NotificationSource,
  useNotificationsStore,
} from "@/stores/notifications";

withDefaults(defineProps<{ buttonClass?: string }>(), {
  buttonClass: "app-header-button app-notification-button",
});

const notifications = useNotificationsStore();
const { t } = useVueI18n();
const visible = ref(false);
const hasReadNotifications = computed(() => notifications.items.some((item) => Boolean(item.readAt)));

function fallbackTarget(source: NotificationSource) {
  if (source === "ai") return { kind: "ai" } as const;
  const views = { db: "database", ssh: "terminal", update: "settings" } as const;
  const view = views[source as keyof typeof views];
  return view ? { kind: "view", view } as const : undefined;
}

function activateNotification(item: AppNotification) {
  notifications.markRead(item.id);
  const target = item.target ?? fallbackTarget(item.source);
  if (!target) return;
  visible.value = false;
  requestNotificationNavigation(target);
}

function activateNotificationAction(item: AppNotification) {
  notifications.markRead(item.id);
  visible.value = false;
}
</script>

<template>
  <el-button
    :class="buttonClass"
    text
    native-type="button"
    :aria-label="t('common.notifications')"
    :title="t('common.notifications')"
    @click="visible = true"
  >
    <el-icon><Bell /></el-icon>
    <span v-if="notifications.unreadCount" class="app-notification-button__badge">{{
      notifications.unreadCount > 99 ? "99+" : notifications.unreadCount
    }}</span>
  </el-button>

  <el-drawer
    v-model="visible"
    class="notification-drawer"
    :title="t('notifications.title')"
    size="390px"
    append-to-body
  >
    <template #header>
      <div class="notification-drawer-title">
        <strong>{{ t("notifications.title") }}</strong>
        <span />
        <el-button
          class="notification-drawer-title__action"
          text
          size="small"
          native-type="button"
          :disabled="!notifications.unreadCount"
          @click="notifications.markAllRead()"
        >
          {{ t("notifications.markAllRead") }}
        </el-button>
        <el-button
          class="notification-drawer-title__action"
          text
          size="small"
          native-type="button"
          :disabled="!hasReadNotifications"
          @click="notifications.clearRead()"
        >
          {{ t("notifications.clearRead") }}
        </el-button>
      </div>
    </template>

    <el-scrollbar class="notification-drawer__scroll">
      <div class="notification-list">
        <NotificationListItem
          v-for="item in notifications.items"
          :key="item.id"
          :item="item"
          @activate="activateNotification"
          @activate-action="activateNotificationAction"
          @remove="notifications.remove"
        />
        <el-empty v-if="!notifications.items.length" :description="t('notifications.empty')" />
      </div>
    </el-scrollbar>
  </el-drawer>
</template>
