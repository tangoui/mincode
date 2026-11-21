<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import {
  CopyDocument,
  MoreFilled,
  RefreshLeft,
  Scissor,
  View,
} from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";

interface GitHistoryEntry {
  hash: string;
  short_hash: string;
  author: string;
  email?: string;
  message: string;
  time: number;
  parents?: string[];
  refs?: string[];
}

type GitHistoryAction = "view" | "copy-hash" | "cherry-pick" | "revert";

withDefaults(
  defineProps<{
    entries: GitHistoryEntry[];
    selectedHash?: string;
    emptyText: string;
    actions?: GitHistoryAction[];
  }>(),
  {
    selectedHash: "",
    actions: () => ["view", "copy-hash"],
  }
);

const emit = defineEmits<{
  select: [entry: GitHistoryEntry];
  action: [payload: { action: Exclude<GitHistoryAction, "view" | "copy-hash">; entry: GitHistoryEntry }];
  /** The entry whose "⋯" menu is open, or null once it closed (for the `menu-extra` slot owner). */
  "menu-open-change": [entry: GitHistoryEntry | null];
}>();

defineSlots<{
  /** Extra items at the end of a row's "⋯" menu; `open` is true only while that row's menu is open. */
  "menu-extra"?: (props: { entry: GitHistoryEntry; open: boolean }) => unknown;
}>();

// One menu and one tooltip for the whole list: an ElDropdown and an ElTooltip per row made
// switching to a 100-commit history slow.
const menu = ref<{ entry: GitHistoryEntry; x: number; y: number }>();

function openMenu(event: MouseEvent, entry: GitHistoryEntry) {
  const anchor = (event.currentTarget as HTMLElement | null)?.getBoundingClientRect();
  const width = 180;
  const x = anchor ? anchor.right - width : event.clientX;
  const y = anchor ? anchor.bottom + 4 : event.clientY;
  menu.value = { entry, x: Math.max(8, Math.min(x, window.innerWidth - width - 8)), y: Math.max(8, y) };
  emit("menu-open-change", entry);
}

function closeMenu() {
  if (!menu.value) return;
  menu.value = undefined;
  emit("menu-open-change", null);
}

function runMenu(command: GitHistoryAction) {
  const entry = menu.value?.entry;
  closeMenu();
  if (entry) void handleCommand(command, entry);
}

const tip = ref<{ element: HTMLElement; text: string }>();
let tipTimer: ReturnType<typeof setTimeout> | undefined;
function showTip(event: MouseEvent, entry: GitHistoryEntry) {
  const element = event.currentTarget as HTMLElement;
  if (tipTimer) clearTimeout(tipTimer);
  tipTimer = setTimeout(() => {
    tipTimer = undefined;
    tip.value = { element, text: entry.message };
  }, 450);
}
function hideTip() {
  if (tipTimer) clearTimeout(tipTimer);
  tipTimer = undefined;
  tip.value = undefined;
}
onBeforeUnmount(() => {
  if (tipTimer) clearTimeout(tipTimer);
});

const { t, intlLocale } = useVueI18n();
const relativeTimeFormatter = computed(
  () => new Intl.RelativeTimeFormat(intlLocale.value, { numeric: "auto" })
);
const exactTimeFormatter = computed(
  () =>
    new Intl.DateTimeFormat(intlLocale.value, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    })
);

function formatRelativeTime(unixSeconds: number) {
  const diffSeconds = unixSeconds - Math.floor(Date.now() / 1000);
  const absoluteSeconds = Math.abs(diffSeconds);
  if (absoluteSeconds < 60) return relativeTimeFormatter.value.format(diffSeconds, "second");
  if (absoluteSeconds < 3600) {
    return relativeTimeFormatter.value.format(Math.trunc(diffSeconds / 60), "minute");
  }
  if (absoluteSeconds < 86400) {
    return relativeTimeFormatter.value.format(Math.trunc(diffSeconds / 3600), "hour");
  }
  if (absoluteSeconds < 2592000) {
    return relativeTimeFormatter.value.format(Math.trunc(diffSeconds / 86400), "day");
  }
  return exactTimeFormatter.value.format(new Date(unixSeconds * 1000));
}

function formatExactTime(unixSeconds: number) {
  return exactTimeFormatter.value.format(new Date(unixSeconds * 1000));
}

