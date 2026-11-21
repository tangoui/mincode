<script setup lang="ts">
/**
 * Project sidebar "Tasks" tab: plugin tasks (contributes.tasks) of the open
 * project. Static tasks appear when their `when.files` exist; detected tasks
 * appear after the user runs a provider's detection. Running always asks the
 * host to resolve the task and, on the first run in this project (or after
 * any change), shows the exact command line for confirmation.
 */
import { computed, onBeforeUnmount, watch } from "vue";
import { ElMessage } from "element-plus";
import { Refresh, Search, VideoPlay } from "@element-plus/icons-vue";
import { groupPluginTasks, pluginTaskIdentity, pluginTaskTitle, type PluginTaskItem } from "@/domain/tasks/plugin-tasks";
import { formatTerminalArgv } from "@/domain/terminal/profiles";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import { pluginTaskErrorMessage, runPluginTask } from "@/services/plugin-task-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { usePluginTasksStore } from "@/stores/plugin-tasks";

const props = defineProps<{ projectRoot: string }>();

const { t, localeChain } = useVueI18n();
const store = usePluginTasksStore();
const desktop = isDesktopRuntime();

const groups = computed(() => groupPluginTasks(store.list?.tasks ?? [], localeChain.value));
const providers = computed(() => store.list?.providers ?? []);
const empty = computed(() => !store.loading && !store.error && groups.value.length === 0 && providers.value.length === 0);

watch(
  [() => props.projectRoot, () => store.contributionRevision],
  () => {
    store.setActiveProjectRoot(props.projectRoot);
    void store.refresh(props.projectRoot);
  },
  { immediate: true },
);

function groupLabel(group: string) {
  return t(`pluginContrib.tasks.group.${group}` as MessageKey);
}

function commandLine(task: PluginTaskItem) {
  return formatTerminalArgv([task.command, ...task.args]);
}

async function run(task: PluginTaskItem) {
  if (!desktop) {
    ElMessage.warning(t("pluginContrib.tasks.desktopOnly"));
    return;
  }
  try {
    await runPluginTask({
      pluginId: task.pluginId,
      taskKey: task.key,
      projectRoot: store.list?.projectRoot || props.projectRoot,
      title: pluginTaskTitle(task, localeChain.value),
    });
  } catch (error) {
    ElMessage.error(t("pluginContrib.tasks.runFailed", { message: pluginTaskErrorMessage(error) }));
    void store.refresh();
  }
}

async function detect(pluginId: string, providerId: string) {
  try {
    const result = await store.detect(pluginId, providerId);
    if (result) ElMessage.success(t("pluginContrib.tasks.detected", { count: result.count }));
  } catch (error) {
    ElMessage.error(t("pluginContrib.tasks.detectFailed", { message: pluginTaskErrorMessage(error) }));
  }
}

function formatTime(value: number) {
  return new Date(value).toLocaleTimeString(localeChain.value[0]);
}

// Per-task palette commands for the open project only.
let commandDisposers: Array<() => void> = [];
function disposeCommands() {
  for (const dispose of commandDisposers) dispose();
  commandDisposers = [];
}
watch(
  () => store.list?.tasks ?? [],
  (tasks) => {
    disposeCommands();
    commandDisposers = tasks.map((task) =>
      registerCommand({
        id: `tasks.run:${pluginTaskIdentity({ pluginId: task.pluginId, taskKey: task.key })}`,
        source: "workspace",
        title: () => t("pluginContrib.tasks.paletteTask", { title: pluginTaskTitle(task, localeChain.value), plugin: task.pluginName }),
        keywords: ["task", task.group, task.pluginId],
        execute: () => run(task),
      }),
    );
  },
  { immediate: true },
);
onBeforeUnmount(disposeCommands);
</script>

