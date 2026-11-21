/**
 * Window events of plugin tasks (contributes.tasks), kept apart from
 * services/plugin-task-service.ts so the tab bar (entry chunk) does not load
 * the task runtime.
 */

/** Asks the tab bar to open a confirmed task in a new terminal tab (detail: `{ task, title }`). */
export const PLUGIN_TASK_OPEN_EVENT = "app:plugin-task-open";
/** Asks the project workspace to show its Tasks tab (detail: `{ detect?: boolean }`). */
export const PLUGIN_TASKS_REVEAL_EVENT = "app:plugin-tasks-reveal";
