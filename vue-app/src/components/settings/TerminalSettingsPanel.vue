<script setup lang="ts">
import { computed } from "vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import {
  isPluginTerminalColorSchemeId,
  TERMINAL_COLOR_SCHEME_SWATCHES,
  terminalColorSchemeTitle,
  type TerminalColorSchemeRecord,
} from "@/domain/themes/terminal-color-schemes";
import { useVueI18n } from "@/i18n/messages";
import { useTerminalColorSchemesStore } from "@/stores/terminal-color-schemes";

interface TerminalSettingsModel {
  shellType: string;
  aiCommand: string;
  scrollback: number;
  cursorBlink: boolean;
  webgl: boolean;
  predictiveEcho: "auto" | "always" | "off";
  terminalAttention: boolean;
  terminalAttentionSystemNotify: boolean;
  wireTransport: "auto" | "binary" | "json";
  adaptiveRender: "auto" | "quality" | "performance";
  cursorStyle: "block" | "underline" | "bar";
  terminalColorScheme: string;
  terminalFontFamily: string;
}

const props = defineProps<{ settings: TerminalSettingsModel }>();
const emit = defineEmits<{ change: [patch: Partial<TerminalSettingsModel>] }>();
const { t, localeChain } = useVueI18n();

// Contributed schemes (contributes.terminalColorSchemes). A stored plugin id
// whose plugin is gone stays selected (the terminal falls back to "default").
const colorSchemes = useTerminalColorSchemesStore();
const unavailableScheme = computed(() => {
  const id = props.settings.terminalColorScheme;
  return isPluginTerminalColorSchemeId(id) && colorSchemes.loaded && !colorSchemes.schemeById(id) ? id : "";
});
function schemeSwatches(scheme: TerminalColorSchemeRecord) {
  return TERMINAL_COLOR_SCHEME_SWATCHES.map((key) => scheme.colors[key]).filter((color): color is string => Boolean(color));
}
</script>

