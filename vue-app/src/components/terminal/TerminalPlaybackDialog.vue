<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { CloseBold, Delete, EditPen, VideoPlay } from "@element-plus/icons-vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type IDisposable } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n } from "@/i18n/messages";
import {
  attachTerminalWebglAddon,
  readTerminalPreferences,
} from "@/services/terminal-preferences";
import type { SavedTerminalRecording } from "@/services/terminal-tools";
import {
  resolveTerminalTheme,
  setTerminalTheme,
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT,
  type TerminalColorScheme,
} from "@/services/terminal-theme";
import { UI_THEME_CHANGED_EVENT, type UiTheme } from "@/services/ui-theme";

const props = defineProps<{
  modelValue: boolean;
  recordings: readonly SavedTerminalRecording[];
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  delete: [id: string];
  rename: [id: string, name: string];
}>();

const { t } = useVueI18n();
const playbackHost = ref<HTMLElement>();
const selectedId = ref<string>();
const playing = ref(false);
const speed = ref(1);
const query = ref("");
const editingId = ref<string>();
const draftName = ref("");
const renameInput = ref<{ focus?: () => void; select?: () => void }>();

let xterm: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let webglAddonHandle: IDisposable | null = null;
let resizeObserver: ResizeObserver | null = null;
let playbackTimer: ReturnType<typeof setTimeout> | null = null;
let playbackRun = 0;

function selectedRecording() {
  return props.recordings.find((recording) => recording.id === selectedId.value);
}

function recordedAt(recording: SavedTerminalRecording) {
  return new Date(recording.createdAt).toLocaleString();
}

// Recordings saved before sessions named them carry the timestamp as their name;
// show the date once for those, and as the meta line for named ones.
function hasOwnName(recording: SavedTerminalRecording) {
  return recording.name !== recordedAt(recording);
}

// The timestamp stays searchable so a renamed recording can still be found by
// when it was taken.
const filteredRecordings = computed(() => {
  const needle = query.value.trim().toLowerCase();
  if (!needle) return props.recordings;
  return props.recordings.filter(
    (recording) =>
      recording.name.toLowerCase().includes(needle) ||
      recordedAt(recording).toLowerCase().includes(needle)
  );
});

// Hiding the selected row would leave the player on a recording the user cannot
// see, so the selection follows the first visible result.
watch(filteredRecordings, (visible) => {
  if (visible.some((recording) => recording.id === selectedId.value)) return;
  selectedId.value = visible[0]?.id;
  resetTerminal();
});

function setRenameInput(element: unknown) {
  renameInput.value = (element as { focus?: () => void; select?: () => void } | null) ?? undefined;
}

async function startRename(recording: SavedTerminalRecording) {
  editingId.value = recording.id;
  draftName.value = recording.name;
  await nextTick();
  renameInput.value?.focus?.();
  renameInput.value?.select?.();
}

function cancelRename() {
  editingId.value = undefined;
}

// Enter saves, Escape cancels, and the blur after either must not save twice:
// clearing the editing id first turns the second call into a no-op.
function commitRename() {
  const id = editingId.value;
  if (!id) return;
  editingId.value = undefined;
  const name = draftName.value.trim();
  if (!name) return;
  if (name === props.recordings.find((recording) => recording.id === id)?.name) return;
  emit("rename", id, name);
}

function stopPlayback() {
  playbackRun += 1;
  if (playbackTimer) clearTimeout(playbackTimer);
  playbackTimer = null;
  playing.value = false;
}

function resetTerminal() {
  stopPlayback();
  xterm?.reset();
}

function fitPlaybackTerminal() {
  try {
    fitAddon?.fit();
  } catch {
    // Dialog transition may temporarily yield no usable size.
  }
}

function handleThemeChange(event: Event) {
  if (xterm) setTerminalTheme(xterm, resolveTerminalTheme((event as CustomEvent<UiTheme>).detail));
}

function handleColorSchemeChange(event: Event) {
  if (xterm) {
    setTerminalTheme(
      xterm,
      resolveTerminalTheme(undefined, (event as CustomEvent<TerminalColorScheme>).detail)
    );
  }
}

function ensureTerminal() {
  if (!playbackHost.value || xterm) return;
  xterm = new Terminal({
    cursorBlink: false,
    disableStdin: true,
    fontFamily: '"SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    lineHeight: 1.25,
    scrollback: readTerminalPreferences().scrollback,
  });
  fitAddon = new FitAddon();
  xterm.loadAddon(fitAddon);
  xterm.open(playbackHost.value);
  setTerminalTheme(xterm, resolveTerminalTheme());
  webglAddonHandle = attachTerminalWebglAddon(xterm) ?? null;
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.addEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);
  resizeObserver = new ResizeObserver(fitPlaybackTerminal);
  resizeObserver.observe(playbackHost.value);
  fitPlaybackTerminal();
}

function selectRecording(id: string) {
  selectedId.value = id;
  resetTerminal();
}

