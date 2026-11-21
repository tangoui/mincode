<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from "vue";
import { ArrowDown, ArrowRight, Folder, Key, Monitor, Plus, Refresh, Upload } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import ConnectionAvatar, { type ConnectionAvatarStatus } from "@/components/connections/ConnectionAvatar.vue";
import ConnectionRecentStrip, { type RecentStripItem } from "@/components/connections/ConnectionRecentStrip.vue";
import { isRepeatClick, useOverviewSearchKeyboard } from "@/composables/connections/useOverviewSearchKeyboard";
import { ROW_GROUP_ATTRIBUTE, useGroupedRowWindow } from "@/composables/use-grouped-row-window";
import { orderConnectionGroups } from "@/domain/connections/group-order";
import { parseSshAddress, pickRecent, relativeTime } from "@/domain/connections/overview";
import type { SshConnectionProfile, SshTerminalStatus } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import { usageTone } from "@/domain/ui/usage-tone";
import {
  loadSshServerSnapshot,
  type SshServerSnapshot,
} from "@/services/ssh-server-manager-service";

export interface SshOverviewSession {
  profileId: string;
  sessionId: string;
  tabId: string;
  status: SshTerminalStatus;
  statusDetail?: string;
}

interface ServerMetricState {
  snapshot?: SshServerSnapshot;
  cpuPercent: number;
  receiveBytesPerSecond: number;
  transmitBytesPerSecond: number;
  updatedAt: number;
  loading: boolean;
  error: string;
  previousCpuTotal: number;
  previousCpuIdle: number;
  previousNetTimestamp: number;
  previousNet: Record<string, { rxBytes: number; txBytes: number }>;
}

const REFRESH_INTERVAL_MS = 10_000;
const MAX_REFRESH_CONCURRENCY = 4;

const props = defineProps<{
  profiles: SshConnectionProfile[];
  sessions: SshOverviewSession[];
  /** Last time each host reached "ready" (see stores/ssh-recent). */
  openedAt: Record<string, number>;
  /** Hosts opened before this time are hidden from "Recently opened". */
  recentClearedAt: number;
  /** The sidebar's custom group order, so both lists group hosts the same way. */
  groupOrder: string[];
}>();

const emit = defineEmits<{
  connect: [profile: SshConnectionProfile];
  "open-session": [tabId: string];
  "new-connection": [];
  "new-connection-for-address": [address: string];
  "clear-recent": [];
  "file-transfer": [profile: SshConnectionProfile];
  import: [];
  "known-hosts": [];
}>();

const { t, intlLocale } = useVueI18n();
const hostsSection = ref<HTMLElement>();
const overviewScroller = ref<{ wrapRef?: HTMLElement }>();
const searchField = ref<{ focus: () => void }>();
const query = ref("");
const selectedGroup = ref("");
/** Collapsed table groups; local to this page and not persisted. */
const collapsedGroups = ref<Record<string, boolean>>({});
const refreshing = ref(false);
const now = ref(Date.now());
const metrics = reactive<Record<string, ServerMetricState>>({});
let refreshTimer: ReturnType<typeof setInterval> | undefined;

function createMetricState(): ServerMetricState {
  return {
    cpuPercent: 0,
    receiveBytesPerSecond: 0,
    transmitBytesPerSecond: 0,
    updatedAt: 0,
    loading: false,
    error: "",
    previousCpuTotal: 0,
    previousCpuIdle: 0,
    previousNetTimestamp: 0,
    previousNet: {},
  };
}

function metricState(profileId: string) {
  return (metrics[profileId] ??= createMetricState());
}

const sessionsByProfile = computed(() => {
  const result = new Map<string, SshOverviewSession>();
  for (const session of props.sessions) {
    const current = result.get(session.profileId);
    if (!current || (current.status !== "ready" && session.status === "ready")) {
      result.set(session.profileId, session);
    }
  }
  return result;
});

