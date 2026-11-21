<script setup lang="ts">
import { computed } from "vue";
import { processLaunchTitle } from "@/domain/plugins/title";
import { formatTerminalArgv, formatTerminalEnv, visibleTerminalText } from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";
import type { ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";

// Body of the first-run confirmation for a plugin process launch (tasks,
// languageServers, linters, …). Text only: every value is the host-resolved
// launch (never the raw manifest), shown as plain text, so the user confirms
// exactly what the host will execute. The title is plugin content and is
// labelled as such. `notices` are facts the caller adds (for
// example what an ACP agent receives besides its own launch), listed right
// below the command.
const props = defineProps<{
  launch: ResolvedProcessLaunch;
  changed?: boolean;
  notices?: string[];
}>();

const { t, localeChain } = useVueI18n();
const title = computed(() => visibleTerminalText(processLaunchTitle(props.launch, localeChain.value)));
const commandLine = computed(() => formatTerminalArgv(props.launch.argv));
const envLines = computed(() => formatTerminalEnv(props.launch.env ?? {}));
const cwd = computed(() => visibleTerminalText(props.launch.cwd));
const files = computed(() => {
  const lines: string[] = [];
  if (props.launch.pluginExecutable && props.launch.executableSha256) {
    lines.push(`${visibleTerminalText(props.launch.argv[0] ?? "")}  ${props.launch.executableSha256}`);
  }
  for (const file of props.launch.pluginFiles ?? []) lines.push(`${visibleTerminalText(file.relative)}  ${file.sha256}`);
  return lines;
});
</script>

<template>
  <el-scrollbar class="app-plugin-launch-confirm__scrollbar">
    <div class="app-plugin-launch-confirm__body">
      <p>
        {{ t("pluginContrib.processLaunch.confirmIntro", {
          plugin: visibleTerminalText(launch.pluginName || launch.pluginId),
          id: launch.pluginId,
          title,
        }) }}
      </p>
      <el-alert
        v-if="changed"
        type="warning"
        :closable="false"
        show-icon
        :title="t('pluginContrib.processLaunch.confirmChanged')"
      />
      <dl class="app-plugin-launch-confirm__spec">
        <dt>{{ t("pluginContrib.processLaunch.confirmCommand") }}</dt>
        <dd><code dir="ltr">{{ commandLine }}</code></dd>
        <template v-if="notices?.length">
          <dt>{{ t("pluginContrib.processLaunch.confirmNotices") }}</dt>
          <dd class="app-plugin-launch-confirm__notices">
            <p v-for="notice in notices" :key="notice" data-launch-notice>{{ notice }}</p>
          </dd>
        </template>
        <template v-if="envLines.length">
          <dt>{{ t("pluginContrib.processLaunch.confirmEnv") }}</dt>
          <dd>
            <code v-for="line in envLines" :key="line" dir="ltr">{{ line }}</code>
          </dd>
        </template>
        <dt>{{ t("pluginContrib.processLaunch.confirmCwd") }}</dt>
        <dd><code dir="ltr">{{ cwd }}</code></dd>
        <template v-if="files.length">
          <dt>{{ t("pluginContrib.processLaunch.confirmFiles") }}</dt>
          <dd>
            <code v-for="line in files" :key="line" dir="ltr">{{ line }}</code>
          </dd>
        </template>
      </dl>
      <p class="app-plugin-launch-confirm__note">{{ t("pluginContrib.processLaunch.confirmNotSandboxed") }}</p>
      <p class="app-plugin-launch-confirm__note">{{ t("pluginContrib.processLaunch.confirmEveryMode") }}</p>
    </div>
  </el-scrollbar>
</template>

<style scoped>
.app-plugin-launch-confirm__scrollbar {
  flex: 1;
  min-width: 0;
  min-height: 0;
}

.app-plugin-launch-confirm__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding-right: 12px;
}

.app-plugin-launch-confirm__body p {
  margin: 0;
  overflow-wrap: anywhere;
}

.app-plugin-launch-confirm__spec {
  display: grid;
  gap: 4px;
  margin: 0;
}

.app-plugin-launch-confirm__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-plugin-launch-confirm__spec dd {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0 0 4px;
}

.app-plugin-launch-confirm__spec code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-plugin-launch-confirm__spec .app-plugin-launch-confirm__notices p {
  color: var(--app-warning);
  font-size: 12px;
}

.app-plugin-launch-confirm__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}
</style>
