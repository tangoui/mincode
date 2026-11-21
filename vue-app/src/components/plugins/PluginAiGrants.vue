<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { MagicStick } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { getPluginAiGrants, setPluginAiGrants, type PluginAiGrants } from "@/services/plugin-service";

// Grants a plugin can never give itself: deciding tool approvals from a chat app,
// and letting its owner turns use the local workspace like a desktop conversation.
const props = defineProps<{ pluginId: string; pluginName: string }>();
const { t } = useVueI18n();
const grants = reactive<PluginAiGrants>({ chatApproval: false, workspaceAccess: false });
const loaded = ref(false);
const saving = ref(false);
const enabledCount = computed(() => Number(grants.chatApproval) + Number(grants.workspaceAccess));

onMounted(async () => {
  try {
    Object.assign(grants, await getPluginAiGrants(props.pluginId));
    loaded.value = true;
  } catch {
    loaded.value = false;
  }
});

// One confirmation that lists every grant still off, then enables them together.
async function enableAll() {
  if (saving.value || enabledCount.value === 2) return;
  const pending: Array<keyof PluginAiGrants> = (["chatApproval", "workspaceAccess"] as const).filter((key) => !grants[key]);
  const confirmKey = (key: keyof PluginAiGrants) => (key === "chatApproval" ? "plugins.aiGrants.chatApprovalConfirm" : "plugins.aiGrants.workspaceConfirm");
  try {
    await ElMessageBox.confirm(
      h("div", { style: "display: grid; gap: 8px" }, pending.map((key) => h("p", { style: "margin: 0" }, t(confirmKey(key), { name: props.pluginName })))),
      t("plugins.aiGrants.confirmTitle"),
      { type: "warning", confirmButtonText: t("plugins.aiGrants.allow"), cancelButtonText: t("common.cancel"), confirmButtonClass: "el-button--danger" },
    );
  } catch {
    return;
  }
  saving.value = true;
  try {
    Object.assign(grants, await setPluginAiGrants(props.pluginId, { ...grants, chatApproval: true, workspaceAccess: true }));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    saving.value = false;
  }
}

async function toggle(key: keyof PluginAiGrants, value: boolean) {
  if (saving.value) return;
  if (value) {
    try {
      await ElMessageBox.confirm(
        t(key === "chatApproval" ? "plugins.aiGrants.chatApprovalConfirm" : "plugins.aiGrants.workspaceConfirm", { name: props.pluginName }),
        t("plugins.aiGrants.confirmTitle"),
        { type: "warning", confirmButtonText: t("plugins.aiGrants.allow"), cancelButtonText: t("common.cancel"), confirmButtonClass: "el-button--danger" },
      );
    } catch {
      return;
    }
  }
  saving.value = true;
  try {
    Object.assign(grants, await setPluginAiGrants(props.pluginId, { ...grants, [key]: value }));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <el-popover v-if="loaded" trigger="click" placement="top-end" :width="300" popper-class="plugin-ai-grants-popper">
    <template #reference>
      <el-button text :icon="MagicStick" :class="{ 'is-granted': enabledCount > 0 }">
        {{ t("plugins.aiGrants.button") }}<small class="plugin-ai-grants__count">{{ enabledCount }}/2</small>
      </el-button>
    </template>
    <div class="plugin-ai-grants">
      <div class="plugin-ai-grants__head">
        <div class="plugin-ai-grants__title">{{ t("plugins.aiGrants.title") }}</div>
        <el-button v-if="enabledCount < 2" size="small" type="warning" plain :loading="saving" @click="enableAll">{{ t("plugins.aiGrants.enableAll") }}</el-button>
      </div>
      <div class="plugin-ai-grants__row">
        <div class="plugin-ai-grants__text">
          <strong>{{ t("plugins.aiGrants.chatApproval") }}</strong>
          <small>{{ t("plugins.aiGrants.chatApprovalHint") }}</small>
        </div>
        <el-switch size="small" :model-value="grants.chatApproval" :loading="saving" :aria-label="t('plugins.aiGrants.chatApproval')" @change="toggle('chatApproval', $event === true)" />
      </div>
      <div class="plugin-ai-grants__row">
        <div class="plugin-ai-grants__text">
          <strong>{{ t("plugins.aiGrants.workspace") }}</strong>
          <small>{{ t("plugins.aiGrants.workspaceHint") }}</small>
        </div>
        <el-switch size="small" :model-value="grants.workspaceAccess" :loading="saving" :aria-label="t('plugins.aiGrants.workspace')" @change="toggle('workspaceAccess', $event === true)" />
      </div>
    </div>
  </el-popover>
</template>

<style scoped>
.plugin-ai-grants__count { margin-left: 4px; color: var(--app-text-muted); font-size: 11px; font-weight: 500; }
.is-granted .plugin-ai-grants__count { color: var(--app-warning); }
.plugin-ai-grants { display: grid; gap: 12px; }
.plugin-ai-grants__head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.plugin-ai-grants__title { min-width: 0; color: var(--app-text); font-size: 13px; font-weight: 600; }
.plugin-ai-grants__head .el-button { flex: none; }
.plugin-ai-grants__row { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.plugin-ai-grants__text { display: grid; gap: 3px; min-width: 0; }
.plugin-ai-grants__text strong { color: var(--app-text); font-size: 12px; font-weight: 500; }
.plugin-ai-grants__text small { color: var(--app-text-muted); font-size: 11px; line-height: 1.5; }
.plugin-ai-grants__row .el-switch { flex: none; margin-top: 1px; }
</style>
