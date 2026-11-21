<script setup lang="ts">
// Runs plugin services in the main window: App.vue mounts this lazily once
// the window is idle, so services/plugin-services.ts stays out of the entry
// bundle. Starts the sync (service list, call snapshots, editor selection
// menu) and stops it on unmount; the progress/result host loads on the first
// parameter form, call or result and stays mounted afterwards. Also drops the
// content of service result tabs once their tab closes.
import { defineAsyncComponent, onBeforeUnmount, ref, watch } from "vue";
import {
  pluginServiceCalls,
  pluginServiceParamRequest,
  pluginServiceResult,
  startPluginServiceSync,
} from "@/services/plugin-services";
import { installPluginServiceResultTabCleanup } from "@/services/plugin-service-result-tabs";
import { useWorkbenchStore } from "@/stores/workbench";

const PluginServiceCallsHost = defineAsyncComponent(() => import("@/components/plugins/PluginServiceCallsHost.vue"));

const hostLoaded = ref(false);
watch([pluginServiceParamRequest, pluginServiceCalls, pluginServiceResult], ([request, calls, result]) => {
  if (request || calls.length || result) hostLoaded.value = true;
});
onBeforeUnmount(startPluginServiceSync());
onBeforeUnmount(installPluginServiceResultTabCleanup(useWorkbenchStore()));
</script>

<template>
  <PluginServiceCallsHost v-if="hostLoaded" />
</template>