const groupOptions = computed(() =>
  [...new Set(props.profiles.map((profile) => profile.group?.trim()).filter(Boolean) as string[])].sort(
    (left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" })
  )
);

const filteredProfiles = computed(() => {
  const normalizedQuery = query.value.trim().toLocaleLowerCase();
  return props.profiles.filter((profile) => {
    const group = profile.group?.trim() || "";
    if (selectedGroup.value && group !== selectedGroup.value) return false;
    if (!normalizedQuery) return true;
    return [profile.name, profile.host, profile.username, group, ...profile.tags]
      .join(" ")
      .toLocaleLowerCase()
      .includes(normalizedQuery);
  });
});

const groupedProfiles = computed(() => {
  const groups = new Map<string, SshConnectionProfile[]>();
  for (const profile of filteredProfiles.value) {
    const group = profile.group?.trim() || "";
    groups.set(group, [...(groups.get(group) ?? []), profile]);
  }
  return orderConnectionGroups(
    [...groups.entries()].map(([key, items]) => ({ key, label: key, items })),
    props.groupOrder
  ).map((group) => [group.key, group.items] as const);
});

const onlineCount = computed(
  () => props.profiles.filter((profile) => sessionsByProfile.value.get(profile.id)?.status === "ready").length
);
const abnormalCount = computed(
  () =>
    props.profiles.filter((profile) => {
      const status = sessionsByProfile.value.get(profile.id)?.status;
      return status === "error" || status === "credentials" || status === "unavailable";
    }).length
);

function percent(used: number, total: number) {
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((used / total) * 100)));
}

function updateMetric(profileId: string, snapshot: SshServerSnapshot) {
  const state = metricState(profileId);
  const total = snapshot.cpuTotal ?? 0;
  const idle = snapshot.cpuIdle ?? 0;
  if (state.previousCpuTotal > 0 && total > state.previousCpuTotal) {
    const deltaTotal = total - state.previousCpuTotal;
    const deltaIdle = Math.max(0, idle - state.previousCpuIdle);
    state.cpuPercent = Math.max(0, Math.min(100, Math.round(((deltaTotal - deltaIdle) / deltaTotal) * 100)));
  } else if (snapshot.cpuCores > 0) {
    state.cpuPercent = Math.max(0, Math.min(100, Math.round((snapshot.load1 / snapshot.cpuCores) * 100)));
  }
  state.previousCpuTotal = total;
  state.previousCpuIdle = idle;

  const timestamp = snapshot.diskIOTimestampMs || Date.now();
  if (state.previousNetTimestamp > 0 && timestamp > state.previousNetTimestamp) {
    const elapsedSeconds = (timestamp - state.previousNetTimestamp) / 1000;
    let receive = 0;
    let transmit = 0;
    for (const item of snapshot.netIO ?? []) {
      const previous = state.previousNet[item.interface];
      if (!previous) continue;
      receive += Math.max(0, item.rxBytes - previous.rxBytes);
      transmit += Math.max(0, item.txBytes - previous.txBytes);
    }
    state.receiveBytesPerSecond = receive / elapsedSeconds;
    state.transmitBytesPerSecond = transmit / elapsedSeconds;
  }
  state.previousNet = Object.fromEntries(
    (snapshot.netIO ?? []).map((item) => [item.interface, { rxBytes: item.rxBytes, txBytes: item.txBytes }])
  );
  state.previousNetTimestamp = timestamp;
  state.snapshot = snapshot;
  state.updatedAt = Date.now();
  state.error = "";
}

async function refreshSession(session: SshOverviewSession) {
  if (session.status !== "ready") return;
  const state = metricState(session.profileId);
  state.loading = true;
  try {
    updateMetric(session.profileId, await loadSshServerSnapshot(session.sessionId));
  } catch (cause) {
    state.error = cause instanceof Error ? cause.message : String(cause);
  } finally {
    state.loading = false;
  }
}

async function refreshAll() {
  if (refreshing.value || document.hidden) return;
  const sessions = [...sessionsByProfile.value.values()].filter((session) => session.status === "ready");
  if (!sessions.length) return;
  refreshing.value = true;
  let nextIndex = 0;
  const worker = async () => {
    while (nextIndex < sessions.length) {
      const session = sessions[nextIndex++];
      if (session) await refreshSession(session);
    }
  };
  try {
    await Promise.all(
      Array.from({ length: Math.min(MAX_REFRESH_CONCURRENCY, sessions.length) }, () => worker())
    );
    now.value = Date.now();
  } finally {
    refreshing.value = false;
  }
}

function statusClass(profile: SshConnectionProfile) {
  const status = sessionsByProfile.value.get(profile.id)?.status;
  if (status === "ready") return "is-online";
  if (status === "connecting" || status === "reconnecting") return "is-pending";
  if (status) return "is-error";
  return "is-offline";
}

function avatarStatus(profile: SshConnectionProfile): ConnectionAvatarStatus {
  const state = statusClass(profile);
  if (state === "is-online") return "online";
  if (state === "is-pending") return "pending";
  return "offline";
}

