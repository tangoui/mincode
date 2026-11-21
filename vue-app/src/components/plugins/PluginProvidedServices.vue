<script setup lang="ts">
/**
 * Services one plugin declares, shown in the plugin details drawer (spec
 * §12): live ones as usual, the others greyed with why they are not offered
 * (plugin disabled, not active in this workspace, plugin unavailable). Only
 * this view lists inactive services. Read from the host on mount (also in a
 * popped-out plugin manager) and again when plugin state, contributions or
 * the workspace change.
 */
import { computed, onBeforeUnmount, shallowRef, watch } from "vue";
import { usePluginServiceLabels } from "@/composables/use-plugin-service-labels";
import type { PluginDeclaredServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { parseContributionError } from "@/services/plugin-contribution-requests";
import { loadDeclaredPluginServices, onPluginServicesChanged } from "@/services/plugin-services";

const props = defineProps<{ pluginId: string }>();
const { t } = useVueI18n();
const { entryTitle, riskType, riskLabel, shapeText } = usePluginServiceLabels();

const declared = shallowRef<PluginDeclaredServiceEntry[]>([]);
/** The last load's failure; undefined while none failed. */
const loadError = shallowRef<string | undefined>();
let sequence = 0;

const items = computed(() => [...declared.value].sort((left, right) => left.service.localeCompare(right.service)));

/** Reads the declared services; stale responses are ignored and a failure keeps the last list. */
async function load() {
  const current = ++sequence;
  const pluginId = props.pluginId;
  try {
    const entries = await loadDeclaredPluginServices(pluginId);
    if (current !== sequence) return;
    declared.value = entries;
    loadError.value = undefined;
  } catch (error) {
    if (current !== sequence) return;
    const { code, detail } = parseContributionError(error);
    loadError.value = detail || code;
  }
}

// Nothing shows while the first load is on its way; a failure is named instead of looking like "no services".
watch(
  () => props.pluginId,
  () => {
    declared.value = [];
    loadError.value = undefined;
    void load();
  },
  { immediate: true },
);
const stopChanges = onPluginServicesChanged(() => void load());
onBeforeUnmount(() => {
  sequence++;
  stopChanges();
});
</script>

<template>
  <section v-if="items.length || loadError !== undefined" class="plugin-provided-services" :aria-label="t('plugins.services.provided')">
    <h3>{{ t("plugins.services.provided") }}</h3>
    <p v-if="loadError !== undefined" class="plugin-provided-services__error" role="alert">
      {{ t("plugins.services.catalog.loadFailed", { detail: loadError }) }}
    </p>
    <ul v-if="items.length">
      <li
        v-for="item in items"
        :key="item.entryId"
        :class="{ 'plugin-provided-services__item--inactive': !item.active }"
      >
        <div class="plugin-provided-services__main">
          <strong>{{ entryTitle(item) }}</strong>
          <code>{{ item.service }}</code>
          <small>{{ shapeText(item.input) }} → {{ shapeText(item.output) }}</small>
        </div>
        <el-tag v-if="!item.active" size="small" type="info" effect="plain">
          {{ t(`plugins.services.inactive.${item.inactiveReason}`) }}
        </el-tag>
        <el-tag v-if="item.isDefault" size="small" effect="plain">{{ t("plugins.services.default.tag") }}</el-tag>
        <el-tag size="small" :type="riskType(item.risk)">{{ riskLabel(item.risk) }}</el-tag>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.plugin-provided-services {
  margin: 16px 0;
}
.plugin-provided-services h3 {
  margin: 0 0 8px;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
}
.plugin-provided-services__error {
  margin: 0 0 8px;
  color: var(--app-danger);
  font-size: 12px;
}
.plugin-provided-services ul {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.plugin-provided-services li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}
.plugin-provided-services__item--inactive {
  background: var(--app-surface-muted);
}
.plugin-provided-services__item--inactive .plugin-provided-services__main {
  opacity: 0.6;
}
.plugin-provided-services__main {
  display: grid;
  flex: 1;
  min-width: 0;
  gap: 2px;
}
.plugin-provided-services__main strong {
  overflow: hidden;
  color: var(--app-text);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plugin-provided-services__main code {
  font-family: var(--app-font-mono);
}
.plugin-provided-services__main code,
.plugin-provided-services__main small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
