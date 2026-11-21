<script setup lang="ts">
/**
 * Records a keybinding for one command. While open the store's `recording`
 * flag pauses the keybinding dispatcher and the double-modifier detector, and
 * keys are taken in the capture phase of the recorder box so no other handler
 * (dialog Esc, Monaco, dispatcher) sees them. Element Plus has no key-recorder
 * control, so the capture box is a focusable element styled as an input.
 *
 * The `global` variant records a plugin global shortcut: one chord saved as a
 * accelerator, without double-modifier or when, analyzed against the
 * host rules and the in-app keys (domain/keybindings/plugin-global-shortcuts.ts).
 */
import "@/styles/workspaces/settings/keybindings.css";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { formatKeySequence, keyChordFromEvent, sequenceId, type KeyboardPlatform, type ModifierName } from "@/domain/keybindings/keys";
import type { KeybindingResolution } from "@/domain/keybindings/resolver";
import {
  analyzeCandidate,
  EMPTY_RECORDER_STATE,
  recordChord,
  recorderSequence,
  type RecorderState,
} from "@/domain/keybindings/settings-model";
import { useKeybindingsStore } from "@/stores/keybindings";

const props = defineProps<{
  modelValue: boolean;
  commandId: string;
  commandTitle: string;
  mode: "change" | "add";
  /** Label of the binding being changed (change mode). */
  currentLabel?: string;
  initialWhen?: string;
  resolution: KeybindingResolution;
  platform: KeyboardPlatform;
  globalShortcutEnabled: boolean;
  doubleModifierEnabled: boolean;
  titleOf: (command: string) => string;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  save: [value: { key: string; when?: string }];
}>();

const { t } = useVueI18n();
const store = useKeybindingsStore();
const captureBox = ref<HTMLElement>();
const recorder = ref<RecorderState>(EMPTY_RECORDER_STATE);
const doubleModifier = ref<ModifierName | "">("");
const when = ref("");

const MODIFIERS: readonly ModifierName[] = ["shift", "ctrl", "alt", "meta"];
const doubleOptions = computed(() =>
  MODIFIERS.map((modifier) => ({
    value: modifier,
    label: t("settings.keybindings.recorder.doubleOption", {
      key: formatKeySequence({ kind: "double", modifier }, props.platform),
    }),
  })),
);

const key = computed(() => {
  if (doubleModifier.value) return `${doubleModifier.value} ${doubleModifier.value}`;
  const sequence = recorderSequence(recorder.value);
  return sequence ? sequenceId(sequence) : "";
});

const recordedLabel = computed(() => {
  if (doubleModifier.value) return formatKeySequence({ kind: "double", modifier: doubleModifier.value }, props.platform);
  const sequence = recorderSequence(recorder.value);
  return sequence ? formatKeySequence(sequence, props.platform) : "";
});

const analysis = computed(() =>
  key.value
    ? analyzeCandidate({
        resolution: props.resolution,
        platform: props.platform,
        command: props.commandId,
        key: key.value,
        when: when.value,
        globalShortcutEnabled: props.globalShortcutEnabled,
      })
    : null,
);

const whenInvalid = computed(() => analysis.value?.error === "when");
const blocked = computed(() => analysis.value?.reserved?.severity === "reserved");
const canSave = computed(() =>
  Boolean(key.value && analysis.value && !analysis.value.error && !blocked.value),
);
const dialogTitle = computed(() => {
  return t(props.mode === "add" ? "settings.keybindings.recorder.titleAdd" : "settings.keybindings.recorder.titleChange");
});

function reasonLabel(id: string) {
  return t(`settings.keybindings.reason.${id}` as MessageKey);
}

function reset() {
  recorder.value = EMPTY_RECORDER_STATE;
  doubleModifier.value = "";
  when.value = props.initialWhen ?? "";
}

function focusCapture() {
  captureBox.value?.focus();
}

function clear() {
  recorder.value = EMPTY_RECORDER_STATE;
  doubleModifier.value = "";
  focusCapture();
}

function close() {
  emit("update:modelValue", false);
}

function save() {
  if (!canSave.value) return;
  const trimmed = when.value.trim();
  emit("save", trimmed ? { key: key.value, when: trimmed } : { key: key.value });
  close();
}

function onCaptureKeydown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229) return;
  const bare = !event.ctrlKey && !event.altKey && !event.metaKey;
  // Bare Tab / Shift+Tab keep moving focus, bare Esc still closes the dialog.
  if (bare && (event.key === "Tab" || (event.key === "Escape" && !event.shiftKey))) return;
  event.preventDefault();
  event.stopPropagation();
  if (event.repeat) return;
  const chord = keyChordFromEvent(event);
  if (!chord) return;
  doubleModifier.value = "";
  const now = performance.now();
  recorder.value = recordChord(recorder.value, chord, now);
}

