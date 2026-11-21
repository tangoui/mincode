<script setup lang="ts">
import { computed } from "vue";
import { formatDataBytes } from "@/domain/settings/data-usage";
import { visibleTerminalText } from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";
import type { JdtlsBundleStatus } from "@/services/jdtls-bundles";

// Body of the confirmation before a plugin's jdtls bundles (e.g. java-debug)
// load into the built-in Java language service. Every value is what the host
// hashed (plugin_host_jdtls_bundles_status), shown as plain text, so the user
// confirms exactly the jars the host will load. Dependency jars (no commands)
// are confirmed with their entry's jar as one group: the summary shows the
// file count and total size, and their names and hashes fold away.
const props = defineProps<{ bundle: JdtlsBundleStatus }>();

const { t } = useVueI18n();
const plugin = computed(() => visibleTerminalText(props.bundle.pluginName || props.bundle.pluginId));
const jars = computed(() =>
  props.bundle.jars
    .filter((jar) => !jar.dependencyOf)
    .map((jar) => ({ name: visibleTerminalText(jar.name), sha256: jar.sha256, commands: (jar.commands ?? []).map(visibleTerminalText) })),
);
const dependencies = computed(() =>
  props.bundle.jars.filter((jar) => jar.dependencyOf).map((jar) => ({ name: visibleTerminalText(jar.name), sha256: jar.sha256 })),
);
const summary = computed(() => ({
  count: props.bundle.jars.length,
  size: formatDataBytes(props.bundle.jars.reduce((total, jar) => total + jar.bytes, 0)),
}));
</script>

<template>
  <el-scrollbar class="app-jdtls-bundle-confirm__scrollbar">
    <div class="app-jdtls-bundle-confirm">
      <p>
        {{ t("debug.jdtls.confirmIntro", { plugin, version: visibleTerminalText(bundle.pluginVersion), id: bundle.pluginId }) }}
      </p>
      <el-alert
        v-if="bundle.previouslyTrusted"
        type="warning"
        :closable="false"
        show-icon
        :title="t('debug.jdtls.confirmChanged')"
      />
      <p data-jdtls-summary>{{ t("debug.jdtls.confirmSummary", summary) }}</p>
      <dl class="app-jdtls-bundle-confirm__spec">
        <dt>{{ t("debug.jdtls.confirmJars") }}</dt>
        <dd>
          <template v-for="jar in jars" :key="jar.name">
            <code dir="ltr" data-jdtls-jar>{{ jar.name }}  {{ jar.sha256 }}</code>
            <code v-for="command in jar.commands" :key="command" dir="ltr" data-jdtls-command>{{ command }}</code>
          </template>
        </dd>
        <template v-if="dependencies.length">
          <dt>{{ t("debug.jdtls.confirmDependencies") }}</dt>
          <dd>
            <details data-jdtls-dependencies class="app-jdtls-bundle-confirm__dependencies">
              <summary>{{ t("debug.jdtls.confirmDependencyCount", { count: dependencies.length }) }}</summary>
              <div class="app-jdtls-bundle-confirm__dependency-list">
                <code v-for="(jar, index) in dependencies" :key="index" dir="ltr" data-jdtls-dependency>{{ jar.name }}  {{ jar.sha256 }}</code>
              </div>
            </details>
          </dd>
        </template>
      </dl>
      <p class="app-jdtls-bundle-confirm__note">{{ t("debug.jdtls.confirmLoadsCode") }}</p>
      <p class="app-jdtls-bundle-confirm__note">{{ t("debug.jdtls.confirmRestart") }}</p>
    </div>
  </el-scrollbar>
</template>

<style scoped>
.app-jdtls-bundle-confirm__scrollbar {
  flex: 1;
  min-width: 0;
  min-height: 0;
}

.app-jdtls-bundle-confirm {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding-right: 12px;
}

.app-jdtls-bundle-confirm p {
  margin: 0;
  overflow-wrap: anywhere;
}

.app-jdtls-bundle-confirm__spec {
  display: grid;
  gap: 4px;
  margin: 0;
}

.app-jdtls-bundle-confirm__spec dt {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.app-jdtls-bundle-confirm__spec dd {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  margin: 0;
}

.app-jdtls-bundle-confirm__spec code {
  padding: 2px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface-muted);
  font-family: var(--app-font-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-jdtls-bundle-confirm__dependencies summary {
  cursor: pointer;
  font-size: 12px;
}

.app-jdtls-bundle-confirm__dependency-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 4px;
}

.app-jdtls-bundle-confirm__note {
  color: var(--app-text-secondary);
  font-size: 12px;
}
</style>
