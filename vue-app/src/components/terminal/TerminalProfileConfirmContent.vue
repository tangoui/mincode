<script setup lang="ts">
import { computed } from "vue";
import {
  formatTerminalArgv,
  formatTerminalEnv,
  visibleTerminalText,
  type TerminalProfileLaunch,
} from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";

// Body of the first-launch confirmation for a plugin terminal profile. Every
// value shown here is the host-resolved launch spec (never the raw manifest),
// so the user confirms exactly what the host will execute.
const props = defineProps<{
  launch: TerminalProfileLaunch;
  changed?: boolean;
}>();

const { t } = useVueI18n();
const commandLine = computed(() => formatTerminalArgv(props.launch.argv));
const envLines = computed(() => formatTerminalEnv(props.launch.env));
const cwd = computed(() => visibleTerminalText(props.launch.cwd));
</script>

<template>
  <div class="app-terminal-profile-confirm__body">
    <p>{{ t("pluginContrib.terminalProfiles.confirmIntro", { plugin: launch.pluginName }) }}</p>
    <el-alert
      v-if="changed"
      type="warning"
      :closable="false"
      show-icon
      :title="t('pluginContrib.terminalProfiles.confirmChanged')"
    />
    <dl class="app-terminal-profile-confirm__spec">
      <dt>{{ t("pluginContrib.terminalProfiles.confirmCommand") }}</dt>
      <dd><code dir="ltr">{{ commandLine }}</code></dd>
      <template v-if="envLines.length">
        <dt>{{ t("pluginContrib.terminalProfiles.confirmEnv") }}</dt>
        <dd>
          <code v-for="line in envLines" :key="line" dir="ltr">{{ line }}</code>
        </dd>
      </template>
      <dt>{{ t("pluginContrib.terminalProfiles.confirmCwd") }}</dt>
      <dd><code dir="ltr">{{ cwd }}</code></dd>
      <template v-if="launch.pluginExecutable && launch.executableSha256">
        <dt>{{ t("pluginContrib.terminalProfiles.confirmExecutable") }}</dt>
        <dd><code dir="ltr">{{ launch.executableSha256 }}</code></dd>
      </template>
    </dl>
    <p class="app-terminal-profile-confirm__note">{{ t("pluginContrib.terminalProfiles.confirmEveryMode") }}</p>
  </div>
</template>

<style scoped>
.app-terminal-profile-confirm__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.app-terminal-profile-confirm__body p {
  margin: 0;
}

.app-terminal-profile-confirm__spec {
  display: grid;
  gap: 4px;
  margin: 0;
}

.app-terminal-profile-confirm__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-terminal-profile-confirm__spec dd {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 160px;
  margin: 0 0 4px;
  overflow: auto;
}

.app-terminal-profile-confirm__spec code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-terminal-profile-confirm__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}
</style>