function statusLabel(profile: SshConnectionProfile) {
  const status = sessionsByProfile.value.get(profile.id)?.status;
  if (!status) return t("ssh.overview.notConnected");
  return t(`ssh.status.${status}`);
}

function memoryPercent(profileId: string) {
  const snapshot = metrics[profileId]?.snapshot;
  return percent(snapshot?.memoryUsedBytes ?? 0, snapshot?.memoryTotalBytes ?? 0);
}

function diskPercent(profileId: string) {
  const snapshot = metrics[profileId]?.snapshot;
  return percent(snapshot?.diskUsedBytes ?? 0, snapshot?.diskTotalBytes ?? 0);
}

// Usage only gets a severity once a real sample exists; idle cards stay neutral.
function toneClass(profileId: string, value: number) {
  return `is-${usageTone(metrics[profileId]?.snapshot ? value : undefined)}`;
}

function groupLabel(group: string) {
  return group || t("ssh.manager.ungrouped");
}

function formatRate(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B/s";
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount >= 10 || unit === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
}

function updatedLabel(profileId: string) {
  const updatedAt = metrics[profileId]?.updatedAt ?? 0;
  if (!updatedAt) return t("ssh.overview.waitingForMetrics");
  const seconds = Math.max(0, Math.round((now.value - updatedAt) / 1000));
  if (seconds < 5) return t("ssh.overview.justNow");
  if (seconds < 60) return t("ssh.overview.secondsAgo").replace("{count}", String(seconds));
  return t("ssh.overview.minutesAgo").replace("{count}", String(Math.floor(seconds / 60)));
}

function summaryLabel() {
  const summary = t("ssh.overview.summary")
    .replace("{total}", String(props.profiles.length))
    .replace("{online}", String(onlineCount.value));
  if (!abnormalCount.value) return summary;
  return summary + t("ssh.overview.summaryAbnormal").replace("{abnormal}", String(abnormalCount.value));
}

function lastOpenedLabel(profile: SshConnectionProfile) {
  const openedAt = props.openedAt[profile.id];
  if (typeof openedAt !== "number" || !Number.isFinite(openedAt)) return t("ssh.overview.neverConnected");
  return relativeTime(openedAt, now.value, intlLocale.value);
}

function addressLabel(profile: SshConnectionProfile) {
  const address = profile.username ? `${profile.username}@${profile.host}` : profile.host;
  return profile.port && profile.port !== 22 ? `${address}:${profile.port}` : address;
}

/** A live session switches to its tab; otherwise the host gets connected. */
function activateProfile(profile: SshConnectionProfile) {
  const session = sessionsByProfile.value.get(profile.id);
  if (session) emit("open-session", session.tabId);
  else emit("connect", profile);
}

// ---- Recently opened -------------------------------------------------------

const recentItems = computed<RecentStripItem[]>(() =>
  pickRecent(props.profiles, (profile) => props.openedAt[profile.id], props.recentClearedAt).map((profile) => ({
    id: profile.id,
    name: profile.name,
    seed: profile.id,
    status: avatarStatus(profile),
    detail:
      sessionsByProfile.value.get(profile.id)?.status === "ready"
        ? t("ssh.overview.inUse")
        : [groupLabel(profile.group?.trim() || ""), lastOpenedLabel(profile)].join(" · "),
  }))
);

function openRecent(profileId: string) {
  const profile = props.profiles.find((item) => item.id === profileId);
  if (profile) activateProfile(profile);
}

// ---- In use ----------------------------------------------------------------

const liveProfiles = computed(() =>
  props.profiles.filter((profile) => sessionsByProfile.value.get(profile.id)?.status === "ready")
);

function connectedForLabel(profile: SshConnectionProfile) {
  const openedAt = props.openedAt[profile.id];
  if (typeof openedAt !== "number" || !Number.isFinite(openedAt)) return "";
  const minutes = Math.max(0, Math.floor((now.value - openedAt) / 60_000));
  return t("ssh.overview.connectedFor").replace("{count}", String(minutes));
}

function openLiveSession(profile: SshConnectionProfile) {
  const session = sessionsByProfile.value.get(profile.id);
  if (session) emit("open-session", session.tabId);
}

// ---- All hosts table and keyboard highlight ---------------------------------

function groupIsCollapsed(group: string) {
  return collapsedGroups.value[group] === true;
}

