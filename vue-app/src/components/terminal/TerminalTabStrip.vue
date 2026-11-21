<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import type { TabPaneName } from "element-plus";
import { Close, Plus } from "@element-plus/icons-vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import { focusInlineInput } from "@/domain/ui/focus-inline-input";
import type { TerminalTab } from "@/domain/terminal/terminal-tabs";
import { TERMINAL_TAB_TITLE_MAX } from "@/domain/terminal/terminal-tabs";
import {
  formatTerminalMemory,
  terminalMemoryLevel,
  type TerminalTabUsage,
} from "@/domain/terminal/resource-usage";
import { useVueI18n } from "@/i18n/messages";

export type TerminalTabAddMenuCommand = "configured" | "ssh" | `profile:${string}`;

const props = defineProps<{
  tabs: TerminalTab[];
  activeTabId: string;
  canAdd: boolean;
  labelFor: (tab: TerminalTab) => string;
  attentionFor: (tab: TerminalTab) => boolean;
  profiles: { key: string; title: string }[];
  /** Memory of a tab's local terminals; shown from 1 GB on. */
  usageFor?: (tab: TerminalTab) => TerminalTabUsage | undefined;
  /** Set while the tabs must not close (last tab of a pinned workbench tab): shows a disabled × with this reason. */
  closeDisabledReason?: string;
}>();

const emit = defineEmits<{
  select: [tabId: string];
  close: [tabId: string];
  /** Several tabs closed at once from the tab context menu (close others / left / right). */
  "close-tabs": [tabIds: string[]];
  rename: [tabId: string, title: string];
  add: [];
  "add-menu": [kind: TerminalTabAddMenuCommand];
  /** End a process listed in a tab's memory popover (sessionId is its pane). */
  "end-process": [sessionId: string, pid: number, name: string];
}>();

const { t } = useVueI18n();

const editingTabId = ref<string>();
const renameDraft = ref("");
let renameInitial = "";
let renameInput: { focus: () => void; select: () => void } | undefined;

function setRenameInput(instance: unknown) {
  renameInput = instance && typeof instance === "object" && "focus" in instance && "select" in instance
    ? (instance as { focus: () => void; select: () => void })
    : undefined;
}

async function startRename(tab: TerminalTab, fromMenu = false) {
  renameInitial = props.labelFor(tab);
  renameDraft.value = renameInitial;
  editingTabId.value = tab.id;
  await nextTick();
  // From the context menu the dropdown hands focus back as it closes; focus after it.
  if (fromMenu) {
    focusInlineInput(`[data-terminal-tab-rename-input="${tab.id}"]`);
    return;
  }
  renameInput?.focus();
  renameInput?.select();
}

function commitRename() {
  const tabId = editingTabId.value;
  if (!tabId) return;
  editingTabId.value = undefined;
  // Confirming the shown default name unchanged must not pin it as a custom title.
  if (renameDraft.value.trim() === renameInitial.trim()) return;
  emit("rename", tabId, renameDraft.value);
}

function cancelRename() {
  editingTabId.value = undefined;
}

function handleRenameKeydown(event: KeyboardEvent) {
  // Keys typed into the field must not reach the tab nav (arrow keys switch tabs).
  event.stopPropagation();
  if (event.isComposing) return;
  if (event.key === "Enter") {
    event.preventDefault();
    commitRename();
  } else if (event.key === "Escape") {
    event.preventDefault();
    cancelRename();
  }
}

function selectTab(name: TabPaneName) {
  const tabId = String(name);
  if (tabId !== props.activeTabId) emit("select", tabId);
}

type TabMenuCommand = "rename" | "close" | "close-other" | "close-left" | "close-right";

const contextMenu = ref<{ tabId: string; x: number; y: number }>();

const contextMenuTargets = computed(() => {
  const index = props.tabs.findIndex((tab) => tab.id === contextMenu.value?.tabId);
  if (index < 0) return { left: [], right: [], other: [] };
  const ids = props.tabs.map((tab) => tab.id);
  return {
    left: ids.slice(0, index),
    right: ids.slice(index + 1),
    other: ids.filter((_, i) => i !== index),
  };
});

