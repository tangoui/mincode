<script setup lang="ts">
import { computed } from "vue";
import { processLaunchTitle } from "@/domain/i18n/localized-title";
import { formatTerminalArgv, formatTerminalEnv, visibleTerminalText } from "@/domain/terminal/launch-display";
import { useVueI18n } from "@/i18n/messages";
import type { DebuggerProcessLaunch } from "@/domain/debug/types";

const props = defineProps<{
  launch: DebuggerProcessLaunch;
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
  <el-scrollbar class="debug-launch-confirm__scrollbar">
    <div class="debug-launch-confirm__body">
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
      <dl class="debug-launch-confirm__spec">
        <dt>{{ t("pluginContrib.processLaunch.confirmCommand") }}</dt>
        <dd><code dir="ltr">{{ commandLine }}</code></dd>
        <template v-if="notices?.length">
          <dt>{{ t("pluginContrib.processLaunch.confirmNotices") }}</dt>
          <dd class="debug-launch-confirm__notices">
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
      <p class="debug-launch-confirm__note">{{ t("pluginContrib.processLaunch.confirmNotSandboxed") }}</p>
      <p class="debug-launch-confirm__note">{{ t("pluginContrib.processLaunch.confirmEveryMode") }}</p>
    </div>
  </el-scrollbar>
</template>

<style scoped>
.debug-launch-confirm__scrollbar {
  flex: 1;
  min-width: 0;
  min-height: 0;
}

.debug-launch-confirm__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding-right: 12px;
}

.debug-launch-confirm__body p {
  margin: 0;
  overflow-wrap: anywhere;
}

.debug-launch-confirm__spec {
  display: grid;
  gap: 4px;
  margin: 0;
}

.debug-launch-confirm__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.debug-launch-confirm__spec dd {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0 0 4px;
}

.debug-launch-confirm__spec code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.debug-launch-confirm__spec .debug-launch-confirm__notices p {
  color: var(--app-warning);
  font-size: 12px;
}

.debug-launch-confirm__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}
</style>
