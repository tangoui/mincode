<script setup lang="ts">
import { computed } from "vue";
import type { ResolvedPluginLink } from "@/domain/plugins/uri-handlers";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { visibleTerminalText } from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";

// Body of the host confirmation for a plugin deep link (uriHandlers). Text
// only: every value is the host-resolved link, shown as plain text with
// control and bidi characters made visible, so the user sees exactly which
// plugin will open which view or run which read-only command with which
// parameters. Titles are plugin content and are labelled as such.
const props = defineProps<{
  url: string;
  link: ResolvedPluginLink;
}>();

const { t, localeChain } = useVueI18n();
const pluginLabel = computed(() => visibleTerminalText(props.link.pluginName || props.link.pluginId));
const handlerTitle = computed(() =>
  props.link.title ? visibleTerminalText(resolvePluginTitle({ title: props.link.title, titleLocalized: props.link.titleLocalized }, localeChain.value)) : "",
);
const targetLine = computed(() =>
  props.link.target.type === "view"
    ? t("pluginContrib.uriHandlers.confirmTargetView", { title: visibleTerminalText(props.link.target.viewTitle), id: props.link.target.view })
    : t("pluginContrib.uriHandlers.confirmTargetCommand", { title: visibleTerminalText(props.link.target.commandTitle), id: props.link.target.command }),
);
const parameterLines = computed(() => [
  ...Object.entries(props.link.pathParams).map(([name, value]) => `${visibleTerminalText(name)} = ${visibleTerminalText(value)}`),
  ...Object.entries(props.link.params).map(([name, value]) => `${visibleTerminalText(name)} = ${visibleTerminalText(String(value))}`),
]);
</script>

<template>
  <div class="app-plugin-link-confirm__body">
    <p>
      {{ t("pluginContrib.uriHandlers.confirmIntro", {
        plugin: pluginLabel,
        id: link.pluginId,
        version: link.pluginVersion || "?",
      }) }}
    </p>
    <dl class="app-plugin-link-confirm__spec">
      <dt>{{ t("pluginContrib.uriHandlers.confirmTarget") }}</dt>
      <dd><span>{{ targetLine }}</span></dd>
      <template v-if="handlerTitle">
        <dt>{{ t("pluginContrib.uriHandlers.confirmHandler") }}</dt>
        <dd><span>{{ handlerTitle }}</span></dd>
      </template>
      <dt>{{ t("pluginContrib.uriHandlers.confirmLink") }}</dt>
      <dd><code dir="ltr">{{ visibleTerminalText(url) }}</code></dd>
      <template v-if="parameterLines.length">
        <dt>{{ t("pluginContrib.uriHandlers.confirmParams") }}</dt>
        <dd>
          <code v-for="line in parameterLines" :key="line" dir="ltr">{{ line }}</code>
        </dd>
      </template>
    </dl>
    <p class="app-plugin-link-confirm__note">{{ t("pluginContrib.uriHandlers.confirmNote") }}</p>
  </div>
</template>

<style scoped>
.app-plugin-link-confirm__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.app-plugin-link-confirm__body p {
  margin: 0;
  overflow-wrap: anywhere;
}

.app-plugin-link-confirm__spec {
  display: grid;
  gap: 4px;
  margin: 0;
}

.app-plugin-link-confirm__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-plugin-link-confirm__spec dd {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 160px;
  margin: 0 0 4px;
  overflow: auto;
  overflow-wrap: anywhere;
}

.app-plugin-link-confirm__spec code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-plugin-link-confirm__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}
</style>
