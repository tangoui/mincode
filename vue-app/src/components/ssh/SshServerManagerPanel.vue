<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Close, FullScreen, MagicStick, Refresh, RefreshRight, ScaleToOriginal, VideoPause, VideoPlay } from "@element-plus/icons-vue";
import SshDockerTerminal from "@/components/ssh/SshDockerTerminal.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n } from "@/i18n/messages";
import { sendToAi } from "@/services/ai-handoff";
import type { SshDockerTerminalMode } from "@/services/ssh-docker-service";
import {
  loadSshDockerContainers,
  loadSshDockerInfo,
  loadSshDockerLogs,
  loadSshServerLogs,
  loadSshServerProcesses,
  loadSshServerSnapshot,
  loadSshSystemdServices,
  runSshDockerAction,
  runSshSystemdAction,
  signalSshServerProcess,
  type SshDockerContainer,
  type SshDockerInfo,
  type SshServerDiskIO,
  type SshServerNetIO,
  type SshServerProcess,
  type SshServerSnapshot,
  type SshSystemdService,
} from "@/services/ssh-server-manager-service";

const props = defineProps<{
  modelValue: boolean;
  sessionId: string;
}>();

const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();
const activeTab = ref("overview");
const loading = ref(false);
const error = ref("");
const snapshot = ref<SshServerSnapshot>();
const logs = ref("");
const logUnit = ref("");
const loadedLogUnit = ref("");
const logLines = ref(200);
const processes = ref<SshServerProcess[]>([]);
const processQuery = ref("");
const services = ref<SshSystemdService[]>([]);
const serviceQuery = ref("");
const dockerInfo = ref<SshDockerInfo>();
const containers = ref<SshDockerContainer[]>([]);
const dockerQuery = ref("");
const dockerLogsVisible = ref(false);
const dockerLogsTitle = ref("");
const dockerLogs = ref("");
const dockerLogsLoaded = ref(false);
type DockerTerminalTab = {
  sessionId: string;
  container: SshDockerContainer;
  mode: SshDockerTerminalMode;
};
const dockerTerminalTabs = ref<DockerTerminalTab[]>([]);
const activeDockerTerminalId = ref<string | null>(null);
const isFullscreen = ref(false);
const dockerTerminalFullscreen = ref(false);
const diskIOSpeeds = ref<{ device: string; readBytesPerSec: number; writeBytesPerSec: number }[]>([]);
const cpuUsagePct = ref(0);
const netIOSpeeds = ref<{ interface: string; rxBytesPerSec: number; txBytesPerSec: number }[]>([]);
let prevDiskIORef: { io: SshServerDiskIO[]; tsMs: number } | null = null;
let prevNetIORef: { io: SshServerNetIO[]; tsMs: number } | null = null;
let prevCpuRef: { total: number; idle: number } | null = null;
let loadSequence = 0;
let dockerTerminalSequence = 0;
let dockerLogLoadSequence = 0;

const visibleProcesses = computed(() => {
  const query = processQuery.value.trim().toLocaleLowerCase();
  if (!query) return processes.value;
  return processes.value.filter((process) =>
    `${process.pid} ${process.ppid} ${process.user} ${process.state} ${process.command}`
      .toLocaleLowerCase()
      .includes(query)
  );
});

const visibleServices = computed(() => {
  const query = serviceQuery.value.trim().toLocaleLowerCase();
  if (!query) return services.value;
  return services.value.filter((service) =>
    `${service.unit} ${service.description} ${service.active} ${service.sub}`
      .toLocaleLowerCase()
      .includes(query)
  );
});

const visibleContainers = computed(() => {
  const query = dockerQuery.value.trim().toLocaleLowerCase();
  if (!query) return containers.value;
  return containers.value.filter((container) =>
    `${container.id} ${container.names} ${container.image} ${container.state} ${container.status} ${container.ports} ${container.created}`
      .toLocaleLowerCase()
      .includes(query)
  );
});

function containerName(container: SshDockerContainer) {
  return container.names || container.id.slice(0, 12);
}

