<script setup lang="ts">
// Hosts the parameter form of plugin action commands with an `inputSchema`.
// Plugin commands are registered at startup (services/plugin-commands.ts), so
// they can run from keybindings, menus or the palette; this host is mounted
// lazily by App.vue after the first request and stays mounted afterwards.
import { ElMessage } from "element-plus";
import PluginCommandParamsDialog from "@/components/plugins/PluginCommandParamsDialog.vue";
import { useVueI18n } from "@/i18n/messages";
import {
  cancelPluginCommandParams,
  confirmPluginCommandParams,
  pluginCommandParamRequest,
} from "@/services/plugin-commands";

const { t } = useVueI18n();

async function confirm(values: Record<string, string | number | boolean | undefined>) {
  const title = pluginCommandParamRequest.value?.title ?? "";
  try {
    await confirmPluginCommandParams(values);
  } catch {
    ElMessage.error(t("commandPalette.actionFailed", { name: title }));
  }
}
</script>

<template>
  <PluginCommandParamsDialog
    :request="pluginCommandParamRequest"
    @confirm="confirm"
    @cancel="cancelPluginCommandParams"
  />
</template>
