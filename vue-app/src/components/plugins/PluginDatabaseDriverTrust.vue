<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Key } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import {
  databaseDriverErrorMessage,
  getDatabaseDriverTrust,
  setDatabaseDriverTrust,
} from "@/services/database-driver-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

// Per-plugin trust for database driver plugins: an explicit user choice that
// lets connections bound to this plugin's drivers send their credentials
// without the per-connection confirm. Host-owned; the plugin and AI tools can
// never set it. Turning it on takes one protected confirm, turning it off none.
const props = defineProps<{ pluginId: string; pluginName: string }>();
const { t } = useVueI18n();
const trusted = ref(false);
const loaded = ref(false);
const saving = ref(false);

onMounted(async () => {
  if (!isDesktopRuntime()) return;
  try {
    trusted.value = await getDatabaseDriverTrust(props.pluginId);
    loaded.value = true;
  } catch {
    loaded.value = false;
  }
});

async function toggle(value: boolean) {
  if (saving.value) return;
  if (value) {
    try {
      await ElMessageBox.confirm(
        t("pluginContrib.databaseDrivers.trustConfirmBody", { name: props.pluginName, id: props.pluginId }),
        t("pluginContrib.databaseDrivers.trustConfirmTitle"),
        {
          type: "warning",
          confirmButtonText: t("pluginContrib.databaseDrivers.trustConfirmButton"),
          cancelButtonText: t("common.cancel"),
          confirmButtonClass: "el-button--danger",
          distinguishCancelAndClose: true,
          autofocus: false,
        },
      );
    } catch {
      return;
    }
  }
  saving.value = true;
  try {
    await setDatabaseDriverTrust(props.pluginId, value);
    trusted.value = value;
  } catch (error) {
    ElMessage.error(t("pluginContrib.databaseDrivers.trustFailed", { error: databaseDriverErrorMessage(error) }));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <el-popover v-if="loaded" trigger="click" placement="top-end" :width="320" popper-class="plugin-driver-trust-popper">
    <template #reference>
      <el-button text :icon="Key" :class="{ 'is-trusted': trusted }">
        {{ t("pluginContrib.databaseDrivers.trustButton") }}
      </el-button>
    </template>
    <div class="plugin-driver-trust">
      <div class="plugin-driver-trust__row">
        <div class="plugin-driver-trust__text">
          <strong>{{ t("pluginContrib.databaseDrivers.trustLabel") }}</strong>
          <small>{{ t("pluginContrib.databaseDrivers.trustHint") }}</small>
        </div>
        <el-switch
          size="small"
          :model-value="trusted"
          :loading="saving"
          :aria-label="t('pluginContrib.databaseDrivers.trustLabel')"
          @change="toggle($event === true)"
        />
      </div>
    </div>
  </el-popover>
</template>

<style scoped>
.is-trusted { color: var(--app-warning); }
.plugin-driver-trust__row { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.plugin-driver-trust__text { display: grid; gap: 3px; min-width: 0; }
.plugin-driver-trust__text strong { color: var(--app-text); font-size: 12px; font-weight: 500; }
.plugin-driver-trust__text small { color: var(--app-text-muted); font-size: 11px; line-height: 1.5; }
.plugin-driver-trust__row .el-switch { flex: none; margin-top: 1px; }
</style>