<template>
  <section class="settings-flat-page">
    <div class="settings-section-label">{{ t("settings.terminal") }}</div>
    <div class="settings-flat-row"><strong>{{ t("settings.defaultShell") }}</strong>
      <el-select :model-value="settings.shellType" @update:model-value="emit('change', { shellType: $event as string })">
        <el-option value="system" :label="t('settings.shell.system')" /><el-option value="zsh" label="zsh" />
        <el-option value="bash" label="bash" /><el-option value="fish" label="fish" /><el-option value="pwsh" label="PowerShell" />
      </el-select>
    </div>
    <div class="settings-flat-row"><span class="app-inline-label"><strong>{{ t("settings.ai.defaultCommand") }}</strong><AppHintIcon :text="t('settings.ai.defaultCommandHint')" /></span>
      <el-select :model-value="settings.aiCommand" @update:model-value="emit('change', { aiCommand: $event as string })">
        <el-option value="claude" label="Claude Code" /><el-option value="codex" label="Codex" /><el-option value="gemini" label="Gemini CLI" />
      </el-select>
    </div>
    <div class="settings-slider-row"><strong>{{ t("settings.scrollback") }}</strong>
      <el-slider :model-value="settings.scrollback" :min="1000" :max="100000" :step="1000" @update:model-value="emit('change', { scrollback: $event as number })" />
      <b>{{ settings.scrollback }}</b>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.cursorBlink") }}</strong><el-switch :model-value="settings.cursorBlink" @update:model-value="emit('change', { cursorBlink: $event })" /></div>
    <div class="settings-flat-row"><span class="app-inline-label"><strong>{{ t("settings.terminalAttention") }}</strong><AppHintIcon :text="t('settings.terminalAttentionHint')" /></span><el-switch :model-value="settings.terminalAttention" @update:model-value="emit('change', { terminalAttention: $event as boolean })" /></div>
    <div class="settings-flat-row"><strong>{{ t("settings.terminalAttentionSystemNotify") }}</strong><el-switch :model-value="settings.terminalAttentionSystemNotify" :disabled="!settings.terminalAttention" @update:model-value="emit('change', { terminalAttentionSystemNotify: $event as boolean })" /></div>
    <div class="settings-flat-row"><strong>{{ t("settings.webgl") }}</strong><el-switch :model-value="settings.webgl" @update:model-value="emit('change', { webgl: $event })" /></div>
    <div class="settings-flat-row"><span class="app-inline-label"><strong>{{ t("settings.predictiveEcho") }}</strong><AppHintIcon :text="t('settings.predictiveEchoHint')" /></span>
      <el-select :model-value="settings.predictiveEcho" @update:model-value="emit('change', { predictiveEcho: $event as TerminalSettingsModel['predictiveEcho'] })">
        <el-option value="auto" :label="t('settings.option.auto')" /><el-option value="always" :label="t('settings.predictiveEcho.always')" /><el-option value="off" :label="t('settings.predictiveEcho.off')" />
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.wireTransport") }}</strong>
      <el-select :model-value="settings.wireTransport" @update:model-value="emit('change', { wireTransport: $event as TerminalSettingsModel['wireTransport'] })">
        <el-option value="auto" :label="t('settings.option.auto')" /><el-option value="binary" label="Binary" /><el-option value="json" label="JSON" />
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.adaptiveRender") }}</strong>
      <el-select :model-value="settings.adaptiveRender" @update:model-value="emit('change', { adaptiveRender: $event as TerminalSettingsModel['adaptiveRender'] })">
        <el-option value="auto" :label="t('settings.option.auto')" /><el-option value="quality" :label="t('settings.option.quality')" /><el-option value="performance" :label="t('settings.option.performance')" />
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.colorScheme") }}</strong>
      <el-select :model-value="settings.terminalColorScheme" @update:model-value="emit('change', { terminalColorScheme: $event as string })">
        <el-option value="default" :label="t('settings.option.default')" /><el-option value="dark" :label="t('settings.theme.dark')" /><el-option value="light" :label="t('settings.theme.light')" /><el-option value="solarized" label="Solarized" />
        <el-option-group v-if="colorSchemes.schemes.length" :label="t('pluginContrib.terminalColorSchemes.group')">
          <el-option
            v-for="scheme in colorSchemes.schemes"
            :key="scheme.id"
            :value="scheme.id"
            :label="terminalColorSchemeTitle(scheme, localeChain)"
          >
            <span class="terminal-scheme-option">
              <span class="terminal-scheme-option__swatches" :style="{ background: scheme.colors.background }" aria-hidden="true">
                <i v-for="(color, index) in schemeSwatches(scheme)" :key="index" :style="{ background: color }" />
              </span>
              <span class="terminal-scheme-option__title" dir="auto">{{ terminalColorSchemeTitle(scheme, localeChain) }}</span>
              <small>{{ scheme.pluginName }}</small>
            </span>
          </el-option>
        </el-option-group>
        <el-option
          v-if="unavailableScheme"
          :value="unavailableScheme"
          :label="t('pluginContrib.terminalColorSchemes.unavailableOption', { id: unavailableScheme })"
        />
      </el-select>
    </div>
    <el-alert
      v-if="unavailableScheme"
      class="terminal-scheme-alert"
      type="warning"
      :closable="false"
      show-icon
      :title="t('pluginContrib.terminalColorSchemes.unavailable', { id: unavailableScheme })"
    />
    <div class="settings-flat-row"><strong>{{ t("settings.cursorStyle") }}</strong>
      <el-select :model-value="settings.cursorStyle" @update:model-value="emit('change', { cursorStyle: $event as TerminalSettingsModel['cursorStyle'] })">
        <el-option value="block" :label="t('settings.cursor.block')" /><el-option value="underline" :label="t('settings.cursor.underline')" /><el-option value="bar" :label="t('settings.cursor.bar')" />
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.terminalFont") }}</strong><el-input :model-value="settings.terminalFontFamily" @update:model-value="emit('change', { terminalFontFamily: $event })" /></div>
    <p class="settings-page-note">{{ t("settings.terminalHint") }}</p>
  </section>
</template>

<style scoped>
.terminal-scheme-option {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.terminal-scheme-option__swatches {
  display: inline-flex;
  flex-shrink: 0;
  gap: 2px;
  padding: 3px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
}

.terminal-scheme-option__swatches i {
  width: 8px;
  height: 8px;
  border-radius: 2px;
}

.terminal-scheme-option__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.terminal-scheme-option small {
  margin-left: auto;
  color: var(--app-text-muted);
  font-size: 11px;
}

.terminal-scheme-alert {
  margin: 4px 0 8px;
}
</style>
