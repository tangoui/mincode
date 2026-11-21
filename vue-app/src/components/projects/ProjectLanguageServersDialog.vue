<script setup lang="ts">
// Project "Language servers" dialog (extracted verbatim from ProjectWorkspaceView).
// Built-in servers are enabled explicitly per project; processes only start
// once a file of a matching language is opened. Plugin servers
// (contributes.languageServers) are enabled here too: enabling asks the user
// to confirm the resolved launch, and a language a built-in server covers
// moves to a plugin server only through the per-language "Use for" choice.
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import type { LspProjectSettings } from "@/domain/projects/lsp";
import { useVueI18n } from "@/i18n/messages";
import { getLspServerLogs, installLspServer, listLspServers, type LspServerInfo } from "@/services/lsp-service";
import { copyText, isDesktopRuntime } from "@/services/desktop-bridge";
import { isLspWatchLimited, lspRuntimeState } from "@/services/lsp-runtime-state";

const props = defineProps<{
  modelValue: boolean;
  projectRoot: string;
  lspSettings: LspProjectSettings;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  "update:lspSettings": [value: LspProjectSettings];
}>();

const { t } = useVueI18n();
const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});
const lspServers = ref<LspServerInfo[]>([]);
const lspLoading = ref(false);

interface LspInstallState {
  installing: boolean;
  output: string;
  failed: boolean;
  done: boolean;
}
const lspInstallStates = ref<Record<string, LspInstallState>>({});

watch(
  () => props.modelValue,
  (open) => {
    if (open) void refreshLspServers();
  },
  { immediate: true },
);

function updateEnabledServers(enabledServers: string[]) {
  emit("update:lspSettings", { ...props.lspSettings, enabledServers });
}

async function refreshLspServers() {
  lspLoading.value = true;
  try {
    lspServers.value = await listLspServers();
  } catch {
    lspServers.value = [];
  } finally {
    lspLoading.value = false;
  }
}

// 语言服务器由用户显式启用；实际进程只在打开匹配语言文件后启动。
function runtimeState(serverId: string) {
  return lspRuntimeState(props.projectRoot, serverId);
}

function runtimeFailureMessage(serverId: string) {
  const failure = runtimeState(serverId)?.failure;
  return failure ? t(`project.lsp.failure.${failure}`) : "";
}

// The project exceeds the file-watch limits: the server runs, but external
// file changes do not reach it.
function watchLimited(serverId: string) {
  return isLspWatchLimited(props.projectRoot, serverId) && runtimeState(serverId)?.phase === "ready";
}

// Logs and install output are long and hard to select by hand.
async function copyOutput(text: string) {
  try {
    await copyText(text);
    ElMessage.success(t("project.lsp.outputCopied"));
  } catch (error) {
    ElMessage.error(t("project.lsp.outputCopyFailed", { message: error instanceof Error ? error.message : String(error) }));
  }
}

function toggleLspServer(serverId: string) {
  const current = props.lspSettings.enabledServers;
  const next = current.includes(serverId)
    ? current.filter((id) => id !== serverId)
    : [...current, serverId];
  updateEnabledServers(next);
}

async function doInstallLspServer(serverId: string) {
  lspInstallStates.value[serverId] = { installing: true, output: "", failed: false, done: false };
  try {
    const result = await installLspServer(serverId);
    lspInstallStates.value[serverId] = {
      installing: false,
      output: result.output,
      failed: !!result.error,
      done: true,
    };
    if (!result.error) {
      // Auto-enable the newly installed server
      const current = props.lspSettings.enabledServers;
      if (!current.includes(serverId)) {
        updateEnabledServers([...current, serverId]);
      }
      await refreshLspServers();
    }
  } catch (err) {
    lspInstallStates.value[serverId] = {
      installing: false,
      output: String(err),
      failed: true,
      done: true,
    };
  }
}

// ---- Plugin language servers ----
interface LspLogsState {
  visible: boolean;
  loading: boolean;
  name: string;
  serverId: string;
  output: string;
  unavailable: boolean;
}
const lspLogs = ref<LspLogsState>({
  visible: false,
  loading: false,
  name: "",
  serverId: "",
  output: "",
  unavailable: false,
});

