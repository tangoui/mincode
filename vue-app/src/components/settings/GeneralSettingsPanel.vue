<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import LanguageSettingRow from "@/components/settings/LanguageSettingRow.vue";
import FileIconThemeSettingRow from "@/components/settings/FileIconThemeSettingRow.vue";
import EditorThemeSettingRow from "@/components/settings/EditorThemeSettingRow.vue";
import type { CustomEditorTheme } from "@/domain/themes/editor-theme-data";
import FormatterSettingsSection from "@/components/settings/FormatterSettingsSection.vue";
import LinterSettingsSection from "@/components/settings/LinterSettingsSection.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { MAX_WORKSPACE_CACHE_LIMIT, MIN_WORKSPACE_CACHE_LIMIT } from "@/domain/workspace/state-scope";
import { MAX_PLUGIN_VIEW_WARM_LIMIT, MIN_PLUGIN_VIEW_WARM_LIMIT } from "@/domain/plugin-ui/warm-pool";
import { sidebarItems } from "@/services/sidebar-items";
import type { SidebarItemId, SidebarVisibility } from "@/services/sidebar-preferences";
import type { UiFontFamily } from "@/services/ui-appearance";
import { pluginThemeTitle, type PluginThemeRecord } from "@/domain/themes/plugin-theme";
import {
  PLUGIN_THEMES_CHANGED_EVENT,
  isPluginUiTheme,
  listPluginThemes,
  unavailablePluginThemeId,
  type UiTheme,
  type UiThemeKind,
} from "@/services/ui-theme";
import {
  RUNTIME_PERFORMANCE_LIMITS,
  RUNTIME_PERFORMANCE_PRESETS,
  type RuntimePerformanceProfile,
  type RuntimePerformanceSettings,
} from "@/domain/performance/runtime-limits";

interface GeneralSettingsModel extends RuntimePerformanceSettings {
  minimizeToTray: boolean;
  autostart: boolean;
  globalShortcutEnabled: boolean;
  uiTheme: UiTheme;
  uiThemeKind?: UiThemeKind;
  uiScale: number;
  uiFontFamily: UiFontFamily;
  workspaceCacheLimit: number;
  pluginViewWarmFrames: number;
  fileIconTheme: string;
  editorColorTheme: string;
  editorCustomTheme: CustomEditorTheme;
}

const props = defineProps<{
  settings: GeneralSettingsModel;
  locale: "zh" | "en";
  sidebarVisibility: SidebarVisibility;
  workspaceStateIsolationEnabled: boolean;
}>();
const emit = defineEmits<{
  change: [patch: Partial<GeneralSettingsModel>];
  "change-locale": [locale: "zh" | "en"];
  "change-sidebar-visibility": [id: SidebarItemId, visible: boolean];
  "change-workspace-state-isolation": [enabled: boolean];
}>();
const { t, localeChain } = useVueI18n();

function performanceProfilePatch(profile: RuntimePerformanceProfile): Partial<GeneralSettingsModel> {
  if (profile === "custom") return { performanceProfile: profile };
  return { performanceProfile: profile, ...RUNTIME_PERFORMANCE_PRESETS[profile] };
}

function customPerformancePatch(patch: Partial<RuntimePerformanceSettings>) {
  emit("change", { performanceProfile: "custom", ...patch });
}

// 0 表示不限制，数值展示统一转为「不限制」文案。

function formatPerformanceSeconds(value: number) {
  return value <= 0 ? t("settings.performanceUnlimited") : `${value}s`;
}
function formatPerformanceMiB(value: number) {
  return value <= 0 ? t("settings.performanceUnlimited") : `${value} MiB`;
}
function formatPerformanceCount(value: number) {
  return value <= 0 ? t("settings.performanceUnlimited") : String(value);
}

const themeOptions: Array<{ value: UiTheme; labelKey: MessageKey }> = [
  { value: "system", labelKey: "settings.theme.system" },
  { value: "light", labelKey: "settings.theme.light" },
  { value: "dark", labelKey: "settings.theme.dark" },
  { value: "graphite", labelKey: "settings.theme.graphite" },
  { value: "midnight", labelKey: "settings.theme.midnight" },
  { value: "paper", labelKey: "settings.theme.paper" },
  { value: "cloud-blue", labelKey: "settings.theme.cloudBlue" },
  { value: "arco", labelKey: "settings.theme.arco" },
  { value: "arco-dark", labelKey: "settings.theme.arcoDark" },
];