// el-tabs renders each tab header as `.el-tabs__item#tab-<name>`; right click anywhere on it.
function openContextMenu(event: MouseEvent) {
  const item = (event.target as Element | null)?.closest?.(".el-tabs__item");
  const tabId = item?.id.startsWith("tab-") ? item.id.slice(4) : undefined;
  if (!tabId || !props.tabs.some((tab) => tab.id === tabId)) return;
  event.preventDefault();
  event.stopPropagation();
  contextMenu.value = { tabId, x: event.clientX, y: event.clientY };
}

function closeContextMenu() {
  contextMenu.value = undefined;
}

function runTabMenuCommand(command: TabMenuCommand) {
  const menu = contextMenu.value;
  const targets = contextMenuTargets.value;
  closeContextMenu();
  if (!menu) return;
  const tab = props.tabs.find((item) => item.id === menu.tabId);
  if (!tab) return;
  if (command === "rename") void startRename(tab, true);
  else if (command === "close") {
    if (!props.closeDisabledReason) emit("close", tab.id);
  }
  else {
    const ids = command === "close-left" ? targets.left : command === "close-right" ? targets.right : targets.other;
    if (ids.length) emit("close-tabs", ids);
  }
}

function shownUsage(tab: TerminalTab) {
  const usage = props.usageFor?.(tab);
  return usage && terminalMemoryLevel(usage.bytes) !== "hidden" ? usage : undefined;
}

function handleAddMenu(command: string | number | object) {
  if (command === "configured" || command === "ssh") emit("add-menu", command);
  else if (typeof command === "string" && command.startsWith("profile:")) emit("add-menu", command as `profile:${string}`);
}
</script>

