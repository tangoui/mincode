<script setup lang="ts">
/**
 * The editor toolbar's run controls: the project's run configurations, and
 * Run (no debugging), Debug and Stop for the selected one. With no
 * configuration yet, Run and Debug create a "Current File" one from the
 * active file's debugger (offering its plugin when none is installed), or
 * ask for a new one. Desktop only.
 */
import { computed, ref } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { launchConfigurationFor } from "@/services/debug-guidance";
import { canInvoke } from "@/services/desktop-bridge";
import { sameProjectRoot, useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import AppChevron from "@/components/ui/AppChevron.vue";

const props = defineProps<{
  projectRoot: string;
  /** The active project file, for configurations that use `${file}`. */
  activeFile?: string;
  /** Monaco language id of the active file, read when Run / Debug is pressed. */
  activeLanguageId?: () => string | undefined;
}>();

const emit = defineEmits<{
  "edit-configurations": [];
  "new-configuration": [];
}>();

const { t } = useVueI18n();
const configs = useDebugConfigsStore();
const sessions = useDebugSessionsStore();
void configs.load().catch(() => undefined);

const desktop = canInvoke("plugin_host_debugger_start");
const state = computed(() => configs.stateFor(props.projectRoot));
const selected = computed(() => {
  const list = state.value.configs;
  return list.find((item) => item.id === state.value.selectedConfigId) ?? list[0] ?? null;
});


/** The selected session when it belongs to this project and still runs. */
const stoppable = computed(() => {
  const session = sessions.selectedSession;
  return session && session.state !== "ended" && sameProjectRoot(session.projectRoot, props.projectRoot) ? session : null;
});

function handleCommand(command: string) {
  if (command === "edit") emit("edit-configurations");
  else if (command === "new") emit("new-configuration");
  else configs.selectConfig(props.projectRoot, command);
}

/** This toolbar's launch while it picks the configuration; the store tracks it from then on. */
const picking = ref<"run" | "debug" | null>(null);
/**
 * The project's launch in flight (configuration, prepare, confirmation,
 * start), from this toolbar or a keybinding; its button shows loading.
 */
const starting = computed<"run" | "debug" | null>(() => {
  if (picking.value) return picking.value;
  const launch = sessions.launchFor(props.projectRoot);
  return launch ? (launch.noDebug ? "run" : "debug") : null;
});

async function start(noDebug: boolean) {
  if (starting.value) return;
  picking.value = noDebug ? "run" : "debug";
  try {
    const cfg = await launchConfigurationFor(props.projectRoot, props.activeLanguageId?.());
    if (cfg === "new") emit("new-configuration");
    if (!cfg || cfg === "new") return;
    // Failures are reported by the store.
    const started = sessions.start(props.projectRoot, cfg, { noDebug, activeFile: props.activeFile });
    picking.value = null;
    await started;
  } finally {
    picking.value = null;
  }
}

function stop() {
  if (stoppable.value) void sessions.stop(stoppable.value.id);
}

const runLabel = computed(() => (selected.value ? t("debug.run.run", { name: selected.value.name }) : t("debug.run.runNew")));
const debugLabel = computed(() => (selected.value ? t("debug.run.debug", { name: selected.value.name }) : t("debug.run.debugNew")));
const stopLabel = computed(() => (stoppable.value ? t("debug.run.stop", { name: stoppable.value.configName }) : t("debug.run.stopNone")));
</script>

<template>
  <div class="run-config-selector" role="group" :aria-label="t('debug.run.label')">
    <span v-if="!desktop" class="run-config-selector__desktop">{{ t("common.desktopRequired") }}</span>
    <template v-else>
      <el-dropdown trigger="click" placement="bottom-end" :disabled="!configs.loaded" @command="handleCommand">
        <el-button
          text
          native-type="button"
          class="run-config-selector__trigger"
          :aria-label="t('debug.run.select')"
          :title="selected?.name ?? t('debug.run.select')"
        >
          <span class="run-config-selector__name" :class="{ 'is-placeholder': !selected }">
            {{ !configs.loaded ? t("debug.run.loading") : selected?.name ?? t("debug.run.none") }}
          </span>
          <AppChevron direction="down" />
        </el-button>
        <template #dropdown>
          <el-dropdown-menu class="run-config-selector__menu">
            <el-dropdown-item
              v-for="item in state.configs"
              :key="item.id"
              :command="item.id"
              :class="{ 'is-selected': item.id === selected?.id }"
            >
              {{ item.name }}
            </el-dropdown-item>
            <el-dropdown-item v-if="!state.configs.length" disabled>{{ t("debug.run.none") }}</el-dropdown-item>
            <el-dropdown-item divided command="edit">{{ t("debug.run.editConfigurations") }}</el-dropdown-item>
            <el-dropdown-item command="new">{{ t("debug.run.newConfiguration") }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-tooltip :content="runLabel" placement="bottom">
        <el-button
          text
          native-type="button"
          class="run-config-selector__action is-run"
          data-action="run"
          :aria-label="runLabel"
          :disabled="!configs.loaded || (starting !== null && starting !== 'run')"
          :loading="starting === 'run'"
          @click="start(true)"
        >
          <el-icon>
            <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M7 4.5v15l12-7.5z" /></svg>
          </el-icon>
        </el-button>
      </el-tooltip>
      <el-tooltip :content="debugLabel" placement="bottom">
        <el-button
          text
          native-type="button"
          class="run-config-selector__action is-debug"
          data-action="debug"
          :aria-label="debugLabel"
          :disabled="!configs.loaded || (starting !== null && starting !== 'debug')"
          :loading="starting === 'debug'"
          @click="start(false)"
        >
          <el-icon>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
              <rect x="7" y="8" width="10" height="12" rx="5" />
              <path d="M9 8a3 3 0 0 1 6 0M12 12v8M3 13h4M17 13h4M4 7l3 2M20 7l-3 2M4 20l3-2M20 20l-3-2" />
            </svg>
          </el-icon>
        </el-button>
      </el-tooltip>
      <el-tooltip :content="stopLabel" placement="bottom">
        <el-button
          text
          native-type="button"
          class="run-config-selector__action is-stop"
          data-action="stop"
          :aria-label="stopLabel"
          :disabled="!stoppable"
          @click="stop"
        >
          <el-icon>
            <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="1.5" /></svg>
          </el-icon>
        </el-button>
      </el-tooltip>
    </template>
  </div>
</template>
