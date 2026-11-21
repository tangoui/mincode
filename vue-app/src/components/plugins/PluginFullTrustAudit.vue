<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { fullTrustDisplayText, type FullTrustAuditEntry } from "@/domain/plugins/full-trust";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { clearFullTrustAudit, fullTrustReasonText, getFullTrustAudit } from "@/services/plugin-full-trust";
import "@/styles/workspaces/plugins/plugin-full-trust.css";

// Read-only record of the confirmations full trust skipped, plus grants,
// revocations and lapses (spec §六). Every cell is text interpolation: subject
// and counterpart are plugin-chosen IDs and must never be parsed as markup.
const props = defineProps<{ pluginId: string; pluginName: string; modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t, intlLocale } = useVueI18n();
const entries = ref<FullTrustAuditEntry[]>([]);
const loading = ref(false);
const clearing = ref(false);
let generation = 0;

const SURFACES = new Set(["viewAction", "codeLens", "notification", "tray", "testRun", "pluginCall", "serviceCall", "hostService"]);
const RISKS = new Set(["network", "write", "dangerous"]);

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});
// Newest first.
const rows = computed(() => [...entries.value].reverse().map((entry, index) => ({ key: `${entry.at}-${index}`, ...entry })));

async function load() {
  const request = ++generation;
  loading.value = true;
  try {
    const result = await getFullTrustAudit(props.pluginId);
    if (request === generation) entries.value = result;
  } catch (error) {
    if (request === generation) ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    if (request === generation) loading.value = false;
  }
}

watch(() => [props.modelValue, props.pluginId] as const, ([open]) => { if (open) void load(); }, { immediate: true });

function timeText(entry: FullTrustAuditEntry) {
  const format = (value: number) => new Date(value).toLocaleString(intlLocale.value);
  return entry.lastAt > entry.at ? `${format(entry.at)} – ${format(entry.lastAt)}` : format(entry.at);
}

function surfaceText(entry: FullTrustAuditEntry) {
  if (entry.kind !== "skipped") {
    const kind = t(`plugins.fullTrust.kind.${entry.kind}` as MessageKey);
    return entry.reason ? `${kind} · ${fullTrustReasonText(entry.reason)}` : kind;
  }
  const surface = entry.surface ?? "";
  return SURFACES.has(surface) ? t(`plugins.fullTrust.surface.${surface}` as MessageKey) : fullTrustDisplayText(surface);
}

function riskText(risk: string | undefined) {
  return risk && RISKS.has(risk) ? t(`pluginHostUi.risk.${risk}` as MessageKey) : fullTrustDisplayText(risk ?? "");
}

async function clear() {
  try {
    await ElMessageBox.confirm(
      t("plugins.fullTrust.auditClearConfirm", { name: props.pluginName }),
      t("plugins.fullTrust.auditClear"),
      {
        type: "warning",
        confirmButtonText: t("plugins.fullTrust.auditClear"),
        cancelButtonText: t("common.cancel"),
        confirmButtonClass: "el-button--danger",
        distinguishCancelAndClose: true,
        autofocus: false,
      },
    );
  } catch {
    return;
  }
  clearing.value = true;
  try {
    await clearFullTrustAudit(props.pluginId);
    await load();
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    clearing.value = false;
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('plugins.fullTrust.auditTitle', { name: fullTrustDisplayText(pluginName) })"
    width="min(820px, 92vw)"
    append-to-body
    destroy-on-close
    class="plugin-full-trust-audit"
  >
    <div v-loading="loading" class="plugin-full-trust-audit__body">
      <el-empty v-if="!loading && !rows.length" :image-size="64" :description="t('plugins.fullTrust.auditEmpty')" />
      <el-table v-else :data="rows" row-key="key" size="small" max-height="420">
        <el-table-column :label="t('plugins.fullTrust.auditColumns.time')" min-width="190">
          <template #default="{ row }">{{ timeText(row) }}</template>
        </el-table-column>
        <el-table-column :label="t('plugins.fullTrust.auditColumns.surface')" min-width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ surfaceText(row) }}</template>
        </el-table-column>
        <el-table-column :label="t('plugins.fullTrust.auditColumns.risk')" width="100">
          <template #default="{ row }">{{ riskText(row.risk) }}</template>
        </el-table-column>
        <el-table-column :label="t('plugins.fullTrust.auditColumns.subject')" min-width="140" show-overflow-tooltip>
          <template #default="{ row }"><bdi class="plugin-full-trust__value">{{ fullTrustDisplayText(row.subject ?? "") }}</bdi></template>
        </el-table-column>
        <el-table-column :label="t('plugins.fullTrust.auditColumns.counterpart')" min-width="140" show-overflow-tooltip>
          <template #default="{ row }"><bdi class="plugin-full-trust__value">{{ fullTrustDisplayText(row.counterpart ?? "") }}</bdi></template>
        </el-table-column>
        <el-table-column :label="t('plugins.fullTrust.auditColumns.count')" width="70" align="right">
          <template #default="{ row }">{{ row.count }}</template>
        </el-table-column>
      </el-table>
    </div>
    <template #footer>
      <el-button type="danger" plain :disabled="!rows.length || loading" :loading="clearing" @click="clear">
        {{ t("plugins.fullTrust.auditClear") }}
      </el-button>
      <el-button @click="visible = false">{{ t("common.close") }}</el-button>
    </template>
  </el-dialog>
</template>