<template>
  <section class="project-tasks-panel" :aria-label="t('pluginContrib.tasks.tab')">
    <header class="project-tasks-panel__toolbar">
      <span class="project-tasks-panel__hint">{{ t("pluginContrib.tasks.hint") }}</span>
      <el-tooltip :content="t('pluginContrib.tasks.refresh')" placement="bottom">
        <el-button
          text
          native-type="button"
          :aria-label="t('pluginContrib.tasks.refresh')"
          :loading="store.loading"
          :disabled="!desktop"
          @click="store.refresh()"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
      </el-tooltip>
    </header>

    <el-scrollbar class="project-tasks-panel__body">
      <p v-if="!desktop" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.tasks.desktopOnly") }}</p>
      <el-alert v-else-if="store.error" type="error" :closable="false" show-icon :title="pluginTaskErrorMessage(store.error)" />
      <p v-else-if="store.loading && !store.list" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.tasks.loading") }}</p>
      <p v-else-if="empty" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.tasks.empty") }}</p>

      <template v-if="desktop && !store.error">
        <div v-if="providers.length" class="project-tasks-panel__section">
          <div class="project-tasks-panel__label">{{ t("pluginContrib.tasks.providers") }}</div>
          <div v-for="provider in providers" :key="`${provider.pluginId}/${provider.providerId}`" class="project-tasks-panel__row">
            <div class="project-tasks-panel__text">
              <strong dir="auto">{{ pluginTaskTitle(provider, localeChain) }}</strong>
              <small dir="auto">
                {{ provider.pluginName }} ·
                <template v-if="provider.state === 'ready'">
                  {{ t("pluginContrib.tasks.detectedAt", { time: formatTime(provider.detectedAt ?? 0) }) }}
                  <template v-if="provider.dropped">· {{ t("pluginContrib.tasks.dropped", { count: provider.dropped }) }}</template>
                </template>
                <span v-else-if="provider.state === 'error'" class="is-error">{{ t("pluginContrib.tasks.detectError") }}</span>
                <template v-else>{{ t("pluginContrib.tasks.notDetected") }}</template>
              </small>
            </div>
            <el-tooltip :content="t('pluginContrib.tasks.detect')" placement="left">
              <el-button
                text
                native-type="button"
                :aria-label="t('pluginContrib.tasks.detect')"
                :loading="store.isDetecting(provider.pluginId, provider.providerId)"
                @click="detect(provider.pluginId, provider.providerId)"
              >
                <el-icon><Search /></el-icon>
              </el-button>
            </el-tooltip>
          </div>
        </div>

        <div v-for="entry in groups" :key="entry.group" class="project-tasks-panel__section">
          <div class="project-tasks-panel__label">{{ groupLabel(entry.group) }}</div>
          <div v-for="task in entry.tasks" :key="`${task.pluginId}/${task.key}`" class="project-tasks-panel__row">
            <el-tooltip :content="commandLine(task)" placement="right" :show-after="400">
              <div class="project-tasks-panel__text">
                <strong dir="auto">{{ pluginTaskTitle(task, localeChain) }}</strong>
                <small dir="auto">{{ task.detail || task.pluginName }}</small>
              </div>
            </el-tooltip>
            <el-tooltip :content="t('pluginContrib.tasks.run')" placement="left">
              <el-button text native-type="button" :aria-label="t('pluginContrib.tasks.run')" @click="run(task)">
                <el-icon><VideoPlay /></el-icon>
              </el-button>
            </el-tooltip>
          </div>
        </div>
      </template>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.project-tasks-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.project-tasks-panel__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
}

.project-tasks-panel__hint {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-tasks-panel__body {
  min-height: 0;
  flex: 1;
}

.project-tasks-panel__section {
  padding: 6px 0;
}

.project-tasks-panel__label {
  padding: 2px 10px;
  color: var(--app-text-muted);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
}

.project-tasks-panel__row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 2px 4px 2px 10px;
}

.project-tasks-panel__row:hover {
  background: var(--app-hover);
}

.project-tasks-panel__text {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.project-tasks-panel__text strong,
.project-tasks-panel__text small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-tasks-panel__text strong {
  color: var(--app-text);
  font-size: 12px;
  font-weight: 500;
}

.project-tasks-panel__text small {
  color: var(--app-text-muted);
  font-size: 11px;
}

.project-tasks-panel__text .is-error {
  color: var(--app-danger);
}
</style>
