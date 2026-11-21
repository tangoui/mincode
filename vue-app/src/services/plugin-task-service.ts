/**
 * Plugin tasks (contributes.tasks): the IPC boundary.
 *
 * The WebView never sends a command, args, env or cwd for a task. It names a
 * task only by `(pluginId, taskKey, projectRoot)`; the Go host looks the task
 * up in its own snapshot or detection cache, re-validates it, resolves cwd
 * inside the canonical project root, recomputes the fingerprint and refuses
 * to start until the user confirmed exactly that fingerprint for that project
 * (backend/plugin/tasks_launch.go). The confirmation is required on
 * the first run per project in every approval mode; AI tools have no path to
 * these commands. A task runs in a terminal pane; a restored layout only
 * re-attaches (see `consumePluginTaskLaunchToken`).
 */
import {
  parsePluginTaskList,
  pluginTaskErrorCode,
  type PluginTaskList,
} from "@/domain/tasks/plugin-tasks";
import type { PluginTaskRef } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";
import { confirmPluginLaunch, type ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

import { PLUGIN_TASK_OPEN_EVENT, PLUGIN_TASKS_REVEAL_EVENT } from "@/services/plugin-task-events";

export { PLUGIN_TASK_OPEN_EVENT, PLUGIN_TASKS_REVEAL_EVENT };

export interface PluginTaskLaunch extends ResolvedProcessLaunch {
  taskKey: string;
  projectRoot: string;
}

export interface PluginTaskStartOptions {
  sessionId: string;
  fingerprint: string;
  cols?: number;
  rows?: number;
  /** Re-attach to a live session of the same launch instead of failing. */
  attachIfExists?: boolean;
  /** Only re-attach; the host never starts a process (restored layouts). */
  attachOnly?: boolean;
}

type TaskTarget = Pick<PluginTaskRef, "pluginId" | "taskKey" | "projectRoot">;

const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;
// Tokens of tasks the user just ran in this window. In memory only: a pane
// restored after a restart has no matching token and never starts a process.
const pendingLaunchTokens = new Set<string>();

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: plugin tasks need the MinCode desktop app");
}

function parseLaunch(raw: unknown, operation: string): PluginTaskLaunch {
  const record = typeof raw === "object" && raw !== null && !Array.isArray(raw) ? (raw as Record<string, unknown>) : undefined;
  const valid =
    record &&
    typeof record.pluginId === "string" &&
    typeof record.taskKey === "string" &&
    typeof record.projectRoot === "string" &&
    typeof record.cwd === "string" &&
    typeof record.fingerprint === "string" &&
    FINGERPRINT_PATTERN.test(record.fingerprint) &&
    Array.isArray(record.argv) &&
    record.argv.length > 0 &&
    record.argv.every((arg) => typeof arg === "string");
  if (!valid) throw new Error(`unavailable: ${operation} returned an invalid launch spec`);
  const env = typeof record.env === "object" && record.env !== null ? (record.env as Record<string, string>) : {};
  return { ...(record as unknown as PluginTaskLaunch), env, trusted: record.trusted === true, previouslyTrusted: record.previouslyTrusted === true };
}

export async function listPluginTasks(projectRoot: string): Promise<PluginTaskList> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_tasks_list", { payload: { projectRoot } });
  const list = parsePluginTaskList(raw);
  if (!list) throw new Error("unavailable: the task list is invalid");
  return list;
}