function toggleGroup(group: string) {
  const highlightedId = highlightedProfileId.value;
  collapsedGroups.value = { ...collapsedGroups.value, [group]: !groupIsCollapsed(group) };
  // Keep the same host highlighted when it is still visible; otherwise start from the top.
  const index = highlightedId ? rowIndexById.value.get(highlightedId) : undefined;
  if (index === undefined) resetHighlight();
  else highlightIndex.value = index;
}

/** Rows in the order they appear in the table; arrow keys walk this list. */
const visibleRows = computed(() =>
  groupedProfiles.value.flatMap(([group, servers]) => (groupIsCollapsed(group) ? [] : servers))
);

const rowIndexById = computed(() => new Map(visibleRows.value.map((profile, index) => [profile.id, index])));

/** `user@host[:port]` typed into the search box when no saved host matches it. */
const addressCandidate = computed(() =>
  !filteredProfiles.value.length && parseSshAddress(query.value) ? query.value : ""
);

const highlightCount = computed(() => visibleRows.value.length || (addressCandidate.value ? 1 : 0));

/** Enter on a row connects it; with no saved match, row 0 is the "new host" entry. */
function activateHighlighted(index: number) {
  const profile = visibleRows.value[index];
  if (profile) {
    activateProfile(profile);
    return;
  }
  if (addressCandidate.value && index === 0) emit("new-connection-for-address", addressCandidate.value);
}

// Many hosts render only the rows near the viewport (see use-grouped-row-window).
const hostRows = useGroupedRowWindow({
  scroller: () => overviewScroller.value?.wrapRef,
  counts: () => Object.fromEntries(groupedProfiles.value.map(([group, servers]) => [group, groupIsCollapsed(group) ? 0 : servers.length])),
  rowSelector: ".connection-overview__row",
  estimatedRowHeight: 40,
  fallbackRows: 12,
});

/** Scrolls the list so the `index`-th visible host row is rendered (keyboard highlight). */
function revealHostRow(index: number) {
  let offset = index;
  for (const [group, servers] of groupedProfiles.value) {
    if (groupIsCollapsed(group)) continue;
    if (offset < servers.length) {
      hostRows.scrollToRow(group, offset);
      return;
    }
    offset -= servers.length;
  }
}

// Also focuses the search box on mount/activation unless a dialog is open.
const { highlightIndex, resetHighlight, onSearchKeydown } = useOverviewSearchKeyboard({
  query,
  count: highlightCount,
  activate: activateHighlighted,
  searchField,
  scrollContainer: hostsSection,
  revealIndex: revealHostRow,
});

const highlightedProfileId = computed(() => visibleRows.value[highlightIndex.value]?.id);

function selectRow(profile: SshConnectionProfile) {
  highlightIndex.value = rowIndexById.value.get(profile.id) ?? -1;
}

function onRowAction(event: MouseEvent, profile: SshConnectionProfile) {
  if (isRepeatClick(event)) return;
  activateProfile(profile);
}

watch([query, selectedGroup], () => {
  // Searching shows every match, including those in collapsed groups.
  if (query.value.trim()) collapsedGroups.value = {};
  resetHighlight();
});

watch(
  () => props.profiles.map((profile) => profile.id),
  (profileIds) => {
    const retained = new Set(profileIds);
    for (const profileId of Object.keys(metrics)) {
      if (!retained.has(profileId)) delete metrics[profileId];
    }
  },
  { immediate: true }
);

watch(
  () => props.sessions.map((session) => `${session.sessionId}:${session.status}`).join("|"),
  () => void refreshAll(),
  { immediate: true }
);

function startRefreshTimer() {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    now.value = Date.now();
    void refreshAll();
  }, REFRESH_INTERVAL_MS);
}

function stopRefreshTimer() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = undefined;
}

onMounted(startRefreshTimer);
// The SSH workspace is cached by KeepAlive; stop probing servers while it is hidden.
onActivated(() => {
  if (refreshTimer) return;
  startRefreshTimer();
  now.value = Date.now();
  void refreshAll();
});
onDeactivated(stopRefreshTimer);
onBeforeUnmount(stopRefreshTimer);
</script>