function playRecording(speedOverride?: number) {
  const recording = selectedRecording();
  if (!recording?.frames.length) return;
  stopPlayback();
  xterm?.reset();
  const run = ++playbackRun;
  const multiplier = speedOverride ?? speed.value;
  let index = 0;
  let previousTime = 0;
  playing.value = true;

  const writeNext = () => {
    if (run !== playbackRun || !recording.frames[index]) {
      if (run === playbackRun) playing.value = false;
      return;
    }
    const frame = recording.frames[index];
    const delay = Math.max(0, (frame.t - previousTime) / multiplier);
    playbackTimer = setTimeout(() => {
      if (run !== playbackRun) return;
      xterm?.write(frame.d);
      previousTime = frame.t;
      index += 1;
      writeNext();
    }, delay);
  };

  writeNext();
}

function close() {
  stopPlayback();
  emit("update:modelValue", false);
}

watch(
  () => props.modelValue,
  async (visible) => {
    if (!visible) {
      stopPlayback();
      return;
    }
    if (
      !selectedId.value ||
      !props.recordings.some((recording) => recording.id === selectedId.value)
    ) {
      selectedId.value = props.recordings[0]?.id;
    }
    await nextTick();
    ensureTerminal();
    fitPlaybackTerminal();
  },
  { immediate: true }
);

watch(
  () => props.recordings,
  (recordings) => {
    if (!selectedId.value || !recordings.some((recording) => recording.id === selectedId.value)) {
      selectedId.value = recordings[0]?.id;
      resetTerminal();
    }
  },
  { deep: true }
);

onBeforeUnmount(() => {
  stopPlayback();
  resizeObserver?.disconnect();
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.removeEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);
  webglAddonHandle?.dispose();
  webglAddonHandle = null;
  xterm?.dispose();
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('terminal.playback')"
    width="min(900px, calc(100vw - 32px))"
    class="terminal-tools-dialog terminal-playback-dialog-shell"
    append-to-body
    @update:model-value="close"
  >
    <div class="terminal-playback-dialog">
      <div class="terminal-playback-dialog__recordings">
        <AppSearchField
          v-model="query"
          class="terminal-playback-dialog__search"
          clearable
          :placeholder="t('terminal.playbackSearch')"
          :aria-label="t('terminal.playbackSearch')"
          :clear-label="t('common.clear')"
        />
        <el-scrollbar class="terminal-playback-dialog__list">
          <el-empty
            v-if="!recordings.length"
            :image-size="48"
            :description="t('terminal.playbackEmpty')"
          />
          <el-empty
            v-else-if="!filteredRecordings.length"
            :image-size="48"
            :description="t('terminal.playbackNoMatch')"
          />
          <div
            v-for="recording in filteredRecordings"
            :key="recording.id"
            :class="{
              'is-active': recording.id === selectedId,
              'is-editing': editingId === recording.id,
            }"
            role="option"
            tabindex="0"
            :aria-selected="recording.id === selectedId"
            @click="selectRecording(recording.id)"
            @keydown.enter.prevent="selectRecording(recording.id)"
            @keydown.space.prevent="selectRecording(recording.id)"
            @dblclick="startRename(recording)"
          >
            <template v-if="editingId === recording.id">
              <el-input
                :ref="setRenameInput"
                v-model="draftName"
                size="small"
                maxlength="80"
                @click.stop
                @keydown.enter.prevent.stop="commitRename"
                @keydown.esc.prevent.stop="cancelRename"
                @blur="commitRename"
              />
            </template>
            <template v-else>
              <span>{{ recording.name }}</span>
              <small>
                <template v-if="hasOwnName(recording)">{{ recordedAt(recording) }} · </template>
                {{ recording.frames.length }} {{ t("terminal.playbackFrames") }}
              </small>
              <span class="terminal-playback-dialog__row-actions">
                <el-tooltip :content="t('terminal.playbackRename')">
                  <el-button
                    text
                    native-type="button"
                    :icon="EditPen"
                    :aria-label="t('terminal.playbackRename')"
                    @click.stop="startRename(recording)"
                  />
                </el-tooltip>
                <el-tooltip :content="t('common.delete')">
                  <el-button
                    text
                    native-type="button"
                    :icon="Delete"
                    :aria-label="t('common.delete')"
                    @click.stop="emit('delete', recording.id)"
                  />
                </el-tooltip>
              </span>
            </template>
          </div>
        </el-scrollbar>
      </div>
      <section class="terminal-playback-dialog__viewer">
        <div ref="playbackHost" class="terminal-playback-dialog__terminal" />
        <footer class="terminal-playback-dialog__controls">
          <el-button
            :disabled="!selectedRecording()?.frames.length || playing"
            type="primary"
            @click="playRecording()"
          >
            <el-icon><VideoPlay /></el-icon>{{ t("terminal.playbackPlay") }}
          </el-button>
          <el-button :disabled="!playing" @click="stopPlayback">
            <el-icon><CloseBold /></el-icon>{{ t("terminal.playbackStop") }}
          </el-button>
          <el-select v-model="speed" class="terminal-playback-dialog__speed">
            <el-option :value="0.5" label="0.5×" />
            <el-option :value="1" label="1×" />
            <el-option :value="2" label="2×" />
            <el-option :value="4" label="4×" />
          </el-select>
          <el-button :disabled="!selectedRecording()?.frames.length" @click="playRecording(999)">
            {{ t("terminal.playbackFastForward") }}
          </el-button>
        </footer>
      </section>
    </div>
  </el-dialog>
</template>