<template>
  <header class="terminal-tab-strip">
    <AppWorkspaceTabs
      class="terminal-tab-strip__tabs"
      :model-value="activeTabId"
      :aria-label="t('nav.terminal')"
      @update:model-value="selectTab"
      @tab-remove="emit('close', String($event))"
      @contextmenu="openContextMenu"
    >
      <el-tab-pane v-for="tab in tabs" :key="tab.id" :name="tab.id" :closable="!closeDisabledReason">
        <template #label>
          <span
            v-if="attentionFor(tab)"
            class="terminal-tab-strip__attention"
            role="img"
            :aria-label="t('terminal.tabs.needsAttention')"
            :title="t('terminal.tabs.needsAttention')"
          />
          <el-input
            v-if="editingTabId === tab.id"
            :ref="setRenameInput"
            v-model="renameDraft"
            class="terminal-tab-strip__rename"
            size="small"
            :maxlength="TERMINAL_TAB_TITLE_MAX"
            :aria-label="t('terminal.tabs.rename')"
            :data-terminal-tab-rename-input="tab.id"
            @keydown="handleRenameKeydown"
            @blur="commitRename"
            @click.stop
          />
          <span
            v-else
            class="terminal-tab-strip__label"
            :title="labelFor(tab)"
            dir="auto"
            @dblclick.stop="startRename(tab)"
          >{{ labelFor(tab) }}</span>
          <el-popover
            v-if="shownUsage(tab)"
            trigger="click"
            placement="bottom-start"
            :width="320"
            popper-class="terminal-tab-memory-popper"
          >
            <template #reference>
              <button
                type="button"
                class="terminal-tab-strip__memory"
                :class="{ 'is-high': terminalMemoryLevel(shownUsage(tab)!.bytes) === 'high' }"
                :aria-label="t('terminal.usage.badge', { size: formatTerminalMemory(shownUsage(tab)!.bytes) })"
                @click.stop
                @mousedown.stop
              >{{ formatTerminalMemory(shownUsage(tab)!.bytes) }}</button>
            </template>
            <div class="terminal-tab-memory">
              <div class="terminal-tab-memory__summary">
                {{
                  t("terminal.usage.summary", {
                    size: formatTerminalMemory(shownUsage(tab)!.bytes),
                    count: shownUsage(tab)!.processCount,
                  })
                }}
              </div>
              <ul class="terminal-tab-memory__list">
                <li
                  v-for="process in shownUsage(tab)!.processes"
                  :key="`${process.sessionId}:${process.pid}`"
                  class="terminal-tab-memory__process"
                >
                  <span class="terminal-tab-memory__name" :title="`${process.name} (PID ${process.pid})`">{{ process.name }}</span>
                  <span class="terminal-tab-memory__size">{{ formatTerminalMemory(process.bytes) }}</span>
                  <el-button
                    v-if="!process.isShell"
                    link
                    type="danger"
                    size="small"
                    class="terminal-tab-memory__end"
                    @click="emit('end-process', process.sessionId, process.pid, process.name)"
                  >{{ t("terminal.usage.end") }}</el-button>
                  <span v-else class="terminal-tab-memory__shell">{{ t("terminal.usage.shell") }}</span>
                </li>
              </ul>
            </div>
          </el-popover>
          <el-tooltip v-if="closeDisabledReason" :content="closeDisabledReason" placement="bottom">
            <span
              class="terminal-tab-strip__close is-disabled"
              role="img"
              :aria-label="closeDisabledReason"
              @click.stop
            >
              <el-icon><Close /></el-icon>
            </span>
          </el-tooltip>
        </template>
      </el-tab-pane>
    </AppWorkspaceTabs>

    <AppContextMenu
      v-if="contextMenu"
      :open="true"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :width="180"
      :aria-label="t('terminal.tabs.menu')"
      @close="closeContextMenu"
    >
      <el-dropdown-item @click="runTabMenuCommand('rename')">{{ t("tab.rename") }}</el-dropdown-item>
      <el-dropdown-item :disabled="Boolean(closeDisabledReason)" @click="runTabMenuCommand('close')">
        {{ t("common.close") }}
      </el-dropdown-item>
      <el-dropdown-item
        divided
        :disabled="!contextMenuTargets.other.length"
        @click="runTabMenuCommand('close-other')"
      >
        {{ t("tab.closeOther") }}
      </el-dropdown-item>
      <el-dropdown-item :disabled="!contextMenuTargets.left.length" @click="runTabMenuCommand('close-left')">
        {{ t("tab.closeLeft") }}
      </el-dropdown-item>
      <el-dropdown-item :disabled="!contextMenuTargets.right.length" @click="runTabMenuCommand('close-right')">
        {{ t("tab.closeRight") }}
      </el-dropdown-item>
    </AppContextMenu>

    <div class="terminal-tab-strip__actions">
      <el-tooltip :content="canAdd ? t('terminal.tabs.add') : t('terminal.maxTabs')" placement="bottom">
        <span class="terminal-tab-strip__add-wrap">
          <el-button
            text
            native-type="button"
            class="terminal-tab-strip__add"
            :aria-label="t('terminal.tabs.add')"
            :disabled="!canAdd"
            @click="emit('add')"
          >
            <el-icon><Plus /></el-icon>
          </el-button>
        </span>
      </el-tooltip>
      <el-dropdown
        trigger="click"
        placement="bottom-end"
        :disabled="!canAdd"
        @command="handleAddMenu"
      >
        <el-button
          text
          native-type="button"
          class="terminal-tab-strip__add-menu"
          :aria-label="t('terminal.tabs.addMenu')"
          :title="t('terminal.tabs.addMenu')"
          :disabled="!canAdd"
        >
          <AppChevron direction="down" />
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="configured">{{ t("terminal.tabs.configuredShell") }}</el-dropdown-item>
            <el-dropdown-item command="ssh">{{ t("terminal.tabs.ssh") }}</el-dropdown-item>
            <el-dropdown-item
              v-for="(profile, index) in profiles"
              :key="profile.key"
              :command="`profile:${profile.key}`"
              :divided="index === 0"
            >
              <span dir="auto">{{ profile.title }}</span>
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
  </header>
</template>