<template>
  <section class="ssh-server-dashboard">
    <el-scrollbar ref="overviewScroller" class="ssh-server-overview__scroll">
      <header class="connection-overview__head">
        <h2 class="connection-overview__title">{{ t("ssh.overview.title") }}</h2>
        <span class="connection-overview__stat">{{ summaryLabel() }}</span>
        <div class="connection-overview__actions">
          <el-button :icon="Upload" @click="emit('import')">{{ t("common.import") }}</el-button>
          <el-button :icon="Key" :title="t('ssh.manager.knownHosts')" @click="emit('known-hosts')">
            {{ t("ssh.overview.knownHosts") }}
          </el-button>
          <el-button type="primary" :icon="Plus" @click="emit('new-connection')">{{ t("ssh.overview.newHost") }}</el-button>
        </div>
      </header>

      <div class="connection-overview__search">
        <AppSearchField
          ref="searchField"
          v-model="query"
          clearable
          :placeholder="t('ssh.overview.searchPlaceholder')"
          :aria-label="t('ssh.overview.searchPlaceholder')"
          @keydown="onSearchKeydown"
        />
      </div>

      <el-empty v-if="!profiles.length" :image-size="88" :description="t('ssh.manager.empty')">
        <el-button type="primary" @click="emit('new-connection')">{{ t("ssh.manager.addFirst") }}</el-button>
      </el-empty>

      <template v-else>
        <ConnectionRecentStrip
          v-if="!query.trim()"
          :items="recentItems"
          :title="t('ssh.overview.recent')"
          :clear-label="t('ssh.overview.clearRecent')"
          @open="openRecent"
          @clear="emit('clear-recent')"
        />

        <section v-if="liveProfiles.length" class="ssh-server-live" :aria-label="t('ssh.overview.inUse')">
          <div class="connection-overview__section">
            <h3 class="connection-overview__section-title">{{ t("ssh.overview.inUse") }}</h3>
            <span class="connection-overview__section-count">{{ liveProfiles.length }}</span>
          </div>
          <div class="ssh-server-live__grid">
            <article
              v-for="profile in liveProfiles"
              :key="profile.id"
              class="ssh-server-live__card"
              :title="metrics[profile.id]?.error || updatedLabel(profile.id)"
            >
              <header class="ssh-server-live__head">
                <ConnectionAvatar :name="profile.name" :seed="profile.id" status="online" :size="26" />
                <strong class="connection-overview__name">{{ profile.name }}</strong>
                <span class="connection-overview__mono">{{ addressLabel(profile) }}</span>
                <span class="ssh-server-live__since">{{ connectedForLabel(profile) }}</span>
              </header>
              <div class="ssh-server-live__bars">
                <div class="ssh-server-live__bar" :class="toneClass(profile.id, metrics[profile.id]?.cpuPercent ?? 0)">
                  <span>CPU</span>
                  <b>{{ metrics[profile.id]?.cpuPercent ?? 0 }}%</b>
                  <el-progress :percentage="metrics[profile.id]?.cpuPercent ?? 0" :show-text="false" :stroke-width="5" aria-label="CPU" />
                </div>
                <div class="ssh-server-live__bar" :class="toneClass(profile.id, memoryPercent(profile.id))">
                  <span>{{ t("ssh.overview.memory") }}</span>
                  <b>{{ memoryPercent(profile.id) }}%</b>
                  <el-progress
                    :percentage="memoryPercent(profile.id)"
                    :show-text="false"
                    :stroke-width="5"
                    :aria-label="t('ssh.overview.memory')"
                  />
                </div>
                <div class="ssh-server-live__bar" :class="toneClass(profile.id, diskPercent(profile.id))">
                  <span>{{ t("ssh.overview.disk") }}</span>
                  <b>{{ diskPercent(profile.id) }}%</b>
                  <el-progress
                    :percentage="diskPercent(profile.id)"
                    :show-text="false"
                    :stroke-width="5"
                    :aria-label="t('ssh.overview.disk')"
                  />
                </div>
              </div>
              <footer class="ssh-server-live__foot">
                <el-button link type="primary" :icon="Monitor" @click="openLiveSession(profile)">
                  {{ t("ssh.overview.switchToTerminal") }}
                </el-button>
                <el-button link type="primary" :icon="Folder" @click="emit('file-transfer', profile)">
                  {{ t("ssh.overview.sftp") }}
                </el-button>
                <span v-if="metrics[profile.id]?.error" class="ssh-server-live__note is-error">
                  {{ t("ssh.overview.refreshFailed") }}
                </span>
                <span v-else class="ssh-server-live__note">
                  <span class="connection-overview__sr-only">{{ t("ssh.overview.networkRate") }}</span>
                  ↓ {{ formatRate(metrics[profile.id]?.receiveBytesPerSecond ?? 0) }}
                  ↑ {{ formatRate(metrics[profile.id]?.transmitBytesPerSecond ?? 0) }}
                </span>
              </footer>
            </article>
          </div>
        </section>

        <section ref="hostsSection" class="ssh-server-hosts" :aria-label="t('ssh.overview.allHosts')">
          <div class="connection-overview__section">
            <h3 class="connection-overview__section-title">{{ t("ssh.overview.allHosts") }}</h3>
            <span class="connection-overview__section-count">{{ filteredProfiles.length }}</span>
            <div class="connection-overview__section-action ssh-server-overview__filters">
              <!-- "" is the "all groups" option; keep it a real value so its label shows. -->
              <el-select v-model="selectedGroup" :empty-values="[null, undefined]" size="small" :aria-label="t('ssh.overview.groupFilter')">
                <el-option value="" :label="t('ssh.overview.allGroups')" />
                <el-option v-for="group in groupOptions" :key="group" :value="group" :label="group" />
              </el-select>
              <el-button
                size="small"
                :icon="Refresh"
                :loading="refreshing"
                :aria-label="t('common.refresh')"
                :title="t('common.refresh')"
                @click="refreshAll"
              />
            </div>
          </div>

          <el-empty v-if="!filteredProfiles.length" :image-size="72" :description="t('ssh.manager.noMatch')">
            <el-button
              v-if="addressCandidate"
              type="primary"
              class="ssh-server-overview__new-for-address"
              :class="{ 'is-highlighted': highlightIndex === 0 }"
              :icon="Plus"
              @click="emit('new-connection-for-address', addressCandidate)"
            >
              {{ t("ssh.overview.newHostForAddress").replace("{address}", addressCandidate.trim()) }}
            </el-button>
          </el-empty>

          <div v-else class="connection-overview__table">
            <div
              v-for="[group, servers] in groupedProfiles"
              :key="group"
              role="group"
              class="ssh-server-hosts__group"
              :aria-label="groupLabel(group)"
            >
              <button
                type="button"
                class="connection-overview__group"
                :aria-expanded="!groupIsCollapsed(group)"
                @click="toggleGroup(group)"
              >
                <el-icon aria-hidden="true"><ArrowRight v-if="groupIsCollapsed(group)" /><ArrowDown v-else /></el-icon>
                <span>{{ groupLabel(group) }}</span>
                <span class="connection-overview__section-count">{{ servers.length }}</span>
              </button>
              <div v-if="!groupIsCollapsed(group)" role="list" v-bind="{ [ROW_GROUP_ATTRIBUTE]: group }">
                <div v-if="hostRows.windowOf(group, servers).paddingTop" aria-hidden="true" :style="{ height: `${hostRows.windowOf(group, servers).paddingTop}px` }" />
                <div
                  v-for="profile in hostRows.windowOf(group, servers).rows"
                  :key="profile.id"
                  role="listitem"
                  tabindex="0"
                  class="connection-overview__row is-clickable"
                  :class="{ 'is-highlighted': profile.id === highlightedProfileId }"
                  @click="selectRow(profile)"
                  @focus="selectRow(profile)"
                  @dblclick="activateProfile(profile)"
                  @keydown.enter.self.prevent="activateProfile(profile)"
                >
                  <span class="connection-overview__dot" :class="statusClass(profile)" :title="statusLabel(profile)">
                    <span class="connection-overview__sr-only">{{ statusLabel(profile) }}</span>
                  </span>
                  <span class="connection-overview__name" :title="profile.name">{{ profile.name }}</span>
                  <span class="connection-overview__mono" :title="addressLabel(profile)">{{ addressLabel(profile) }}</span>
                  <span class="connection-overview__row-optional">
                    <span v-if="profile.tags[0]" class="ssh-server-hosts__tag">{{ profile.tags[0] }}</span>
                    <span v-else class="connection-overview__muted">—</span>
                  </span>
                  <span class="connection-overview__muted connection-overview__row-optional">
                    {{ sessionsByProfile.get(profile.id)?.status === "ready" ? t("ssh.overview.inUse") : lastOpenedLabel(profile) }}
                  </span>
                  <el-button
                    link
                    type="primary"
                    class="ssh-server-hosts__action"
                    @click.stop="onRowAction($event, profile)"
                    @dblclick.stop
                  >
                    {{ sessionsByProfile.get(profile.id) ? t("ssh.overview.open") : t("ssh.overview.connect") }}
                  </el-button>
                </div>
                <div v-if="hostRows.windowOf(group, servers).paddingBottom" aria-hidden="true" :style="{ height: `${hostRows.windowOf(group, servers).paddingBottom}px` }" />
              </div>
            </div>
          </div>
        </section>
      </template>
    </el-scrollbar>
  </section>
</template>