// Lifecycle/protocol traces and untrusted stderr, without protocol payloads.
let logsRequest = 0;
async function showLspLogs(server: Pick<LspServerInfo, "id" | "name">) {
  const request = ++logsRequest;
  const root = props.projectRoot;
  lspLogs.value = { visible: true, loading: true, name: server.name, serverId: server.id, output: "", unavailable: false };
  try {
    const result = await getLspServerLogs(root, server.id);
    if (request !== logsRequest || root !== props.projectRoot || !lspLogs.value.visible) return;
    lspLogs.value = { ...lspLogs.value, loading: false, output: result.output };
  } catch {
    if (request !== logsRequest || root !== props.projectRoot || !lspLogs.value.visible) return;
    lspLogs.value = { ...lspLogs.value, loading: false, unavailable: true };
  }
}
</script>

<template>
  <!-- Language Servers dialog -->
  <el-dialog
    append-to-body
    v-model="visible"
    :title="t('project.lsp.title')"
    width="620"
    :close-on-click-modal="true"
    class="project-lsp-dialog"
  >
    <el-scrollbar max-height="480px">
      <div v-if="!isDesktopRuntime()" class="project-lsp-notice">
        {{ t("project.lsp.desktopOnly") }}
      </div>
      <div v-else-if="!projectRoot" class="project-lsp-notice">
        {{ t("project.lsp.noProject") }}
      </div>
      <template v-else>
        <!-- 刷新时保留已有列表，只在首次加载（列表还是空的）时显示占位，
             否则整个对话框会在刷新的一瞬间塌成一行文字、看起来像闪了一下。 -->
        <div v-if="lspLoading && !lspServers.length" class="project-lsp-notice">{{ t("project.lsp.loading") }}</div>
        <div v-else-if="!lspServers.length" class="project-lsp-notice">{{ t("project.lsp.empty") }}</div>
        <ul v-else class="project-lsp-list" :aria-busy="lspLoading">
          <li v-for="server in lspServers" :key="server.id" class="project-lsp-item">
            <div class="project-lsp-item__header">
              <div class="project-lsp-item__info">
                <span class="project-lsp-item__name">{{ server.name }}</span>
                <span class="project-lsp-item__langs">{{ server.languages.join(", ") }}</span>
                <span v-if="server.heavy" class="project-lsp-item__heavy">{{ t("project.lsp.heavy") }}</span>
              </div>
              <div class="project-lsp-item__controls">
                <!-- 徽章和按钮必须读同一套状态：未安装 → 已启用未运行 → 运行中 → 未启用。
                     之前徽章只看 installed/running、按钮只看 enabledServers，
                     于是「装好但没启用」会同时显示「已启用」和「启用」。 -->
                <span
                  v-if="!server.installed"
                  class="project-lsp-item__badge project-lsp-item__badge--warn"
                >{{ t("project.lsp.notInstalled") }}</span>
                <span
                  v-else-if="!lspSettings.enabledServers.includes(server.id)"
                  class="project-lsp-item__badge"
                >{{ t("project.lsp.disabled") }}</span>
                <span
                  v-else-if="runtimeState(server.id)?.phase === 'failed'"
                  class="project-lsp-item__badge project-lsp-item__badge--warn"
                >{{ t("project.lsp.failed") }}</span>
                <span
                  v-else-if="runtimeState(server.id)?.phase === 'ready'"
                  class="project-lsp-item__badge project-lsp-item__badge--ok"
                >{{ t("project.lsp.ready") }}</span>
                <span v-else-if="runtimeState(server.id)?.phase === 'starting' || runtimeState(server.id)?.phase === 'initializing'" class="project-lsp-item__badge">{{ t("project.lsp.initializing") }}</span>
                <span v-else class="project-lsp-item__badge">{{ t("project.lsp.waiting") }}</span>
                <el-button
                  v-if="server.installed && lspSettings.enabledServers.includes(server.id)"
                  size="small"
                  plain
                  @click="showLspLogs(server)"
                >{{ t("project.lsp.logs") }}</el-button>
                <el-button
                  v-if="server.installed && lspSettings.enabledServers.includes(server.id)"
                  size="small"
                  type="danger"
                  plain
                  @click="toggleLspServer(server.id)"
                >{{ t("project.lsp.disable") }}</el-button>
                <el-button
                  v-else-if="server.installed"
                  size="small"
                  type="primary"
                  plain
                  @click="toggleLspServer(server.id)"
                >{{ t("project.lsp.enable") }}</el-button>
              </div>
            </div>
            <div v-if="!server.installed && !server.prerequisiteMissing" class="project-lsp-item__hint">
              <span>{{ t("project.lsp.installHint") }}</span>
              <code>{{ server.installHint }}</code>
              <el-button
                v-if="server.installable && !server.prerequisiteMissing"
                size="small"
                type="primary"
                plain
                :loading="lspInstallStates[server.id]?.installing"
                :disabled="lspInstallStates[server.id]?.installing"
                @click="doInstallLspServer(server.id)"
              >{{ t("project.lsp.install") }}</el-button>
            </div>
            <div v-if="server.prerequisiteMissing && server.prerequisiteHint" class="project-lsp-item__prereq">
              <span class="project-lsp-item__prereq-label">{{ t("project.lsp.prerequisiteRequired") }}</span>
              <span class="project-lsp-item__prereq-hint">{{ server.prerequisiteHint }}</span>
            </div>
            <div
              v-if="lspInstallStates[server.id]?.done"
              class="project-lsp-item__install-output"
              :class="{ 'project-lsp-item__install-output--error': lspInstallStates[server.id]?.failed }"
            >
              <div class="project-lsp-item__install-header">
                <span class="project-lsp-item__install-status">
                  {{ lspInstallStates[server.id]?.failed ? t("project.lsp.installFailed") : t("project.lsp.installDone") }}
                </span>
                <el-button
                  v-if="lspInstallStates[server.id]?.output"
                  size="small"
                  text
                  native-type="button"
                  @click="copyOutput(lspInstallStates[server.id]?.output ?? '')"
                >{{ t("common.copy") }}</el-button>
              </div>
              <el-scrollbar v-if="lspInstallStates[server.id]?.output" class="project-lsp-item__install-scroll" max-height="160px">
                <pre>{{ lspInstallStates[server.id]?.output }}</pre>
              </el-scrollbar>
            </div>
            <div v-if="watchLimited(server.id)" class="project-lsp-item__watch-limited" role="status">
              {{ t("project.lsp.watchLimited") }}
            </div>
            <div v-if="runtimeState(server.id)?.failure" class="project-lsp-item__error" role="status">
              {{ runtimeFailureMessage(server.id) }}
              <el-button v-if="!server.error && server.installable" size="small" plain :loading="lspInstallStates[server.id]?.installing" @click="doInstallLspServer(server.id)">{{ t("project.lsp.reinstall") }}</el-button>
            </div>
            <div v-if="server.error" class="project-lsp-item__error">
              {{ t("project.lsp.error", { msg: server.error }) }}
              <el-button v-if="server.installed && server.installable" size="small" plain :loading="lspInstallStates[server.id]?.installing" @click="doInstallLspServer(server.id)">{{ t("project.lsp.reinstall") }}</el-button>
            </div>
          </li>
        </ul>
      </template>
    </el-scrollbar>
    <template #footer>
      <el-button
        v-if="isDesktopRuntime()"
        type="primary"
        native-type="button"
        :loading="lspLoading"
        :disabled="lspLoading"
        @click="refreshLspServers"
      >
        {{ t("project.lsp.refresh") }}
      </el-button>
      <el-button native-type="button" @click="visible = false">
        {{ t("common.close") }}
      </el-button>
    </template>
  </el-dialog>

  <!-- 语言服务器日志对话框：嵌套在管理对话框之上 -->
  <el-dialog
    append-to-body
    v-model="lspLogs.visible"
    :title="t('project.lsp.logsTitle', { name: lspLogs.name })"
    width="640"
    class="project-lsp-logs-dialog"
  >
    <div v-if="runtimeState(lspLogs.serverId)?.failure" class="project-lsp-item__error" role="status">{{ runtimeFailureMessage(lspLogs.serverId) }}</div>
    <div v-if="watchLimited(lspLogs.serverId)" class="project-lsp-item__watch-limited" role="status">{{ t("project.lsp.watchLimited") }}</div>
    <el-scrollbar max-height="420px">
      <div v-if="lspLogs.loading" class="project-lsp-notice">{{ t("common.loading") }}</div>
      <div v-else-if="lspLogs.unavailable" class="project-lsp-notice">{{ t("project.lsp.logsUnavailable") }}</div>
      <div v-else-if="!lspLogs.output" class="project-lsp-notice">{{ t("project.lsp.logsEmpty") }}</div>
      <pre v-else class="project-lsp-logs-output">{{ lspLogs.output }}</pre>
    </el-scrollbar>
    <template #footer>
      <el-button
        native-type="button"
        :disabled="lspLogs.loading || !lspLogs.output"
        @click="copyOutput(lspLogs.output)"
      >{{ t("project.lsp.copyLogs") }}</el-button>
      <el-button native-type="button" :loading="lspLogs.loading" @click="showLspLogs({ id: lspLogs.serverId, name: lspLogs.name })">{{ t("project.lsp.refreshLogs") }}</el-button>
      <el-button native-type="button" @click="lspLogs.visible = false">{{ t("common.close") }}</el-button>
    </template>
  </el-dialog>
</template>
