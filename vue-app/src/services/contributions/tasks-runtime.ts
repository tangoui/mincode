/**
 * Runtime of the `tasks` consumer, loaded only when a snapshot contains task
 * entries (see tasks.ts). Records whether an active process plugin contributes
 * tasks and registers the two palette commands that reveal the project Tasks
 * tab. Registering never starts anything: every run goes through the host by
 * reference and needs the user's confirmation for that project.
 */
import { ElMessage } from "element-plus";
import type { PointContribution } from "@/domain/plugins/contributions";
import { hasTaskContributions, TASK_DETECT_COMMAND_ID, TASK_RUN_COMMAND_ID } from "@/domain/tasks/plugin-tasks";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import { revealPluginTasks } from "@/services/plugin-task-service";
import { usePluginTasksStore } from "@/stores/plugin-tasks";

let applyGeneration = 0;

function reveal(detect: boolean) {
  const store = usePluginTasksStore();
  if (!store.activeProjectRoot) {
    const { t } = useVueI18n();
    ElMessage.info(t("pluginContrib.tasks.noProject"));
    return;
  }
  revealPluginTasks({ detect });
}

export function applyTaskContributions(contributions: PointContribution[]): () => void {
  const store = usePluginTasksStore();
  const { t } = useVueI18n();
  const token = ++applyGeneration;
  const available = hasTaskContributions(contributions);
  store.setAvailable(available);
  const disposers = available
    ? [
        registerCommand({
          id: TASK_RUN_COMMAND_ID,
          source: "workspace",
          title: () => t("pluginContrib.tasks.paletteRun"),
          keywords: ["task", "tasks", "run", "build", "test", "plugin"],
          execute: () => reveal(false),
        }),
        registerCommand({
          id: TASK_DETECT_COMMAND_ID,
          source: "workspace",
          title: () => t("pluginContrib.tasks.paletteDetect"),
          keywords: ["task", "tasks", "detect", "plugin"],
          execute: () => reveal(true),
        }),
      ]
    : [];
  return () => {
    for (const dispose of disposers) dispose();
    // The Tasks tab disappears only when no apply follows (the lazy loader
    // applies the next snapshot a few microtasks later), so a refresh never
    // closes an open Tasks tab.
    setTimeout(() => {
      if (applyGeneration === token) store.setAvailable(false);
    }, 0);
  };
}
