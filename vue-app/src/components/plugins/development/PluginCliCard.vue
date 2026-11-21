<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import {
  getMincodeCliStatus,
  installMincodeCli,
  uninstallMincodeCli,
  type MincodeCliStatus,
} from "@/services/mincode-cli-service";

const { t } = useVueI18n();
const status = ref<MincodeCliStatus>();
const busy = ref(false);
let disposed = false;

const unavailable = computed(() => status.value?.state === "unavailable");

const tagType = computed(() => {
  if (status.value?.state === "installed") return "success";
  if (status.value?.state === "conflict") return "warning";
  return "info";
});

const hint = computed(() => {
  const current = status.value;
  if (!current) return "";
  if (current.state === "unavailable") return t("plugins.cli.unavailableHint");
  if (current.state === "conflict") {
    return t("plugins.cli.conflictHint", { path: current.linkPath, target: current.conflictTarget });
  }
  if (current.state === "missing") {
    return current.installable ? installHint(current) : t("plugins.cli.managedHint");
  }
  return "";
});

function installHint(current: MincodeCliStatus) {
  if (current.platform === "linux") return t("plugins.cli.installHintLinux", { path: current.linkPath });
  if (current.platform === "windows") return t("plugins.cli.installHintWindows", { path: current.linkPath });
  return t("plugins.cli.installHint", { path: current.linkPath });
}

async function refresh() {
  if (!isDesktopRuntime()) return;
  try {
    const next = await getMincodeCliStatus();
    if (!disposed) status.value = next;
  } catch (cause) {
    if (!disposed) ElMessage.error(String(cause));
  }
}

async function install() {
  const current = status.value;
  if (!current || busy.value) return;
  const replace = current.state === "conflict";
  if (replace) {
    try {
      await ElMessageBox.confirm(
        t("plugins.cli.replaceConfirm", { path: current.linkPath, target: current.conflictTarget }),
        t("plugins.cli.title"),
        { type: "warning", confirmButtonText: t("plugins.cli.replace"), cancelButtonText: t("common.cancel") },
      );
    } catch {
      return;
    }
  }
  busy.value = true;
  try {
    const next = await installMincodeCli(replace);
    if (disposed) return;
    status.value = next;
    ElMessage.success(t("plugins.cli.installedMessage"));
  } catch (cause) {
    if (!disposed) ElMessage.error(String(cause));
  } finally {
    busy.value = false;
  }
}

async function uninstall() {
  if (busy.value) return;
  busy.value = true;
  try {
    const next = await uninstallMincodeCli();
    if (disposed) return;
    status.value = next;
    ElMessage.success(t("plugins.cli.uninstalledMessage"));
  } catch (cause) {
    if (!disposed) ElMessage.error(String(cause));
  } finally {
    busy.value = false;
  }
}

onMounted(refresh);
onBeforeUnmount(() => {
  disposed = true;
});
</script>

<template>
  <section v-if="status" class="plugin-development-panel__section" aria-labelledby="plugin-cli-title">
    <div class="plugin-development-panel__toolbar">
      <div class="plugin-development-panel__section-title">
        <h3 id="plugin-cli-title">{{ t("plugins.cli.title") }}</h3>
        <el-tag size="small" effect="plain" :type="tagType">{{ t(`plugins.cli.${status.state}`) }}</el-tag>
        <span v-if="unavailable" class="plugin-cli-card__text">{{ hint }}</span>
      </div>
      <div v-if="status.installable" class="plugin-development-panel__toolbar-actions">
        <el-button v-if="status.state === 'installed'" :loading="busy" @click="uninstall">
          {{ t("plugins.cli.uninstall") }}
        </el-button>
        <el-button v-else type="primary" :loading="busy" @click="install">
          {{ t("plugins.cli.install") }}
        </el-button>
      </div>
    </div>
    <template v-if="!unavailable">
      <p class="plugin-cli-card__text">{{ t("plugins.cli.description") }}</p>
      <p v-if="hint" class="plugin-cli-card__text">{{ hint }}</p>
      <el-alert
        v-if="status.pathHint"
        type="warning"
        :closable="false"
        show-icon
        :title="t('plugins.cli.pathHint', { dir: status.pathHint })"
      />
      <code class="plugin-cli-card__example">mincode plugin create my-plugin --id my-plugin</code>
    </template>
  </section>
</template>

<style scoped>
.plugin-cli-card__text {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.plugin-cli-card__example {
  justify-self: start;
  padding: 4px 8px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  font-family: var(--app-font-mono);
  font-size: 12px;
  user-select: text;
}
</style>