function updateDiskIOSpeeds(snap: SshServerSnapshot) {
  const currentIO = snap.diskIO ?? [];
  const tsMs = snap.diskIOTimestampMs ?? 0;
  if (prevDiskIORef && currentIO.length > 0 && tsMs > 0) {
    const dtSec = (tsMs - prevDiskIORef.tsMs) / 1000;
    if (dtSec > 0.5) {
      const prevMap = new Map(prevDiskIORef.io.map((d) => [d.device, d]));
      diskIOSpeeds.value = currentIO.map((d) => {
        const prev = prevMap.get(d.device);
        if (!prev) return { device: d.device, readBytesPerSec: 0, writeBytesPerSec: 0 };
        return {
          device: d.device,
          readBytesPerSec: Math.max(0, (d.readBytes - prev.readBytes) / dtSec),
          writeBytesPerSec: Math.max(0, (d.writeBytes - prev.writeBytes) / dtSec),
        };
      });
    }
  } else {
    diskIOSpeeds.value = currentIO.map((d) => ({ device: d.device, readBytesPerSec: 0, writeBytesPerSec: 0 }));
  }
  if (currentIO.length > 0 && tsMs > 0) prevDiskIORef = { io: currentIO, tsMs };
}

function updateCpuUsage(snap: SshServerSnapshot) {
  const total = snap.cpuTotal ?? 0;
  const idle = snap.cpuIdle ?? 0;
  if (prevCpuRef && total > prevCpuRef.total) {
    const deltaTotal = total - prevCpuRef.total;
    const deltaIdle = idle - prevCpuRef.idle;
    cpuUsagePct.value = Math.max(0, Math.min(100, Math.round(((deltaTotal - deltaIdle) / deltaTotal) * 100)));
  } else {
    cpuUsagePct.value = 0;
  }
  if (total > 0) prevCpuRef = { total, idle };
}

function updateNetIOSpeeds(snap: SshServerSnapshot) {
  const currentIO = snap.netIO ?? [];
  const tsMs = snap.diskIOTimestampMs ?? 0;
  if (prevNetIORef && currentIO.length > 0 && tsMs > prevNetIORef.tsMs) {
    const dtSec = (tsMs - prevNetIORef.tsMs) / 1000;
    if (dtSec > 0.5) {
      const prevMap = new Map(prevNetIORef.io.map((d) => [d.interface, d]));
      netIOSpeeds.value = currentIO.map((d) => {
        const prev = prevMap.get(d.interface);
        if (!prev) return { interface: d.interface, rxBytesPerSec: 0, txBytesPerSec: 0 };
        return {
          interface: d.interface,
          rxBytesPerSec: Math.max(0, (d.rxBytes - prev.rxBytes) / dtSec),
          txBytesPerSec: Math.max(0, (d.txBytes - prev.txBytes) / dtSec),
        };
      });
    }
  } else {
    netIOSpeeds.value = currentIO.map((d) => ({ interface: d.interface, rxBytesPerSec: 0, txBytesPerSec: 0 }));
  }
  if (currentIO.length > 0 && tsMs > 0) prevNetIORef = { io: currentIO, tsMs };
}

