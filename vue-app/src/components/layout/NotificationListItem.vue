<script setup lang="ts">
import { Close } from "@element-plus/icons-vue";
import { defineAsyncComponent } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { notificationTimeLabel } from "@/domain/notifications/time-label";
import type { AppNotification, NotificationSource } from "@/stores/notifications";

// One notification row. A row of its own so a new notification renders only itself:
// unchanged rows keep their item object, so Vue skips them (the list holds up to 200).

// Plugin notification buttons are rare: their template and the lazy runner stay out of the entry chunk.
const NotificationActionButtons = defineAsyncComponent(() => import("./NotificationActionButtons.vue"));

const props = defineProps<{ item: AppNotification }>();
const emit = defineEmits<{ activate: [item: AppNotification]; activateAction: [item: AppNotification]; remove: [id: string] }>();

const { t, intlLocale } = useVueI18n();

function timeLabel(timestamp: number) {
  return notificationTimeLabel(timestamp, intlLocale.value);
}

const sourceKeys = {
  db: "notifications.source.db",
  git: "notifications.source.git",
  ssh: "notifications.source.ssh",
  update: "notifications.source.update",
  ai: "notifications.source.ai",
  plugin: "notifications.source.plugin",
  system: "notifications.source.system",
} as const;

function sourceLabel(source: NotificationSource) {
  return t(sourceKeys[source]);
}

const tagTypes = {
  info: "info",
  success: "success",
  warning: "warning",
  error: "danger",
  progress: "primary",
} as const;

function progressPercentage(item: AppNotification) {
  const { progress } = item;
  if (!progress?.total || progress.total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((progress.current / progress.total) * 100)));
}

function progressLabel(item: AppNotification) {
  const { progress } = item;
  if (!progress) return "";
  return progress.total === undefined
    ? String(progress.current)
    : `${progress.current}/${progress.total}`;
}
</script>

<template>
  <el-card
    class="notification-list__item"
    :class="[`is-${props.item.level}`, { 'is-unread': !props.item.readAt }]"
    shadow="never"
  >
    <div class="notification-list__item-body">
      <button
        type="button"
        class="notification-list__content"
        :aria-label="props.item.title"
        @click="emit('activate', props.item)"
      >
        <header>
          <div class="notification-list__heading">
            <el-tag
              class="notification-list__source"
              :type="tagTypes[props.item.level]"
              effect="light"
              size="small"
            >
              {{ sourceLabel(props.item.source) }}
            </el-tag>
            <strong>{{ props.item.title }}</strong>
          </div>
          <time :datetime="new Date(props.item.updatedAt).toISOString()">{{ timeLabel(props.item.updatedAt) }}</time>
        </header>
        <p v-if="props.item.message">{{ props.item.message }}</p>
        <p v-if="props.item.detail" class="notification-list__detail">{{ props.item.detail }}</p>
        <div v-if="props.item.progress" class="notification-list__progress">
          <el-progress
            v-if="props.item.progress.total !== undefined"
            :percentage="progressPercentage(props.item)"
            :show-text="false"
            :stroke-width="4"
          />
          <span>{{ progressLabel(props.item) }}</span>
        </div>
      </button>
      <el-button
        class="notification-list__remove"
        text
        native-type="button"
        :icon="Close"
        :aria-label="t('common.close')"
        @click="emit('remove', props.item.id)"
      />
    </div>
    <NotificationActionButtons
      v-if="props.item.actions?.length"
      :actions="props.item.actions"
      :plugin-name="props.item.pluginName || props.item.actions[0].pluginId"
      @activate="emit('activateAction', props.item)"
    />
  </el-card>
</template>
