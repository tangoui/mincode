<script setup lang="ts">
/**
 * Plugin session sources on the sessions page (contributes.sessionSources).
 * A source is collapsed by default and lists its sessions only when the user
 * expands it; a page-wide refresh (refreshToken) reloads only the expanded
 * ones. Collapsing, leaving the page or the source going away cancels the
 * host request; lists live in memory only and are dropped when the page is
 * left (expanded sources load again when it is shown). Plugin sessions are read only: rows have no terminal,
 * rename, delete or status actions; clicking one opens its transcript.
 */
import "@/styles/workspaces/sessions/plugin-sources.css";
import { computed, onActivated, onBeforeUnmount, onDeactivated, reactive, useId, watch, type Component } from "vue";
import { ChatLineSquare, Cpu, Document, MagicStick, Monitor, Refresh } from "@element-plus/icons-vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import type { PluginSessionSource, PluginSessionSummary, SessionSourceIcon } from "@/domain/plugins/session-sources";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { useVueI18n } from "@/i18n/messages";
import { providerErrorText } from "@/services/plugin-provider-invoke";
import { loadPluginSessions, pluginSourcesToRefresh, type PluginSessionScope } from "@/services/plugin-session-sources";

const props = defineProps<{
  sources: PluginSessionSource[];
  scope: PluginSessionScope;
  projectRoot?: string;
  selectedKey?: string;
  selectedSessionId?: string;
  refreshToken: number;
  /** The page's search text; filters the loaded sessions. */
  query?: string;
}>();

const emit = defineEmits<{
  open: [source: PluginSessionSource, session: PluginSessionSummary];
}>();

const ICONS: Record<SessionSourceIcon, Component> = {
  chat: ChatLineSquare,
  cpu: Cpu,
  document: Document,
  monitor: Monitor,
  magic: MagicStick,
};

type SourceStatus = "idle" | "loading" | "ready" | "error";

interface SourceState {
  status: SourceStatus;
  sessions: PluginSessionSummary[];
  truncated: boolean;
  error: string;
}

const { t, localeChain } = useVueI18n();
const open = reactive<Record<string, boolean>>({});
const states = reactive<Record<string, SourceState>>({});
// Not reactive: the in-flight request of each source.
const controllers = new Map<string, AbortController>();
const idPrefix = useId();

const activeSources = computed(() => props.sources.filter((source) => source.activeInWorkspace));
const normalizedQuery = computed(() => props.query?.trim().toLocaleLowerCase() ?? "");

function stateOf(key: string): SourceState {
  return states[key] ?? { status: "idle", sessions: [], truncated: false, error: "" };
}

function sourceTitle(source: PluginSessionSource) {
  return resolvePluginTitle(source, localeChain.value);
}

function sourceTooltip(source: PluginSessionSource) {
  return t("sessions.pluginSourceTooltip", { plugin: source.pluginName || source.pluginId, id: source.pluginId });
}

/** Unique per instance and source: the position in the list, not the (escaped) key. */
function listId(index: number) {
  return `${idPrefix}-plugin-session-source-${index}`;
}

/** The number in the header: the sessions matching the page search while it is active. */
function sessionCount(source: PluginSessionSource) {
  return normalizedQuery.value ? visibleSessions(source).length : stateOf(source.key).sessions.length;
}

function visibleSessions(source: PluginSessionSource) {
  const sessions = stateOf(source.key).sessions;
  const query = normalizedQuery.value;
  if (!query) return sessions;
  return sessions.filter((session) =>
    [session.title, session.id, session.detail, session.model]
      .filter(Boolean)
      .join(" ")
      .toLocaleLowerCase()
      .includes(query),
  );
}

function formatTime(value: number | undefined) {
  if (value === undefined) return "";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(value);
}

function sessionMeta(session: PluginSessionSummary) {
  const parts: string[] = [];
  if (session.messageCount !== undefined) parts.push(`${session.messageCount} ${t("sessions.messageCount")}`);
  if (session.lastAt !== undefined) parts.push(formatTime(session.lastAt));
  return parts.join(" · ");
}

function isSelected(source: PluginSessionSource, session: PluginSessionSummary) {
  return props.selectedKey === source.key && props.selectedSessionId === session.id;
}

/** Cancels the in-flight list of a source; a cancelled load leaves the source to load again. */
function cancel(key: string) {
  const controller = controllers.get(key);
  if (!controller) return;
  controllers.delete(key);
  controller.abort();
  const state = states[key];
  if (state?.status === "loading") state.status = "idle";
}

async function load(source: PluginSessionSource) {
  cancel(source.key);
  const controller = new AbortController();
  controllers.set(source.key, controller);
  const previous = stateOf(source.key);
  states[source.key] = { ...previous, status: "loading", error: "" };
  const result = await loadPluginSessions(source, props.scope, props.projectRoot || undefined, controller.signal);
  if (controllers.get(source.key) !== controller) return;
  controllers.delete(source.key);
  if (result.ok) {
    states[source.key] = { status: "ready", sessions: result.response.sessions, truncated: result.response.truncated, error: "" };
  } else if (result.error.code === "cancelled") {
    states[source.key] = { ...previous, status: "idle" };
  } else {
    states[source.key] = { status: "error", sessions: [], truncated: false, error: providerErrorText(result.error) };
  }
}