// Contributed themes (contributes.themes) refresh live; see services/contributions/themes.ts.
const pluginThemes = ref<PluginThemeRecord[]>(listPluginThemes());
const unavailableTheme = ref(unavailablePluginThemeId());
function refreshPluginThemes() {
  pluginThemes.value = listPluginThemes();
  unavailableTheme.value = unavailablePluginThemeId();
}
window.addEventListener(PLUGIN_THEMES_CHANGED_EVENT, refreshPluginThemes);
onBeforeUnmount(() => window.removeEventListener(PLUGIN_THEMES_CHANGED_EVENT, refreshPluginThemes));

const pluginThemeOptions = computed(() =>
  pluginThemes.value.map((theme) => ({
    value: theme.id,
    label: pluginThemeTitle(theme, localeChain.value),
    plugin: theme.pluginName,
    kind: theme.kind,
    // Validated hex values only (domain/themes/plugin-theme.ts).
    swatch: [theme.colors.surface, theme.colors.accent, theme.colors.text],
  }))
);
const showUnavailableTheme = computed(
  () => isPluginUiTheme(props.settings.uiTheme) && unavailableTheme.value === props.settings.uiTheme
);

function selectTheme(value: UiTheme) {
  const plugin = pluginThemes.value.find((theme) => theme.id === value);
  emit("change", plugin ? { uiTheme: value, uiThemeKind: plugin.kind } : { uiTheme: value });
}
</script>

