<script setup lang="ts">
import { computed } from "vue";
import PluginPublishConfirmDialog from "./PluginPublishConfirmDialog.vue";
import { normalizePluginPublishRecord } from "@/domain/plugins/publish-summary";
import { dismissPluginPublish, pluginPublishEvents } from "@/services/plugin-publish-listener";
const queue = computed(() => pluginPublishEvents.value.flatMap(value => { const record = normalizePluginPublishRecord(value); return record ? [record] : []; }));
const current = computed(() => queue.value[0]);
// A new ElDialog instance receives a fresh modal stack position after sign-in,
// bringing confirmation back above the settings dialog opened by "Sign in".
const dialogKey = computed(() => current.value ? `${current.value.id}:${current.value.status === "awaiting_login" ? "login" : "publish"}` : "");
</script>
<template>
  <PluginPublishConfirmDialog v-if="current" :key="dialogKey" :record="current" :queued="queue.length - 1" @close="dismissPluginPublish(current.id)" />
</template>
