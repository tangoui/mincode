<script setup lang="ts">
/**
 * Settings → Connectors → Installed: MCP servers contributed by enabled
 * plugins (`mcpServers` point). Enabling one asks the user to confirm its
 * exact launch first; the AI assistant then starts it in the open project.
 */
import { computed, onMounted, ref } from "vue";
import { ElMessage } from "element-plus";
import { Connection } from "@element-plus/icons-vue";
import {
  localizedMcpServerText,
  mcpServerAvailable,
  mcpServerErrorCode,
  mcpServerLaunchRoot,
  withMcpStepTimeout,
  type McpServerHostStep,
  type PluginMcpServer,
} from "@/domain/plugins/mcp-servers";
import { useVueI18n } from "@/i18n/messages";
import { ensurePluginMcpServerConfirmed, stopPluginMcpServer } from "@/services/plugin-mcp-server-service";
import type { ProtocolStatus } from "@/services/settings-service";
import { nativeErrorMessage } from "@/services/desktop-bridge";
import { usePluginMcpServersStore } from "@/stores/plugin-mcp-servers";

const props = defineProps<{ statuses: readonly ProtocolStatus[] }>();
const emit = defineEmits<{ changed: [] }>();

const { t, localeChain } = useVueI18n();
const store = usePluginMcpServersStore();
const busy = ref<string | null>(null);

onMounted(() => void store.hydrate());

const entries = computed(() =>
  store.servers.map((server) => ({
    server,
    title: localizedMcpServerText(server.title, server.titleLocalized, localeChain.value),
    description: server.description
      ? localizedMcpServerText(server.description, server.descriptionLocalized, localeChain.value)
      : "",
  })),
);

function projectRoot() {
  return "";
}

function status(server: PluginMcpServer) {
  return props.statuses.find((item) => item.id === server.sessionId);
}

function errorMessage(error: unknown) {
  const code = mcpServerErrorCode(error);
  if (code === "timeout") {
    const step = /^timeout: (prepare|trust|save)\b/.exec(error instanceof Error ? error.message : "")?.[1] as McpServerHostStep | undefined;
    return t("pluginContrib.mcpServers.error.timeout", { step: t(`pluginContrib.mcpServers.step.${step ?? "prepare"}`) });
  }
  const known = ["not_found", "forbidden", "untrusted", "changed", "unavailable", "limit"] as const;
  const match = known.find((item) => item === code);
  return match ? t(`pluginContrib.mcpServers.error.${match}`) : nativeErrorMessage(error);
}

async function onToggle(server: PluginMcpServer, value: string | number | boolean) {
  if (busy.value) return;
  busy.value = server.sessionId;
  try {
    if (value === true) {
      if (!mcpServerAvailable(server, projectRoot())) {
        ElMessage.warning(t("pluginContrib.mcpServers.needsProject"));
        return;
      }
      if (!(await ensurePluginMcpServerConfirmed(server.sessionId, mcpServerLaunchRoot(server, projectRoot())))) return;
      await withMcpStepTimeout(store.setEnabled(server.sessionId, true), "save");
    } else {
      await store.setEnabled(server.sessionId, false);
      await stopPluginMcpServer(server.sessionId).catch(() => false);
      emit("changed");
    }
  } catch (error) {
    ElMessage.error(errorMessage(error));
  } finally {
    busy.value = null;
  }
}

async function stop(server: PluginMcpServer) {
  busy.value = server.sessionId;
  try {
    await stopPluginMcpServer(server.sessionId);
    emit("changed");
  } catch (error) {
    ElMessage.error(errorMessage(error));
  } finally {
    busy.value = null;
  }
}
</script>

<template>
  <section v-if="entries.length" class="plugin-mcp-servers" :aria-label="t('pluginContrib.mcpServers.settingsSection')">
    <header class="plugin-mcp-servers__head">
      <strong>{{ t("pluginContrib.mcpServers.settingsSection") }}</strong>
      <p>{{ t("pluginContrib.mcpServers.settingsHint") }}</p>
    </header>
    <div class="settings-ai-card-grid">
      <article v-for="{ server, title, description } in entries" :key="server.sessionId" class="settings-ai-entry-card">
        <header class="settings-ai-entry-card__head">
          <span class="settings-ai-entry-card__avatar" data-tone="accent" aria-hidden="true">
            <el-icon><Connection /></el-icon>
          </span>
          <div class="settings-ai-entry-card__title">
            <strong :title="title">{{ title }}</strong>
            <small :title="server.pluginName">{{ t("pluginContrib.mcpServers.fromPlugin", { name: server.pluginName }) }}</small>
          </div>
          <el-switch
            :model-value="store.isEnabled(server.sessionId)"
            :loading="busy === server.sessionId"
            :disabled="busy !== null && busy !== server.sessionId"
            size="small"
            :aria-label="`${t('pluginContrib.mcpServers.enabled')}: ${title}`"
            @update:model-value="onToggle(server, $event)"
          />
        </header>
        <p v-if="description" class="settings-ai-entry-card__desc">{{ description }}</p>
        <p class="settings-ai-entry-card__status">
          <i :class="{ 'is-online': status(server)?.state === 'connected' }" />
          <span>{{ status(server)?.error || status(server)?.state || t("settings.mcp.notConnected") }}</span>
        </p>
        <footer class="settings-ai-entry-card__foot">
          <span class="settings-ai-entry-card__chip">stdio</span>
          <div class="settings-ai-entry-card__actions">
            <el-button
              v-if="status(server)?.state === 'connected'"
              size="small"
              :disabled="busy !== null"
              @click="stop(server)"
              >{{ t("settings.mcp.disconnect") }}</el-button
            >
          </div>
        </footer>
      </article>
    </div>
  </section>
</template>

<style scoped>
.plugin-mcp-servers {
  display: grid;
  gap: 10px;
  margin-top: 16px;
}

.plugin-mcp-servers__head {
  display: grid;
  gap: 4px;
}

.plugin-mcp-servers__head strong {
  color: var(--app-text);
  font-size: 13px;
}

.plugin-mcp-servers__head p {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
</style>