<template>
  <section class="settings-flat-page">
    <div class="settings-section-label">{{ t("settings.generalSection") }}</div>
    <div class="settings-flat-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.minimizeToTray") }}</strong>
        <AppHintIcon :text="t('settings.minimizeToTrayHint')" />
      </span>
      <el-switch :model-value="settings.minimizeToTray" @update:model-value="emit('change', { minimizeToTray: $event })" />
    </div>
    <div class="settings-flat-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.autostart") }}</strong>
        <AppHintIcon :text="t('settings.autostartHint')" />
      </span>
      <el-switch :model-value="settings.autostart" @update:model-value="emit('change', { autostart: $event })" />
    </div>
    <div class="settings-flat-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.globalShortcut") }}</strong>
        <AppHintIcon :text="t('settings.globalShortcutHint')" />
      </span>
      <el-switch :model-value="settings.globalShortcutEnabled" @update:model-value="emit('change', { globalShortcutEnabled: $event })" />
    </div>
    <div class="settings-flat-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.workspaceStateIsolation") }}</strong>
        <AppHintIcon :text="t('settings.workspaceStateIsolationHint')" />
      </span>
      <el-switch
        :model-value="workspaceStateIsolationEnabled"
        :aria-label="t('settings.workspaceStateIsolation')"
        @update:model-value="emit('change-workspace-state-isolation', $event)"
      />
    </div>
    <div class="settings-divider" />
    <div class="settings-section-label">{{ t("settings.interface") }}</div>
    <div class="settings-flat-row">
      <strong>{{ t("settings.theme") }}</strong>
      <el-select :model-value="settings.uiTheme" @update:model-value="selectTheme($event as UiTheme)">
        <el-option
          v-for="option in themeOptions"
          :key="option.value"
          :value="option.value"
          :label="t(option.labelKey)"
        />
        <el-option
          v-if="showUnavailableTheme"
          :value="settings.uiTheme"
          :label="t('pluginContrib.themes.unavailableOption', { id: settings.uiTheme })"
          disabled
        />
        <el-option-group v-if="pluginThemeOptions.length" :label="t('pluginContrib.themes.group')">
          <el-option
            v-for="option in pluginThemeOptions"
            :key="option.value"
            :value="option.value"
            :label="option.label"
          >
            <span class="app-theme-option">
              <span class="app-theme-option__swatch" aria-hidden="true">
                <span v-for="(color, index) in option.swatch" :key="index" :style="{ backgroundColor: color }" />
              </span>
              <span class="app-theme-option__label">{{ option.label }}</span>
              <span class="app-theme-option__meta">
                {{ option.kind === "dark" ? t("pluginContrib.themes.kindDark") : t("pluginContrib.themes.kindLight") }} · {{ option.plugin }}
              </span>
            </span>
          </el-option>
        </el-option-group>
      </el-select>
    </div>
    <el-alert
      v-if="showUnavailableTheme"
      class="settings-theme-alert"
      type="warning"
      :closable="false"
      show-icon
      :title="t('pluginContrib.themes.unavailable', { id: settings.uiTheme })"
    />
    <LanguageSettingRow :locale="locale" @change-locale="emit('change-locale', $event)" />
    <FileIconThemeSettingRow
      :theme="settings.fileIconTheme"
      @change="emit('change', { fileIconTheme: $event })"
    />
    <EditorThemeSettingRow
      :theme="settings.editorColorTheme"
      :custom="settings.editorCustomTheme"
      @change="emit('change', $event)"
    />
    <FormatterSettingsSection />
    <LinterSettingsSection />
    <div class="settings-slider-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.uiScale") }}</strong>
        <AppHintIcon :text="t('settings.uiScaleHint')" />
      </span>
      <el-slider
        :model-value="settings.uiScale"
        :min="80"
        :max="125"
        :step="5"
        @update:model-value="emit('change', { uiScale: $event as number })"
      />
      <b>{{ settings.uiScale }}%</b>
    </div>
    <div class="settings-flat-row">
      <strong>{{ t("settings.uiFont") }}</strong>
      <el-select :model-value="settings.uiFontFamily" @update:model-value="emit('change', { uiFontFamily: $event as UiFontFamily })">
        <el-option value="system" :label="t('settings.font.system')" />
        <el-option value="sans" label="Inter / Sans" /><el-option value="mono" label="Monospace" />
      </el-select>
    </div>
    <div class="settings-slider-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.workspaceCacheLimit") }}</strong>
        <AppHintIcon :text="t('settings.workspaceCacheLimitHint')" />
      </span>
      <el-slider
        :model-value="settings.workspaceCacheLimit"
        :min="MIN_WORKSPACE_CACHE_LIMIT"
        :max="MAX_WORKSPACE_CACHE_LIMIT"
        :step="1"
        :aria-label="t('settings.workspaceCacheLimit')"
        @update:model-value="emit('change', { workspaceCacheLimit: $event as number })"
      />
      <b>{{ settings.workspaceCacheLimit }}</b>
    </div>
    <div class="settings-slider-row">
      <span class="app-inline-label">
        <strong>{{ t("settings.pluginViewWarmLimit") }}</strong>
        <AppHintIcon :text="t('settings.pluginViewWarmLimitHint')" />
      </span>
      <el-slider
        :model-value="settings.pluginViewWarmFrames"
        :min="MIN_PLUGIN_VIEW_WARM_LIMIT"
        :max="MAX_PLUGIN_VIEW_WARM_LIMIT"
        :step="1"
        :aria-label="t('settings.pluginViewWarmLimit')"
        @update:model-value="emit('change', { pluginViewWarmFrames: $event as number })"
      />
      <b>{{ settings.pluginViewWarmFrames > 0 ? settings.pluginViewWarmFrames : t("settings.pluginViewWarmOff") }}</b>
    </div>

    <div class="settings-divider" />
    <div class="settings-section-label">
      <span class="app-inline-label">
        {{ t("settings.performanceMemory") }}
        <AppHintIcon :text="t('settings.performanceMemoryHint')" />
      </span>
    </div>
    <div class="settings-flat-row">
      <strong>{{ t("settings.performanceProfile") }}</strong>
      <el-select
        :model-value="settings.performanceProfile"
        @update:model-value="emit('change', performanceProfilePatch($event as RuntimePerformanceProfile))"
      >
        <el-option value="low-memory" :label="t('settings.performanceProfile.lowMemory')" />
        <el-option value="balanced" :label="t('settings.performanceProfile.balanced')" />
        <el-option value="high-performance" :label="t('settings.performanceProfile.highPerformance')" />
        <el-option value="custom" :label="t('settings.performanceProfile.custom')" />
      </el-select>
    </div>
    <template v-if="settings.performanceProfile === 'custom'">
      <div class="settings-slider-row">
        <strong>{{ t("settings.lspIdleStop") }}</strong>
        <el-slider
          :model-value="settings.lspIdleStopSeconds"
          :min="RUNTIME_PERFORMANCE_LIMITS.lspIdleStopSeconds.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.lspIdleStopSeconds.max"
          :step="30"
          @update:model-value="customPerformancePatch({ lspIdleStopSeconds: $event as number })"
        />
        <b>{{ formatPerformanceSeconds(settings.lspIdleStopSeconds) }}</b>
      </div>
      <div class="settings-slider-row">
        <strong>{{ t("settings.lspAuxiliaryIdleStop") }}</strong>
        <el-slider
          :model-value="settings.lspAuxiliaryIdleStopSeconds"
          :min="RUNTIME_PERFORMANCE_LIMITS.lspAuxiliaryIdleStopSeconds.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.lspAuxiliaryIdleStopSeconds.max"
          :step="30"
          @update:model-value="customPerformancePatch({ lspAuxiliaryIdleStopSeconds: $event as number })"
        />
        <b>{{ formatPerformanceSeconds(settings.lspAuxiliaryIdleStopSeconds) }}</b>
      </div>
      <div class="settings-slider-row">
        <strong>{{ t("settings.lspBackgroundStop") }}</strong>
        <el-slider
          :model-value="settings.lspBackgroundStopSeconds"
          :min="RUNTIME_PERFORMANCE_LIMITS.lspBackgroundStopSeconds.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.lspBackgroundStopSeconds.max"
          :step="30"
          @update:model-value="customPerformancePatch({ lspBackgroundStopSeconds: $event as number })"
        />
        <b>{{ formatPerformanceSeconds(settings.lspBackgroundStopSeconds) }}</b>
      </div>
      <div class="settings-slider-row">
        <strong>{{ t("settings.aiInactiveCacheCount") }}</strong>
        <el-slider
          :model-value="settings.aiInactiveWorkspaceCacheCount"
          :min="RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheCount.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheCount.max"
          :step="1"
          @update:model-value="customPerformancePatch({ aiInactiveWorkspaceCacheCount: $event as number })"
        />
        <b>{{ formatPerformanceCount(settings.aiInactiveWorkspaceCacheCount) }}</b>
      </div>
      <div class="settings-slider-row">
        <strong>{{ t("settings.aiInactiveCacheSize") }}</strong>
        <el-slider
          :model-value="settings.aiInactiveWorkspaceCacheMiB"
          :min="RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheMiB.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheMiB.max"
          :step="1"
          @update:model-value="customPerformancePatch({ aiInactiveWorkspaceCacheMiB: $event as number })"
        />
        <b>{{ formatPerformanceMiB(settings.aiInactiveWorkspaceCacheMiB) }}</b>
      </div>
      <div class="settings-slider-row">
        <strong>{{ t("settings.terminalLayoutCache") }}</strong>
        <el-slider
          :model-value="settings.terminalLayoutCacheLimit"
          :min="RUNTIME_PERFORMANCE_LIMITS.terminalLayoutCacheLimit.min"
          :max="RUNTIME_PERFORMANCE_LIMITS.terminalLayoutCacheLimit.max"
          :step="10"
          @update:model-value="customPerformancePatch({ terminalLayoutCacheLimit: $event as number })"
        />
        <b>{{ formatPerformanceCount(settings.terminalLayoutCacheLimit) }}</b>
      </div>
    </template>
    <div class="settings-divider" />
    <div class="settings-section-label">
      <span class="app-inline-label">
        {{ t("settings.sidebarMenu") }}
        <AppHintIcon :text="t('settings.sidebarMenuHint')" />
      </span>
    </div>
    <div class="settings-sidebar-menu-grid">
      <el-card
        v-for="item in sidebarItems"
        :key="item.id"
        class="settings-sidebar-menu-card"
        :class="{ 'is-visible': sidebarVisibility[item.id] }"
        shadow="never"
      >
        <div class="settings-sidebar-menu-card__content">
          <span class="settings-sidebar-menu-card__identity">
            <el-icon aria-hidden="true"><component :is="item.icon" /></el-icon>
            <strong>{{ t(item.labelKey) }}</strong>
          </span>
          <el-switch
            :model-value="sidebarVisibility[item.id]"
            :aria-label="t('settings.sidebarMenuItemAria', { name: t(item.labelKey) })"
            @update:model-value="emit('change-sidebar-visibility', item.id, $event)"
          />
        </div>
      </el-card>
    </div>
  </section>
</template>