/** Runs a provider's read-only detection for one project (user initiated only). */
export async function detectPluginTasks(pluginId: string, providerId: string, projectRoot: string) {
  requireDesktop();
  const requestId = `tasks-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  const raw = await invokeBackend<{ count?: unknown; dropped?: unknown }>("plugin_host_tasks_detect", {
    payload: { pluginId, providerId, projectRoot, requestId },
  });
  const count = typeof raw?.count === "number" ? raw.count : 0;
  const dropped = typeof raw?.dropped === "number" ? raw.dropped : 0;
  return { count, dropped };
}

/** Resolves the launch on the host (nothing is started). */
export async function preparePluginTask(target: TaskTarget): Promise<PluginTaskLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_task_prepare", {
    payload: { pluginId: target.pluginId, taskKey: target.taskKey, projectRoot: target.projectRoot },
  });
  return parseLaunch(raw, "prepare");
}

/** Stores the user's confirmation of exactly `fingerprint` for this project. */
export async function trustPluginTask(target: TaskTarget, fingerprint: string): Promise<PluginTaskLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_task_trust", {
    payload: { pluginId: target.pluginId, taskKey: target.taskKey, projectRoot: target.projectRoot, fingerprint },
  });
  return parseLaunch(raw, "trust");
}

/** Starts (or re-attaches to) the PTY session of a confirmed task. */
export async function startPluginTaskSession(target: TaskTarget, options: PluginTaskStartOptions) {
  requireDesktop();
  return invokeBackend<string>("plugin_host_task_start", {
    payload: {
      pluginId: target.pluginId,
      taskKey: target.taskKey,
      projectRoot: target.projectRoot,
      fingerprint: options.fingerprint,
      sessionId: options.sessionId,
      cols: options.cols,
      rows: options.rows,
      attachIfExists: options.attachIfExists ?? false,
      attachOnly: options.attachOnly ?? false,
    },
  });
}

/**
 * Prepares a task and, when the host has no matching confirmation for this
 * project, shows the resolved launch and asks the user. Resolves to a trusted
 * launch or undefined (cancelled). Host errors are rethrown.
 */
export async function ensurePluginTaskConfirmed(target: TaskTarget): Promise<PluginTaskLaunch | undefined> {
  const launch = await preparePluginTask(target);
  if (launch.trusted) return launch;
  if (!(await confirmPluginLaunch(launch, { changed: launch.previouslyTrusted }))) return undefined;
  return trustPluginTask(target, launch.fingerprint);
}

function createLaunchToken() {
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/**
 * Confirms a task and asks the tab bar to run it in a new terminal tab.
 * Resolves true when a tab was requested.
 */
export async function runPluginTask(target: TaskTarget & { title: string }): Promise<boolean> {
  const launch = await ensurePluginTaskConfirmed(target);
  if (!launch) return false;
  const launchToken = createLaunchToken();
  pendingLaunchTokens.add(launchToken);
  const task: PluginTaskRef = {
    pluginId: launch.pluginId,
    taskKey: launch.taskKey,
    projectRoot: launch.projectRoot,
    title: target.title,
    launchToken,
  };
  window.dispatchEvent(new CustomEvent(PLUGIN_TASK_OPEN_EVENT, { detail: { task, title: target.title } }));
  return true;
}

/**
 * True exactly once for a token `runPluginTask` just issued: that pane may
 * start the process. Every other mount (restored layout, remount, reload)
 * only re-attaches to a live session.
 */
export function consumePluginTaskLaunchToken(token: string | undefined): boolean {
  if (!token || !pendingLaunchTokens.has(token)) return false;
  pendingLaunchTokens.delete(token);
  return true;
}

/** Marks an explicit "run again" from a pane (same session id, new process). */
export function issuePluginTaskLaunchToken(): string {
  const token = createLaunchToken();
  pendingLaunchTokens.add(token);
  return token;
}

/** Asks the project workspace to show its Tasks tab, optionally detecting tasks. */
export function revealPluginTasks(options: { detect?: boolean } = {}) {
  window.dispatchEvent(new CustomEvent(PLUGIN_TASKS_REVEAL_EVENT, { detail: { detect: options.detect === true } }));
}

/** Localized message for a host error; falls back to the raw message. */
export function pluginTaskErrorMessage(error: unknown): string {
  const { t } = useVueI18n();
  switch (pluginTaskErrorCode(error)) {
    case "unavailable":
      return t("pluginContrib.tasks.unavailable");
    case "not_found":
      return t("pluginContrib.tasks.notFound");
    case "changed":
    case "untrusted":
      return t("pluginContrib.tasks.changed");
    case "forbidden":
      return t("pluginContrib.tasks.forbidden");
    case "limit":
      return t("pluginContrib.tasks.limit");
    default:
      return error instanceof Error ? error.message : String(error);
  }
}
