<script setup lang="ts">
import { defineAsyncComponent, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Tickets, Unlock } from "@element-plus/icons-vue";
import { fullTrustDisplayText as clean, type FullTrustListItem } from "@/domain/plugins/full-trust";
import { useVueI18n } from "@/i18n/messages";
import { listFullTrust, setFullTrust } from "@/services/plugin-full-trust";
import "@/styles/workspaces/plugins/plugin-full-trust.css";

// Toolbar list of every plugin under full trust (spec §七): name, version,
// when it was turned on, revoke, and its record. Revoking never asks.
const PluginFullTrustAudit = defineAsyncComponent(() => import("@/components/plugins/PluginFullTrustAudit.vue"));

const props = defineProps<{ refreshToken: number }>();
const emit = defineEmits<{ changed: [pluginId: string] }>();
const { t, intlLocale } = useVueI18n();
const items = ref<FullTrustListItem[]>([]);
const busyId = ref("");
const audit = ref<{ pluginId: string; name: string }>();
const auditVisible = ref(false);
let generation = 0;

async function load() {
  const request = ++generation;
  try {
    const result = await listFullTrust();
    if (request === generation) items.value = result;
  } catch {
    if (request === generation) items.value = [];
  }
}

watch(() => props.refreshToken, () => { void load(); }, { immediate: true });

function grantedText(at: number) {
  return new Date(at).toLocaleString(intlLocale.value);
}

async function revoke(item: FullTrustListItem) {
  if (busyId.value) return;
  busyId.value = item.pluginId;
  try {
    await setFullTrust(item.pluginId, false);
    items.value = items.value.filter((entry) => entry.pluginId !== item.pluginId);
    emit("changed", item.pluginId);
  } catch (error) {
    ElMessage.error(t("plugins.fullTrust.setFailed", { error: error instanceof Error ? error.message : String(error) }));
  } finally {
    busyId.value = "";
  }
}

function openAudit(item: FullTrustListItem) {
  audit.value = { pluginId: item.pluginId, name: clean(item.name) };
  auditVisible.value = true;
}
</script>

<template>
  <el-popover trigger="click" placement="bottom-end" :width="380" popper-class="plugin-full-trust-popper" role="dialog" @show="load">
    <template #reference>
      <el-button text type="danger" :icon="Unlock" class="plugin-full-trust-summary__button">
        {{ t("plugins.fullTrust.summary", { count: items.length }) }}
      </el-button>
    </template>
    <ul class="plugin-full-trust-summary" :aria-label="t('plugins.fullTrust.badge')">
      <li v-for="item in items" :key="item.pluginId" class="plugin-full-trust-summary__item">
        <div class="plugin-full-trust-summary__text">
          <strong :title="clean(item.name)"><bdi class="plugin-full-trust__value">{{ clean(item.name) }}</bdi></strong>
          <small :title="`${clean(item.pluginId)} v${clean(item.version)}`">v<bdi class="plugin-full-trust__value">{{ clean(item.version) }}</bdi> · {{ grantedText(item.grantedAt) }}</small>
        </div>
        <el-button
          text
          size="small"
          :icon="Tickets"
          :title="t('plugins.fullTrust.auditTitle', { name: clean(item.name) })"
          :aria-label="t('plugins.fullTrust.auditTitle', { name: clean(item.name) })"
          @click="openAudit(item)"
        />
        <el-button link type="danger" size="small" :loading="busyId === item.pluginId" :disabled="!!busyId" @click="revoke(item)">
          {{ t("plugins.fullTrust.revoke") }}
        </el-button>
      </li>
    </ul>
  </el-popover>
  <PluginFullTrustAudit
    v-if="audit"
    v-model="auditVisible"
    :plugin-id="audit.pluginId"
    :plugin-name="audit.name"
  />
</template>