function createDockerTerminalSessionId(mode: SshDockerTerminalMode) {
  dockerTerminalSequence += 1;
  const suffix = typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${Date.now()}-${dockerTerminalSequence}`;
  return `ssh-docker-${mode}-${suffix}`;
}

function openDockerTerminal(container: SshDockerContainer, mode: SshDockerTerminalMode) {
  const sessionId = createDockerTerminalSessionId(mode);
  dockerTerminalTabs.value.push({ sessionId, container, mode });
  activeDockerTerminalId.value = sessionId;
}

function closeDockerTerminal(sessionId?: string) {
  if (sessionId === undefined) {
    dockerTerminalTabs.value = [];
    activeDockerTerminalId.value = null;
    dockerTerminalFullscreen.value = false;
    return;
  }
  const idx = dockerTerminalTabs.value.findIndex((t) => t.sessionId === sessionId);
  if (idx === -1) return;
  dockerTerminalTabs.value.splice(idx, 1);
  if (activeDockerTerminalId.value === sessionId) {
    const next = dockerTerminalTabs.value[idx] ?? dockerTerminalTabs.value[idx - 1];
    activeDockerTerminalId.value = next?.sessionId ?? null;
  }
  // 终端区整体消失后覆盖层就没了，残留的全屏标记会让下次打开直接全屏。
  if (dockerTerminalTabs.value.length === 0) dockerTerminalFullscreen.value = false;
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const amount = value / 1024 ** index;
  return `${amount >= 100 || index === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[index]}`;
}

function percentage(used: number, total: number) {
  return total > 0 ? Math.max(0, Math.min(100, Math.round((used / total) * 100))) : 0;
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return t("ssh.server.uptimeValue")
    .replace("{days}", String(days))
    .replace("{hours}", String(hours))
    .replace("{minutes}", String(minutes));
}

async function loadCurrentTab() {
  const requestId = ++loadSequence;
  const tab = activeTab.value;
  const sessionId = props.sessionId;
  loading.value = true;
  error.value = "";
  try {
    if (tab === "overview") {
      const result = await loadSshServerSnapshot(sessionId);
      if (requestId === loadSequence) {
        snapshot.value = result;
        updateDiskIOSpeeds(result);
        updateCpuUsage(result);
        updateNetIOSpeeds(result);
      }
    } else if (tab === "logs") {
      const unit = logUnit.value;
      const result = await loadSshServerLogs(sessionId, unit, logLines.value);
      if (requestId === loadSequence) {
        logs.value = result;
        loadedLogUnit.value = unit;
      }
    } else if (tab === "processes") {
      const result = await loadSshServerProcesses(sessionId, 500);
      if (requestId === loadSequence) processes.value = result;
    } else if (tab === "services") {
      const result = await loadSshSystemdServices(sessionId);
      if (requestId === loadSequence) services.value = result;
    } else {
      const info = await loadSshDockerInfo(sessionId);
      if (requestId !== loadSequence) return;
      dockerInfo.value = info;
      const result = info.available ? await loadSshDockerContainers(sessionId) : [];
      if (requestId === loadSequence) containers.value = result;
    }
  } catch (reason) {
    if (requestId === loadSequence) error.value = String(reason);
  } finally {
    if (requestId === loadSequence) loading.value = false;
  }
}

function actionLabel(action: string) {
  if (action === "start") return t("ssh.server.action.start");
  if (action === "stop") return t("ssh.server.action.stop");
  if (action === "restart") return t("ssh.server.action.restart");
  if (action === "remove") return t("ssh.server.action.remove");
  return action;
}

async function confirmAction(message: string) {
  try {
    await ElMessageBox.confirm(message, t("common.confirm"), {
      type: "warning",
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    return true;
  } catch {
    return false;
  }
}

async function signalProcess(process: SshServerProcess, signal: "TERM" | "KILL") {
  if (
    !(await confirmAction(
      t("ssh.server.confirmSignal")
        .replace("{signal}", signal)
        .replace("{pid}", String(process.pid))
    ))
  ) {
    return;
  }
  try {
    await signalSshServerProcess(props.sessionId, process.pid, signal);
    ElMessage.success(t("ssh.server.actionComplete"));
    await loadCurrentTab();
  } catch (reason) {
    ElMessage.error(String(reason));
  }
}

async function serviceAction(service: SshSystemdService, action: string) {
  if (
    (action === "stop" || action === "restart") &&
    !(await confirmAction(
      t("ssh.server.confirmServiceAction")
        .replace("{action}", actionLabel(action))
        .replace("{name}", service.unit)
    ))
  ) {
    return;
  }
  try {
    await runSshSystemdAction(props.sessionId, service.unit, action);
    ElMessage.success(t("ssh.server.actionComplete"));
    await loadCurrentTab();
  } catch (reason) {
    ElMessage.error(String(reason));
  }
}

async function dockerAction(container: SshDockerContainer, action: string) {
  if (
    ["stop", "restart", "remove"].includes(action) &&
    !(await confirmAction(
      t("ssh.server.confirmDockerAction")
        .replace("{action}", actionLabel(action))
        .replace("{name}", container.names || container.id.slice(0, 12))
    ))
  ) {
    return;
  }
  try {
    if (["stop", "restart", "remove"].includes(action)) {
      dockerTerminalTabs.value
        .filter((t) => t.container.id === container.id)
        .forEach((t) => closeDockerTerminal(t.sessionId));
    }
    await runSshDockerAction(props.sessionId, container.id, action);
    ElMessage.success(t("ssh.server.actionComplete"));
    await loadCurrentTab();
  } catch (reason) {
    ElMessage.error(String(reason));
  }
}

async function showDockerLogs(container: SshDockerContainer) {
  const requestId = ++dockerLogLoadSequence;
  dockerLogsTitle.value = `${t("ssh.server.dockerLogs")} · ${containerName(container)}`;
  dockerLogsVisible.value = true;
  dockerLogsLoaded.value = false;
  dockerLogs.value = t("common.loading");
  try {
    const result = await loadSshDockerLogs(props.sessionId, container.id, 300);
    if (requestId !== dockerLogLoadSequence) return;
    dockerLogs.value = result;
    dockerLogsLoaded.value = true;
  } catch (reason) {
    if (requestId === dockerLogLoadSequence) dockerLogs.value = String(reason);
  }
}

function handoffLogs(source: "ssh" | "docker") {
  const docker = source === "docker";
  if (docker ? !dockerLogsLoaded.value : loading.value || error.value) return;
  const content = docker ? dockerLogs.value : logs.value;
  if (!content.trim()) return;
  const title = t("ai.handoff.serviceLogs");
  void sendToAi({
    source,
    title: docker ? dockerLogsTitle.value : [title, loadedLogUnit.value.trim()].filter(Boolean).join(" · "),
    content,
    prompt: t("ai.handoff.logsPrompt"),
  });
  dockerLogsVisible.value = false;
  emit("update:modelValue", false);
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) {
      void loadCurrentTab();
      return;
    }
    isFullscreen.value = false;
    dockerTerminalFullscreen.value = false;
    closeDockerTerminal();
    loadSequence += 1;
    loading.value = false;
    prevDiskIORef = null;
    prevNetIORef = null;
    prevCpuRef = null;
    diskIOSpeeds.value = [];
    netIOSpeeds.value = [];
    cpuUsagePct.value = 0;
  },
  { immediate: true }
);
watch(
  () => props.sessionId,
  () => {
    logs.value = "";
    dockerLogLoadSequence += 1;
    dockerLogsVisible.value = false;
    closeDockerTerminal();
    if (props.modelValue) void loadCurrentTab();
  }
);
watch(activeTab, (tab) => {
  if (tab !== "docker") closeDockerTerminal();
  if (props.modelValue) void loadCurrentTab();
});
</script>

<template>
  <el-drawer
    append-to-body
    :model-value="modelValue"
    class="ssh-server-manager"
    :class="{ 'is-fullscreen': isFullscreen }"
    direction="rtl"
    :size="isFullscreen ? 'calc(100% - var(--app-activity-rail-width))' : 'min(820px, 78vw)'"
    :with-header="false"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="ssh-server-manager__toolbar">
      <div class="ssh-server-manager__heading">
        <strong>{{ t("ssh.server.title") }}</strong>
        <span>{{ t("ssh.server.description") }}</span>
      </div>
      <div class="ssh-server-manager__toolbar-actions">
        <el-button
          :aria-label="isFullscreen ? t('ssh.server.exitFullscreen') : t('ssh.server.enterFullscreen')"
          :title="isFullscreen ? t('ssh.server.exitFullscreen') : t('ssh.server.enterFullscreen')"
          @click="isFullscreen = !isFullscreen"
        >
          <el-icon><ScaleToOriginal v-if="isFullscreen" /><FullScreen v-else /></el-icon>
          {{ isFullscreen ? t("ssh.server.exitFullscreen") : t("ssh.server.enterFullscreen") }}
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="loadCurrentTab">
          {{ t("common.refresh") }}
        </el-button>
        <el-button
          :icon="Close"
          :aria-label="t('common.close')"
          :title="t('common.close')"
          @click="emit('update:modelValue', false)"
        />
      </div>
    </div>
    <el-alert v-if="error" type="error" :title="error" show-icon :closable="false" />
    <el-tabs v-model="activeTab" class="ssh-server-manager__tabs">
      <el-tab-pane :label="t('ssh.server.overview')" name="overview" class="ssh-server-manager__tab-pane">
        <el-scrollbar v-if="snapshot" class="ssh-server-overview-scrollbar">
          <div class="ssh-server-overview">
            <el-descriptions :column="2" border size="small">
              <el-descriptions-item :label="t('ssh.server.hostname')">{{ snapshot.hostname }}</el-descriptions-item>
              <el-descriptions-item :label="t('ssh.server.os')">{{ snapshot.operatingSystem }}</el-descriptions-item>
              <el-descriptions-item :label="t('ssh.server.kernel')">{{ snapshot.kernel }}</el-descriptions-item>
              <el-descriptions-item :label="t('ssh.server.architecture')">{{ snapshot.architecture }}</el-descriptions-item>
              <el-descriptions-item :label="t('ssh.server.uptime')">{{ formatUptime(snapshot.uptimeSeconds) }}</el-descriptions-item>
              <el-descriptions-item :label="t('ssh.server.load')">{{ snapshot.load1.toFixed(2) }} / {{ snapshot.load5.toFixed(2) }} / {{ snapshot.load15.toFixed(2) }}</el-descriptions-item>
            </el-descriptions>
            <div class="ssh-server-overview__meters">
              <section>
                <strong>{{ t("ssh.server.memory") }}</strong>
                <span>{{ formatBytes(snapshot.memoryUsedBytes) }} / {{ formatBytes(snapshot.memoryTotalBytes) }}</span>
                <el-progress :percentage="percentage(snapshot.memoryUsedBytes, snapshot.memoryTotalBytes)" />
              </section>
              <section>
                <strong>{{ t("ssh.server.disk") }}</strong>
                <span>{{ formatBytes(snapshot.diskUsedBytes) }} / {{ formatBytes(snapshot.diskTotalBytes) }}</span>
                <el-progress :percentage="percentage(snapshot.diskUsedBytes, snapshot.diskTotalBytes)" />
              </section>
              <section>
                <strong>{{ t("ssh.server.cpu") }}</strong>
                <span>{{ cpuUsagePct }}% · {{ snapshot.cpuCores }} {{ t("ssh.server.cpuCores") }}</span>
                <el-progress :percentage="cpuUsagePct" />
              </section>
            </div>
            <div v-if="netIOSpeeds.length" class="ssh-server-disk-io">
              <strong>{{ t("ssh.server.netIO") }}</strong>
              <div class="ssh-server-disk-io__cards">
                <div v-for="io in netIOSpeeds" :key="io.interface" class="ssh-server-disk-io__card">
                  <div class="ssh-server-disk-io__device">{{ io.interface }}</div>
                  <div class="ssh-server-disk-io__row">
                    <span>{{ t("ssh.server.netRx") }}</span>
                    <strong>{{ formatBytes(io.rxBytesPerSec) }}/s</strong>
                  </div>
                  <div class="ssh-server-disk-io__row">
                    <span>{{ t("ssh.server.netTx") }}</span>
                    <strong>{{ formatBytes(io.txBytesPerSec) }}/s</strong>
                  </div>
                </div>
              </div>
            </div>
            <div v-if="diskIOSpeeds.length" class="ssh-server-disk-io">
              <strong>{{ t("ssh.server.diskIO") }}</strong>
              <div class="ssh-server-disk-io__cards">
                <div v-for="io in diskIOSpeeds" :key="io.device" class="ssh-server-disk-io__card">
                  <div class="ssh-server-disk-io__device">{{ io.device }}</div>
                  <div class="ssh-server-disk-io__row">
                    <span>{{ t("ssh.server.diskReadSpeed") }}</span>
                    <strong>{{ formatBytes(io.readBytesPerSec) }}/s</strong>
                  </div>
                  <div class="ssh-server-disk-io__row">
                    <span>{{ t("ssh.server.diskWriteSpeed") }}</span>
                    <strong>{{ formatBytes(io.writeBytesPerSec) }}/s</strong>
                  </div>
                </div>
              </div>
            </div>
            <div v-if="snapshot.disks?.length" class="ssh-server-disk-mounts">
              <strong>{{ t("ssh.server.diskMounts") }}</strong>
              <el-table :data="snapshot.disks" size="small" stripe>
                <el-table-column prop="mount" :label="t('ssh.server.diskMount')" min-width="120" show-overflow-tooltip />
                <el-table-column :label="t('ssh.server.diskSize')" width="90" align="right">
                  <template #default="{ row }">{{ formatBytes(row.totalBytes) }}</template>
                </el-table-column>
                <el-table-column :label="t('ssh.server.diskAvail')" width="90" align="right">
                  <template #default="{ row }">{{ formatBytes(row.availBytes) }}</template>
                </el-table-column>
                <el-table-column :label="t('ssh.server.diskUsedPct')" width="130">
                  <template #default="{ row }">
                    <div class="ssh-server-disk-mounts__usage">
                      <el-progress
                        :percentage="percentage(row.usedBytes, row.totalBytes)"
                        :stroke-width="4"
                        :show-text="false"
                        style="flex: 1"
                      />
                      <span>{{ percentage(row.usedBytes, row.totalBytes) }}%</span>
                    </div>
                  </template>
                </el-table-column>
              </el-table>
            </div>
          </div>
        </el-scrollbar>
        <el-empty v-else-if="!loading && !error" :description="t('ssh.server.noData')" />
      </el-tab-pane>
      <el-tab-pane :label="t('ssh.server.logs')" name="logs" class="ssh-server-manager__tab-pane">
        <div class="ssh-server-manager__filters">
          <el-input v-model="logUnit" clearable :placeholder="t('ssh.server.logUnitPlaceholder')" @keyup.enter="loadCurrentTab" />
          <el-input-number v-model="logLines" :min="50" :max="2000" :step="50" />
          <el-button type="primary" :loading="loading" @click="loadCurrentTab">{{ t("common.refresh") }}</el-button>
          <el-button text :icon="MagicStick" :disabled="loading || !!error || !logs.trim()" @click="handoffLogs('ssh')">
            {{ t("ai.handoff.analyzeLogs") }}
          </el-button>
        </div>
        <el-scrollbar class="ssh-server-log-viewer" always>
          <pre class="ssh-server-log-viewer__content">{{ logs || t("ssh.server.noLogs") }}</pre>
        </el-scrollbar>
      </el-tab-pane>
      <el-tab-pane :label="t('ssh.server.processes')" name="processes" class="ssh-server-manager__tab-pane">
        <div class="ssh-server-manager__filters">
          <AppSearchField
            v-model="processQuery"
            clearable
            :placeholder="t('ssh.server.processSearch')"
            :aria-label="t('ssh.server.processSearch')"
          />
        </div>
        <el-table
          :data="visibleProcesses"
          height="100%"
          size="small"
          stripe
          :empty-text="processQuery.trim() ? t('ssh.server.noSearchResults') : t('ssh.server.noData')"
        >
          <el-table-column prop="pid" label="PID" width="72" />
          <el-table-column prop="user" :label="t('ssh.server.user')" width="100" show-overflow-tooltip />
          <el-table-column prop="cpu" label="CPU %" width="80" sortable />
          <el-table-column prop="memory" label="MEM %" width="80" sortable />
          <el-table-column prop="state" :label="t('ssh.server.state')" width="72" />
          <el-table-column prop="command" :label="t('ssh.server.command')" min-width="180" show-overflow-tooltip />
          <el-table-column :label="t('common.actions')" width="142" fixed="right">
            <template #default="{ row }">
              <el-button text size="small" @click="signalProcess(row, 'TERM')">TERM</el-button>
              <el-button text size="small" type="danger" @click="signalProcess(row, 'KILL')">KILL</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
      <el-tab-pane :label="t('ssh.server.services')" name="services" class="ssh-server-manager__tab-pane">
        <div class="ssh-server-manager__filters">
          <AppSearchField
            v-model="serviceQuery"
            clearable
            :placeholder="t('ssh.server.serviceSearch')"
            :aria-label="t('ssh.server.serviceSearch')"
          />
        </div>
        <el-table :data="visibleServices" height="100%" size="small" stripe :empty-text="t('ssh.server.noData')">
          <el-table-column prop="unit" :label="t('ssh.server.service')" min-width="190" show-overflow-tooltip />
          <el-table-column prop="active" :label="t('ssh.server.active')" width="82" />
          <el-table-column prop="sub" :label="t('ssh.server.state')" width="92" />
          <el-table-column prop="description" :label="t('common.description')" min-width="180" show-overflow-tooltip />
          <el-table-column :label="t('common.actions')" width="210" fixed="right">
            <template #default="{ row }">
              <el-button text size="small" :icon="VideoPlay" @click="serviceAction(row, 'start')">{{ t("ssh.server.action.start") }}</el-button>
              <el-button text size="small" :icon="VideoPause" @click="serviceAction(row, 'stop')">{{ t("ssh.server.action.stop") }}</el-button>
              <el-button text size="small" :icon="RefreshRight" @click="serviceAction(row, 'restart')">{{ t("ssh.server.action.restart") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
      <el-tab-pane label="Docker" name="docker" class="ssh-server-manager__tab-pane">
        <el-alert
          v-if="dockerInfo && !dockerInfo.available"
          type="warning"
          :title="t('ssh.server.dockerUnavailable')"
          :description="dockerInfo.error"
          show-icon
          :closable="false"
        />
        <template v-else-if="dockerInfo?.available">
          <div class="ssh-server-docker-version">
            {{ t("ssh.server.dockerVersion") }}: {{ dockerInfo.clientVersion }} / {{ dockerInfo.serverVersion }}
          </div>
          <div class="ssh-server-manager__filters">
            <AppSearchField
              v-model="dockerQuery"
              clearable
              :placeholder="t('ssh.server.dockerSearch')"
              :aria-label="t('ssh.server.dockerSearch')"
            />
          </div>
          <div
            class="ssh-server-docker-content"
            :class="{ 'has-terminal': dockerTerminalTabs.length > 0 }"
          >
            <el-table
              :data="visibleContainers"
              height="100%"
              size="small"
              stripe
              :empty-text="dockerQuery.trim() ? t('ssh.server.noSearchResults') : t('ssh.server.noData')"
            >
              <el-table-column prop="names" :label="t('ssh.server.container')" min-width="120" show-overflow-tooltip />
              <el-table-column prop="image" :label="t('ssh.server.image')" min-width="140" show-overflow-tooltip />
              <el-table-column prop="state" :label="t('ssh.server.state')" width="85" />
              <el-table-column prop="status" :label="t('ssh.server.status')" min-width="150" show-overflow-tooltip />
              <el-table-column :label="t('common.actions')" width="360" fixed="right">
                <template #default="{ row }">
                  <div class="ssh-server-docker-actions">
                    <el-button
                      text
                      size="small"
                      :disabled="row.state !== 'running'"
                      @click="openDockerTerminal(row, 'exec')"
                    >
                      {{ t("ssh.server.dockerExec") }}
                    </el-button>
                    <el-button text size="small" @click="openDockerTerminal(row, 'logs')">
                      {{ t("ssh.server.dockerLiveLogs") }}
                    </el-button>
                    <el-button
                      v-if="row.state !== 'running'"
                      text
                      size="small"
                      :icon="VideoPlay"
                      @click="dockerAction(row, 'start')"
                    >
                      {{ t("ssh.server.action.start") }}
                    </el-button>
                    <el-button
                      v-else
                      text
                      size="small"
                      :icon="VideoPause"
                      @click="dockerAction(row, 'stop')"
                    >
                      {{ t("ssh.server.action.stop") }}
                    </el-button>
                    <el-button text size="small" @click="showDockerLogs(row)">
                      {{ t("ssh.server.dockerLogs") }}
                    </el-button>
                    <el-dropdown trigger="click" @command="dockerAction(row, $event)">
                      <el-button text size="small">{{ t("common.more") }}</el-button>
                      <template #dropdown>
                        <el-dropdown-menu>
                          <el-dropdown-item command="restart">
                            {{ t("ssh.server.action.restart") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="remove" divided>
                            {{ t("ssh.server.action.remove") }}
                          </el-dropdown-item>
                        </el-dropdown-menu>
                      </template>
                    </el-dropdown>
                  </div>
                </template>
              </el-table-column>
            </el-table>

            <section v-if="dockerTerminalTabs.length > 0" class="ssh-server-docker-terminal-pane" :class="{ 'is-fullscreen': dockerTerminalFullscreen }">
              <div class="ssh-docker-tab-bar">
                <div class="ssh-docker-tab-bar__scroll">
                  <div
                    v-for="tab in dockerTerminalTabs"
                    :key="tab.sessionId"
                    class="ssh-docker-tab"
                    :class="{ 'is-active': tab.sessionId === activeDockerTerminalId }"
                  >
                    <button
                      type="button"
                      class="ssh-docker-tab__select"
                      @click="activeDockerTerminalId = tab.sessionId"
                    >
                      <span class="ssh-docker-tab__label">
                        {{ containerName(tab.container) }}<small> · {{ tab.mode === "exec" ? t("ssh.server.dockerExec") : t("ssh.server.dockerLiveLogs") }}</small>
                      </span>
                    </button>
                    <button
                      type="button"
                      class="ssh-docker-tab__close"
                      :aria-label="t('common.close')"
                      @click="closeDockerTerminal(tab.sessionId)"
                    ><el-icon><Close /></el-icon></button>
                  </div>
                </div>
                <div class="ssh-docker-tab-bar__actions">
                  <el-button
                    text
                    size="small"
                    :icon="dockerTerminalFullscreen ? ScaleToOriginal : FullScreen"
                    :aria-label="dockerTerminalFullscreen ? t('ssh.server.terminalExitFullscreen') : t('ssh.server.terminalFullscreen')"
                    @click="dockerTerminalFullscreen = !dockerTerminalFullscreen"
                  />
                </div>
              </div>
              <SshDockerTerminal
                v-for="tab in dockerTerminalTabs"
                :key="tab.sessionId"
                v-show="tab.sessionId === activeDockerTerminalId"
                :parent-session-id="sessionId"
                :session-id="tab.sessionId"
                :container-id="tab.container.id"
                :mode="tab.mode"
                @failed="ElMessage.error($event)"
                @ended="closeDockerTerminal(tab.sessionId)"
              />
            </section>
          </div>
        </template>
      </el-tab-pane>
    </el-tabs>
  </el-drawer>
  <el-dialog v-model="dockerLogsVisible" :title="dockerLogsTitle" width="min(840px, 82vw)" append-to-body>
    <el-scrollbar class="ssh-server-log-viewer ssh-server-log-viewer--dialog" always>
      <pre class="ssh-server-log-viewer__content">{{ dockerLogs }}</pre>
    </el-scrollbar>
    <template #footer>
      <el-button text :icon="MagicStick" :disabled="!dockerLogsLoaded || !dockerLogs.trim()" @click="handoffLogs('docker')">
        {{ t("ai.handoff.analyzeLogs") }}
      </el-button>
    </template>
  </el-dialog>
</template>