watch(
  () => props.modelValue,
  (open, previous) => {
    if (open) {
      store.setRecording(true);
      reset();
    } else if (previous) {
      store.setRecording(false);
    }
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  if (props.modelValue) store.setRecording(false);
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    class="app-keybinding-recorder"
    width="min(560px, calc(100vw - 40px))"
    append-to-body
    :title="dialogTitle"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="focusCapture"
  >
    <el-form label-position="top" class="app-keybinding-recorder__form" @submit.prevent>
      <el-form-item :label="t('settings.keybindings.recorder.command')">
        <div class="app-keybinding-recorder__command">
          <strong>{{ commandTitle }}</strong>
          <code>{{ commandId }}</code>
          <span v-if="mode === 'change' && currentLabel" class="app-keybinding-recorder__current">
            {{ t("settings.keybindings.recorder.current") }} <kbd class="app-keybinding-kbd">{{ currentLabel }}</kbd>
          </span>
        </div>
      </el-form-item>
      <el-form-item :label="t('settings.keybindings.recorder.capture')">
        <div
          ref="captureBox"
          class="app-keybinding-recorder__capture"
          :class="{ 'has-value': recordedLabel }"
          tabindex="0"
          role="textbox"
          aria-readonly="true"
          :aria-label="t('settings.keybindings.recorder.captureLabel')"
          aria-describedby="app-keybinding-recorder-hint"
          data-testid="keybinding-recorder-capture"
          @keydown.capture="onCaptureKeydown"
        >
          <kbd v-if="recordedLabel" class="app-keybinding-kbd app-keybinding-kbd--large" aria-live="polite">{{ recordedLabel }}</kbd>
          <span v-else class="app-keybinding-recorder__placeholder">{{ t("settings.keybindings.recorder.placeholder") }}</span>
        </div>
        <p id="app-keybinding-recorder-hint" class="app-keybinding-recorder__hint">
          {{ t("settings.keybindings.recorder.hint") }}
        </p>
      </el-form-item>
      <el-form-item :label="t('settings.keybindings.recorder.double')">
        <el-select
          v-model="doubleModifier"
          clearable
          :placeholder="t('settings.keybindings.recorder.doubleNone')"
          :aria-label="t('settings.keybindings.recorder.double')"
          class="app-keybinding-recorder__double"
        >
          <el-option v-for="option in doubleOptions" :key="option.value" :label="option.label" :value="option.value" />
        </el-select>
        <p v-if="doubleModifier && !doubleModifierEnabled" class="app-keybinding-recorder__hint is-warning">
          {{ t("settings.keybindings.recorder.doubleDisabled") }}
        </p>
      </el-form-item>
      <el-form-item :label="t('settings.keybindings.recorder.when')" :error="whenInvalid ? t('settings.keybindings.recorder.whenInvalid') : ''">
        <el-input
          v-model="when"
          clearable
          :placeholder="t('settings.keybindings.recorder.whenPlaceholder')"
          :aria-label="t('settings.keybindings.recorder.when')"
        />
      </el-form-item>
    </el-form>

    <div class="app-keybinding-recorder__status" aria-live="polite" data-testid="keybinding-recorder-status">
      <el-alert
        v-if="analysis?.error === 'key'"
        type="error"
        :closable="false"
        show-icon
        :title="t('settings.keybindings.recorder.invalidKey')"
      />
      <el-alert
        v-if="analysis?.reserved"
        :type="analysis.reserved.severity === 'reserved' ? 'error' : 'warning'"
        :closable="false"
        show-icon
        :title="t(analysis.reserved.severity === 'reserved' ? 'settings.keybindings.recorder.reserved' : 'settings.keybindings.recorder.webview', { reason: reasonLabel(analysis.reserved.id) })"
      />
      <el-alert
        v-if="analysis?.globalShortcut"
        type="warning"
        :closable="false"
        show-icon
        :title="t('settings.keybindings.recorder.globalShortcut')"
      />
      <el-alert
        v-if="analysis?.conflicts.length"
        type="warning"
        :closable="false"
        show-icon
        :title="t('settings.keybindings.recorder.conflicts', { count: analysis.conflicts.length })"
        data-testid="keybinding-recorder-conflicts"
      >
        <ul class="app-keybinding-recorder__conflicts">
          <li v-for="conflict in analysis.conflicts" :key="`${conflict.command}:${conflict.order}`">
            <span>{{ titleOf(conflict.command) }}</span>
            <kbd class="app-keybinding-kbd">{{ formatKeySequence(conflict.sequence, platform) }}</kbd>
            <code v-if="conflict.when">{{ conflict.when }}</code>
          </li>
        </ul>
      </el-alert>
      <p
        v-if="analysis && !analysis.error && !analysis.conflicts.length && !analysis.reserved && !analysis.globalShortcut"
        class="app-keybinding-recorder__hint is-success"
      >
        {{ t("settings.keybindings.recorder.noConflict") }}
      </p>
    </div>

    <template #footer>
      <el-button :disabled="!key" @click="clear">{{ t("common.clear") }}</el-button>
      <el-button @click="close">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!canSave" data-testid="keybinding-recorder-save" @click="save">
        {{ t("common.save") }}
      </el-button>
    </template>
  </el-dialog>
</template>
