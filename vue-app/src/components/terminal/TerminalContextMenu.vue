<script setup lang="ts">
import { computed, onDeactivated } from "vue";
import {
  Aim,
  Close,
  CopyDocument,
  Delete,
  DocumentCopy,
  FolderOpened,
  Link,
  MagicStick,
  MostlyCloudy,
  RefreshRight,
  Search,
  Select,
  Star,
  Switch,
  TopRight,
  Upload,
} from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { useVueI18n } from "@/i18n/messages";
import { useKeybindingsStore } from "@/stores/keybindings";

import type { TerminalContextMenuAction } from "@/domain/terminal/context-menu";

const props = withDefaults(
  defineProps<{
    open: boolean;
    x: number;
    y: number;
    selection: string;
    token?: string;
    url?: string;
    path?: string;
    ready: boolean;
    canClose: boolean;
    canSplit: boolean;
    terminalKind: "local" | "ssh" | "docker";
    canRevealInSftp?: boolean;
  }>(),
  {
    token: "",
    url: undefined,
    path: undefined,
    canRevealInSftp: false,
  }
);

const emit = defineEmits<{
  close: [];
  action: [action: TerminalContextMenuAction];
}>();
const { t } = useVueI18n();
const keybindings = useKeybindingsStore();
// Labels follow the effective keymap (preset + user overrides); empty when unbound.
const shortcuts = computed(() => ({
  copy: keybindings.label("terminal:copy"),
  paste: keybindings.label("terminal:paste"),
  selectAll: keybindings.label("terminal:select-all"),
  clear: keybindings.label("terminal:clear"),
}));
const menuX = computed(() => Math.max(8, Math.min(props.x, window.innerWidth - 246)));
const menuY = computed(() => {
  const availableHeight = Math.max(0, window.innerHeight - 16);
  const estimatedHeight = Math.min(580, availableHeight);
  return Math.max(8, Math.min(props.y, window.innerHeight - estimatedHeight - 8));
});

onDeactivated(() => {
  if (props.open) emit("close");
});

function choose(action: TerminalContextMenuAction) {
  emit("action", action);
  emit("close");
}
</script>

<template>
  <AppContextMenu
    :open="open"
    :x="menuX"
    :y="menuY"
    :width="238"
    popper-class="terminal-context-menu"
    @close="emit('close')"
  >
    <el-dropdown-item :disabled="!selection" @click="choose('copy')">
      <el-icon><CopyDocument /></el-icon>{{ t('terminal.context.copy') }}
      <kbd v-if="shortcuts.copy" class="terminal-context-menu__shortcut">{{ shortcuts.copy }}</kbd>
    </el-dropdown-item>
    <el-dropdown-item :disabled="!ready" @click="choose('paste')">
      <el-icon><Upload /></el-icon>{{ t('terminal.context.paste') }}
      <kbd v-if="shortcuts.paste" class="terminal-context-menu__shortcut">{{ shortcuts.paste }}</kbd>
    </el-dropdown-item>
    <el-dropdown-item @click="choose('select-all')">
      <el-icon><Select /></el-icon>{{ t('terminal.context.selectAll') }}
      <kbd v-if="shortcuts.selectAll" class="terminal-context-menu__shortcut">{{ shortcuts.selectAll }}</kbd>
    </el-dropdown-item>
    <el-dropdown-item :disabled="!selection" @click="choose('search-selection')">
      <el-icon><Search /></el-icon>{{ t('terminal.context.searchSelection') }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="choose('clear')">
      <el-icon><Delete /></el-icon>{{ t('terminal.context.clear') }}
      <kbd v-if="shortcuts.clear" class="terminal-context-menu__shortcut">{{ shortcuts.clear }}</kbd>
    </el-dropdown-item>
    <el-dropdown-item :disabled="!selection" @click="choose('copy-markdown')">
      <el-icon><DocumentCopy /></el-icon>{{ t('terminal.context.copyMarkdown') }}
    </el-dropdown-item>
    <template v-if="url">
      <el-dropdown-item @click="choose('open-url')">
        <el-icon><TopRight /></el-icon>{{ t('terminal.context.openUrl') }}
      </el-dropdown-item>
      <el-dropdown-item @click="choose('copy-url')">
        <el-icon><Link /></el-icon>{{ t('terminal.context.copyUrl') }}
      </el-dropdown-item>
    </template>
    <template v-if="path">
      <el-dropdown-item @click="choose('copy-path')">
        <el-icon><MostlyCloudy /></el-icon>{{ t('terminal.context.copyPath') }}
      </el-dropdown-item>
      <el-dropdown-item
        v-if="terminalKind === 'ssh'"
        :disabled="!canRevealInSftp || !ready"
        @click="choose('reveal-sftp')"
      >
        <el-icon><FolderOpened /></el-icon>{{ t('terminal.context.revealSftp') }}
      </el-dropdown-item>
    </template>
    <el-dropdown-item :disabled="!selection" @click="choose('bookmark')">
      <el-icon><Star /></el-icon>{{ t('terminal.context.bookmark') }}
    </el-dropdown-item>
    <el-dropdown-item v-if="terminalKind === 'ssh'" @click="choose('copy-ssh-command')">
      <el-icon><Aim /></el-icon>{{ t('terminal.context.copySshCommand') }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="choose('ai-assistant')">
      <el-icon><MagicStick /></el-icon>{{ t('terminal.ai.menu') }}
    </el-dropdown-item>
    <el-dropdown-item :disabled="!ready" @click="choose('send-interrupt')">
      <el-icon><Close /></el-icon>{{ t('terminal.context.sendInterrupt') }}
    </el-dropdown-item>
    <el-dropdown-item :disabled="!ready" @click="choose('send-eof')">
      <el-icon><Switch /></el-icon>{{ t('terminal.context.sendEof') }}
    </el-dropdown-item>
    <el-dropdown-item divided :disabled="!canSplit" @click="choose('split-horizontal')">
      <span class="terminal-context-menu__glyph">⇆</span>{{ t('terminal.splitRight') }}
    </el-dropdown-item>
    <el-dropdown-item :disabled="!canSplit" @click="choose('split-vertical')">
      <span class="terminal-context-menu__glyph">⇅</span>{{ t('terminal.splitDown') }}
    </el-dropdown-item>
    <el-dropdown-item @click="choose('reload')">
      <el-icon><RefreshRight /></el-icon>{{ t('terminal.reload') }}
    </el-dropdown-item>
    <el-dropdown-item class="is-danger" :disabled="!canClose" @click="choose('close')">
      <el-icon><Close /></el-icon>{{ t('terminal.closePane') }}
    </el-dropdown-item>
  </AppContextMenu>
</template>