function toggle(source: PluginSessionSource) {
  const next = !open[source.key];
  open[source.key] = next;
  if (!next) {
    cancel(source.key);
    return;
  }
  if (stateOf(source.key).status === "idle") void load(source);
}

/** Reloads the expanded sources; collapsed ones forget their list and load again when expanded. */
function reloadExpanded() {
  const expanded = pluginSourcesToRefresh(activeSources.value, open);
  const keys = new Set(expanded.map((source) => source.key));
  for (const source of activeSources.value) {
    if (keys.has(source.key)) continue;
    cancel(source.key);
    delete states[source.key];
  }
  for (const source of expanded) void load(source);
}

watch(() => props.refreshToken, reloadExpanded);
watch(() => [props.scope, props.projectRoot] as const, reloadExpanded);

// A source that leaves the snapshot (plugin disabled or uninstalled) is
// cancelled and forgotten.
watch(activeSources, (sources) => {
  const keys = new Set(sources.map((source) => source.key));
  for (const key of Object.keys(states)) {
    if (keys.has(key)) continue;
    cancel(key);
    delete states[key];
  }
  for (const key of Object.keys(open)) if (!keys.has(key)) delete open[key];
});

function cancelAll() {
  for (const key of [...controllers.keys()]) cancel(key);
}

// Leaving the page drops the loaded lists (memory only); expanded sources stay expanded.
onDeactivated(() => {
  cancelAll();
  for (const key of Object.keys(states)) delete states[key];
});
onActivated(() => {
  for (const source of pluginSourcesToRefresh(activeSources.value, open)) {
    if (stateOf(source.key).status === "idle") void load(source);
  }
});
onBeforeUnmount(cancelAll);
</script>

<template>
  <section v-if="activeSources.length" class="app-plugin-session-sources" aria-labelledby="plugin-session-sources-heading">
    <h3 id="plugin-session-sources-heading" class="app-plugin-session-sources__heading">{{ t("sessions.pluginSources") }}</h3>
    <section v-for="(source, index) in activeSources" :key="source.key" class="sessions-provider app-plugin-session-source">
      <header class="sessions-provider__header">
        <button
          type="button"
          class="sessions-provider__toggle"
          data-testid="plugin-session-source-toggle"
          :title="sourceTooltip(source)"
          :aria-expanded="open[source.key] ? 'true' : 'false'"
          :aria-controls="listId(index)"
          @click="toggle(source)"
        >
          <AppChevron :direction="open[source.key] ? 'down' : 'right'" />
          <el-icon class="sessions-provider__nav-icon is-custom">
            <component :is="ICONS[source.icon]" />
          </el-icon>
          <strong>{{ sourceTitle(source) }}</strong>
          <el-tag
            class="app-plugin-session-source__badge"
            data-testid="plugin-session-source-badge"
            size="small"
            effect="plain"
            disable-transitions
          >{{ t("sessions.pluginSourceBadge") }}</el-tag>
          <small v-if="stateOf(source.key).status === 'ready'">{{ sessionCount(source) }}</small>
        </button>
        <el-button
          text
          class="sessions-icon-button sessions-provider__refresh"
          data-testid="plugin-session-source-refresh"
          :icon="Refresh"
          :loading="stateOf(source.key).status === 'loading'"
          :title="t('sessions.refreshGroup')"
          :aria-label="t('sessions.refreshGroup')"
          @click="load(source)"
        />
      </header>

      <div v-if="open[source.key]" :id="listId(index)" class="sessions-provider__content app-plugin-session-source__content">
        <div
          v-if="stateOf(source.key).status === 'loading'"
          class="app-plugin-session-source__status"
          role="status"
          aria-live="polite"
        >
          {{ t("common.loading") }}
        </div>
        <div
          v-else-if="stateOf(source.key).status === 'error'"
          class="app-plugin-session-source__error"
          data-testid="plugin-session-source-error"
          role="alert"
        >
          <span>{{ t("sessions.pluginSourceError", { error: stateOf(source.key).error }) }}</span>
          <el-button size="small" text type="primary" data-testid="plugin-session-source-retry" @click="load(source)">
            {{ t("sessions.pluginSourceRetry") }}
          </el-button>
        </div>
        <template v-else-if="stateOf(source.key).status === 'ready'">
          <p v-if="!visibleSessions(source).length" class="sessions-provider-empty">
            {{ stateOf(source.key).sessions.length ? t("sessions.noSessions") : t("sessions.pluginSourceEmpty") }}
          </p>
          <div v-else class="app-plugin-session-source__sessions">
            <article
              v-for="session in visibleSessions(source)"
              :key="session.id"
              class="sessions-session-row"
              :class="{ 'is-active': isSelected(source, session) }"
            >
              <button
                type="button"
                class="sessions-session-row__main"
                :aria-current="isSelected(source, session) ? 'true' : undefined"
                @click="emit('open', source, session)"
              >
                <span class="sessions-session-row__title">
                  <strong :title="session.title || session.id">{{ session.title || session.id }}</strong>
                </span>
                <small v-if="session.detail || session.model">{{ session.detail || session.model }}</small>
                <span v-if="sessionMeta(session)">{{ sessionMeta(session) }}</span>
              </button>
            </article>
          </div>
          <p v-if="stateOf(source.key).truncated" class="app-plugin-session-source__notice" role="note">
            {{ t("sessions.pluginSourceTruncated") }}
          </p>
        </template>
      </div>
    </section>
  </section>
</template>