function normalizedRefs(entry: GitHistoryEntry) {
  return (entry.refs ?? [])
    .map((refName) => refName.replace(/^refs\/(heads|tags|remotes)\//, ""))
    .slice(0, 2);
}

function selectEntry(entry: GitHistoryEntry) {
  emit("select", entry);
}

async function copyHash(entry: GitHistoryEntry) {
  try {
    await navigator.clipboard.writeText(entry.hash);
    ElMessage.success(t("git.hashCopied", { hash: entry.short_hash }));
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function handleCommand(command: string | number | object | undefined, entry: GitHistoryEntry) {
  // Items added through the menu-extra slot carry no command.
  if (command === undefined) return;
  const action = String(command) as GitHistoryAction;
  if (action === "view") {
    selectEntry(entry);
    return;
  }
  if (action === "copy-hash") {
    await copyHash(entry);
    return;
  }
  emit("action", { action, entry });
}
</script>

<template>
  <div class="app-git-history">
    <el-scrollbar v-if="entries.length" class="app-git-history__scroll">
      <div class="app-git-history__items">
        <div
          v-for="(entry, index) in entries"
          :key="entry.hash"
          class="app-git-history__row"
          :class="{
            'is-selected': selectedHash === entry.hash,
            'is-first': index === 0,
            'is-last': index === entries.length - 1,
          }"
        >
          <button type="button" class="app-git-history__main" @click="selectEntry(entry)">
            <span class="app-git-history__rail" aria-hidden="true"><i /></span>
            <span class="app-git-history__content">
              <span class="app-git-history__primary">
                <strong @mouseenter="showTip($event, entry)" @mouseleave="hideTip">{{ entry.message }}</strong>
                <time :title="formatExactTime(entry.time)">{{ formatRelativeTime(entry.time) }}</time>
              </span>
              <span class="app-git-history__meta">
                <code>{{ entry.short_hash }}</code>
                <span class="app-git-history__author" :title="entry.email || entry.author">
                  {{ entry.author }}
                </span>
                <template v-if="normalizedRefs(entry).length">
                  <el-tag
                    v-for="refName in normalizedRefs(entry)"
                    :key="refName"
                    size="small"
                    effect="plain"
                    round
                  >
                    {{ refName }}
                  </el-tag>
                  <small v-if="(entry.refs?.length ?? 0) > 2">+{{ (entry.refs?.length ?? 0) - 2 }}</small>
                </template>
              </span>
            </span>
          </button>

          <el-button
            v-if="actions.length"
            text
            native-type="button"
            class="app-git-history__more"
            :title="t('common.more')"
            :aria-label="t('common.more')"
            aria-haspopup="menu"
            @click.stop="openMenu($event, entry)"
          >
            <el-icon><MoreFilled /></el-icon>
          </el-button>
        </div>
      </div>
    </el-scrollbar>
    <div v-else class="app-git-history__empty">{{ emptyText }}</div>
    <el-tooltip
      v-if="tip"
      :visible="true"
      virtual-triggering
      :virtual-ref="tip.element"
      :content="tip.text"
      placement="right"
    />
    <AppContextMenu
      v-if="menu"
      :open="true"
      :x="menu.x"
      :y="menu.y"
      :width="180"
      popper-class="app-git-history-menu"
      @close="closeMenu"
    >
      <el-dropdown-item v-if="actions.includes('view')" @click="runMenu('view')">
        <el-icon><View /></el-icon>{{ t("git.viewCommit") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="actions.includes('copy-hash')" @click="runMenu('copy-hash')">
        <el-icon><CopyDocument /></el-icon>{{ t("git.copyHash") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="actions.includes('cherry-pick')" divided @click="runMenu('cherry-pick')">
        <el-icon><Scissor /></el-icon>{{ t("git.cherryPick") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="actions.includes('revert')" class="is-danger" @click="runMenu('revert')">
        <el-icon><RefreshLeft /></el-icon>{{ t("git.revert") }}
      </el-dropdown-item>
      <slot name="menu-extra" :entry="menu.entry" :open="true" />
    </AppContextMenu>
  </div>
</template>

<style scoped>
.app-git-history {
  width: 100%;
  min-height: 0;
  flex: 1;
  overflow: hidden;
  background: var(--app-surface);
  container-type: inline-size;
}

.app-git-history__scroll {
  height: 100%;
}

.app-git-history__items {
  padding: 4px 5px 8px;
}

.app-git-history__row {
  position: relative;
  display: flex;
  min-width: 0;
  min-height: 44px;
  align-items: stretch;
  border-radius: 5px;
  color: var(--app-text);
}

.app-git-history__row:hover {
  background: color-mix(in srgb, var(--app-hover) 78%, transparent);
}

.app-git-history__row.is-selected {
  background: var(--app-accent-soft);
}

.app-git-history__row.is-selected::before {
  position: absolute;
  z-index: 2;
  top: 7px;
  bottom: 7px;
  left: 0;
  width: 2px;
  border-radius: 2px;
  background: var(--app-accent);
  content: "";
}

.app-git-history__main {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: stretch;
  gap: 7px;
  border: 0;
  background: transparent;
  padding: 5px 30px 5px 7px;
  color: inherit;
  text-align: left;
  cursor: pointer;
}

.app-git-history__rail {
  position: relative;
  width: 9px;
  flex: none;
}

.app-git-history__rail::before,
.app-git-history__rail::after {
  position: absolute;
  left: 4px;
  width: 1px;
  background: color-mix(in srgb, var(--app-border-strong) 72%, transparent);
  content: "";
}

.app-git-history__rail::before {
  top: -5px;
  height: 9.5px;
}

.app-git-history__rail::after {
  top: 12.5px;
  bottom: -5px;
}

.app-git-history__row.is-first .app-git-history__rail::before,
.app-git-history__row.is-last .app-git-history__rail::after {
  display: none;
}

.app-git-history__rail i {
  position: absolute;
  z-index: 1;
  top: 4.5px;
  left: 1px;
  width: 7px;
  height: 7px;
  border: 2px solid var(--app-surface);
  border-radius: 50%;
  background: color-mix(in srgb, var(--app-text-muted) 76%, var(--app-surface));
  outline: 1px solid color-mix(in srgb, var(--app-border-strong) 76%, transparent);
}

.app-git-history__row.is-selected .app-git-history__rail i {
  background: var(--app-accent);
  outline-color: color-mix(in srgb, var(--app-accent) 60%, transparent);
}

.app-git-history__content {
  display: flex;
  min-width: 0;
  flex: 1;
  justify-content: center;
  flex-direction: column;
  gap: 2px;
}

.app-git-history__primary,
.app-git-history__meta {
  display: flex;
  min-width: 0;
  align-items: center;
}

.app-git-history__primary {
  gap: 8px;
}

.app-git-history__primary strong {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: var(--app-text);
  font-size: 11px;
  font-weight: 550;
  line-height: 1.25;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-git-history__primary time {
  display: none;
  flex: none;
  color: var(--app-text-muted);
  font-size: 9px;
  white-space: nowrap;
}

.app-git-history__meta {
  /* Matches the ref tag height so rows with and without tags line up. */
  min-height: 16px;
  gap: 5px;
  color: var(--app-text-muted);
  font-size: 9px;
  line-height: 1.3;
}

.app-git-history__meta code {
  flex: none;
  color: var(--app-accent);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 9.5px;
}

.app-git-history__author {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-git-history__author::before {
  margin-right: 5px;
  color: color-mix(in srgb, var(--app-text-muted) 60%, transparent);
  content: "·";
  content: "·" / "";
}

.app-git-history__meta small {
  flex: none;
  font-size: 9px;
}

.app-git-history__meta :deep(.el-tag) {
  --el-tag-bg-color: color-mix(in srgb, var(--app-accent) 8%, transparent);
  --el-tag-border-color: color-mix(in srgb, var(--app-accent) 24%, transparent);
  --el-tag-text-color: var(--app-accent);
  max-width: 74px;
  height: 16px;
  padding: 0 5px;
  overflow: hidden;
  font-size: 8px;
  text-overflow: ellipsis;
}

.app-git-history__more {
  position: absolute;
  z-index: 3;
  top: 50%;
  right: 4px;
  display: grid;
  width: 22px;
  height: 22px;
  place-items: center;
  border: 0;
  border-radius: 4px;
  background: color-mix(in srgb, var(--app-surface) 88%, transparent);
  color: var(--app-text-muted);
  cursor: pointer;
  opacity: 0;
  transform: translateY(-50%);
}

.app-git-history__row:hover .app-git-history__more,
.app-git-history__row.is-selected .app-git-history__more,
.app-git-history__more:focus-visible {
  opacity: 1;
}

.app-git-history__more:hover {
  background: var(--app-hover);
  color: var(--app-text);
}

.app-git-history__empty {
  display: flex;
  min-height: 120px;
  align-items: center;
  justify-content: center;
  padding: 20px 14px;
  color: var(--app-text-muted);
  font-size: 11px;
  text-align: center;
}

@container (min-width: 360px) {
  .app-git-history__primary time {
    display: inline;
  }
}

@media (hover: none) {
  .app-git-history__more {
    opacity: 1;
  }
}
</style>

<style>
.app-git-history-menu .el-dropdown-menu__item {
  gap: 7px;
  min-height: 30px;
  font-size: 11px;
}

.app-git-history-menu .el-dropdown-menu__item .el-icon {
  color: var(--app-text-muted);
}

.app-git-history-menu .el-dropdown-menu__item.is-danger:not(.is-disabled),
.app-git-history-menu .el-dropdown-menu__item.is-danger:not(.is-disabled) .el-icon {
  color: var(--app-danger);
}
</style>
