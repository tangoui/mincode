<script setup lang="ts">
/**
 * Transcript of a plugin session (contributes.sessionSources). The text is
 * untrusted third-party content: it is rendered by text interpolation only
 * (white-space: pre-wrap), never as Markdown or HTML, so markup and links
 * stay literal characters and nothing can open a file or URL.
 */
import "@/styles/workspaces/sessions/plugin-sources.css";
import { Document } from "@element-plus/icons-vue";
import type { PluginSessionMessage } from "@/domain/plugins/session-sources";
import { useVueI18n } from "@/i18n/messages";

defineProps<{
  messages: PluginSessionMessage[];
  /** The plugin had more messages; only the last ones are shown. */
  truncated: boolean;
}>();

const { t } = useVueI18n();

const ROLE_LABELS = {
  user: "sessions.role.user",
  assistant: "sessions.role.assistant",
  system: "sessions.role.system",
  tool: "sessions.role.tool",
} as const;

function roleClass(message: PluginSessionMessage) {
  return message.role === "user" || message.role === "assistant" ? `is-${message.role}` : "is-system";
}

function formatTime(value: number | undefined) {
  if (value === undefined) return "";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(value);
}
</script>

<template>
  <div class="sessions-message-stream app-plugin-session-transcript">
    <p v-if="truncated" class="app-plugin-session-transcript__notice" role="note">{{ t("sessions.pluginMessagesTruncated") }}</p>
    <article
      v-for="(message, index) in messages"
      :key="index"
      class="sessions-message"
      :class="roleClass(message)"
    >
      <header>
        <span>{{ t(ROLE_LABELS[message.role]) }}</span>
        <small>{{ [message.model, formatTime(message.timestamp)].filter(Boolean).join(" · ") }}</small>
      </header>
      <div class="app-plugin-session-transcript__body">
        <p v-if="message.text" class="app-plugin-session-transcript__text" data-testid="plugin-session-message-text">{{ message.text }}</p>
        <p v-else class="sessions-message__empty">{{ t("sessions.emptyMessage") }}</p>
        <span v-if="message.truncated" class="app-plugin-session-transcript__truncated">{{ t("sessions.messageTruncated") }}</span>
      </div>
    </article>
    <div v-if="!messages.length" class="sessions-conversation-empty is-inline">
      <el-icon :size="34"><Document /></el-icon>
      <strong>{{ t("sessions.noMessages") }}</strong>
    </div>
  </div>
</template>
