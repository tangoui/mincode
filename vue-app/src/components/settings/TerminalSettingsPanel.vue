<script setup lang="ts">
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import { useVueI18n } from "@/i18n/messages";

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

defineProps<{ settings: TerminalSettingsModel }>();
const emit = defineEmits<{ change: [patch: Partial<TerminalSettingsModel>] }>();
const { t } = useVueI18n();

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
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.cursorStyle") }}</strong>
      <el-select :model-value="settings.cursorStyle" @update:model-value="emit('change', { cursorStyle: $event as TerminalSettingsModel['cursorStyle'] })">
        <el-option value="block" :label="t('settings.cursor.block')" /><el-option value="underline" :label="t('settings.cursor.underline')" /><el-option value="bar" :label="t('settings.cursor.bar')" />
      </el-select>
    </div>
    <div class="settings-flat-row"><strong>{{ t("settings.terminalFont") }}</strong><el-input :model-value="settings.terminalFontFamily" @update:model-value="emit('change', { terminalFontFamily: $event })" /></div>
    <p class="settings-page-note">{{ t("settings.terminalHint") }}</p>
  </section>
</template>
