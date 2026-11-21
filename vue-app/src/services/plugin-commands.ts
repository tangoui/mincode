/**
 * Registers the `plugin:<pluginId>:<commandId>` commands of enabled process
 * plugins in the shared command registry at startup, so they are available
 * to the command palette, keybindings (including keymap presets that bind
 * plugin commands) and future contribution points (menus, status bar)
 * without opening the palette first.
 *
 * The list is refreshed on plugin state, workspace and startup events and
 * whenever the palette opens; `when` clauses are re-evaluated against the
 * in-memory plugin context on `plugin:context-changed`. Action commands with
 * an `inputSchema` open a host-rendered form (PluginCommandParamsHost.vue).
 * Browser preview has no plugin host and registers nothing.
 *
 * Menu items (contributes.menus, services/contributions/menus.ts) run
 * through `executePluginCommand`, which shares the palette path (view
 * targets, parameter forms, invocation) and wraps the payload with the menu
 * context when the item declares passContext. `commandPalette` menu entries
 * adjust palette visibility and order through
 * `setPluginCommandPaletteOverrides`.
 */
import { shallowRef, watch, effectScope, type Ref } from "vue";
import type { Router } from "vue-router";
import { ElMessage } from "element-plus";
import {
  parseCommandInputForm,
  serializeCommandInputValues,
  type CommandInputForm,
} from "@/domain/commands/input-schema";
import { evaluateWhen } from "@/domain/commands/when";
import { buildMenuInvokePayload, type PluginContextMenuId } from "@/domain/plugins/menus";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import {
  invokePlugin,
  listPluginCommands,
  listPluginContexts,
  type PluginCommand as PluginHostCommand,
  type PluginContextMap,
} from "@/services/plugin-service";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { useWorkbenchStore } from "@/stores/workbench";

/** Menu context attached to a command run from a passContext menu item. */
export interface PluginCommandMenuInvocation {
  menu: PluginContextMenuId;
  context: Record<string, unknown>;
}

/** Pending host-rendered parameter form for a plugin action command. */
export interface PluginCommandParamRequest {
  command: PluginHostCommand;
  title: string;
  form: CommandInputForm;
  invocation?: PluginCommandMenuInvocation;
}

/** commandPalette menu entry of one plugin command (`plugin:<pluginId>:<commandId>`). */
export interface PluginCommandPaletteOverride {
  hidden: boolean;
  group: string;
  order: number;
}

/** The registered plugin commands and plugin contexts, for menu consumers. */
export interface PluginCommandState {
  commands: readonly PluginHostCommand[];
  contexts: PluginContextMap;
}

const REFRESH_DEBOUNCE_MS = 200;

const paramRequest = shallowRef<PluginCommandParamRequest | null>(null);
let router: Router | undefined;
let running = false;
let disposers: Array<() => void> = [];
let lastCommands: PluginHostCommand[] = [];
let contexts: PluginContextMap = {};
let refreshSequence = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
let paletteOverrides: ReadonlyMap<string, PluginCommandPaletteOverride> = new Map();
const commandState = shallowRef<PluginCommandState>({ commands: [], contexts: {} });

/** Reactive view of the current plugin commands and contexts (empty in browser preview). */
export const pluginCommandState: Readonly<Ref<PluginCommandState>> = commandState;

/** The parameter form waiting for the user, if any (rendered by PluginCommandParamsHost.vue). */
export const pluginCommandParamRequest: Readonly<Ref<PluginCommandParamRequest | null>> = paramRequest;

/**
 * The command's own title for `locale` (useVueI18n().localeChain). Language
 * packs never apply: this title reaches parameter forms, confirmations and
 * completion messages. The palette row uses the pack-aware title of plugin-titles.ts.
 */
function commandTitle(command: PluginHostCommand, locale: readonly string[]) {
  return resolvePluginTitle(command, locale);
}

async function runPluginActionCommand(
  command: PluginHostCommand,
  title: string,
  payload: string,
  invocation?: PluginCommandMenuInvocation,
) {
  if (!command.action) throw new Error("Plugin command has no executable action");
  const body = invocation ? buildMenuInvokePayload(invocation.menu, invocation.context, payload) : payload;
  await invokePlugin(command.pluginId, command.action, body);
  const { t } = useVueI18n();
  ElMessage.success(t("commandPalette.pluginCommandCompleted", { name: title }));
}

function commandKey(command: Pick<PluginHostCommand, "pluginId" | "id">) {
  return `plugin:${command.pluginId}:${command.id}`;
}

function commandWhenHolds(command: PluginHostCommand) {
  return !command.when || evaluateWhen(command.when, contexts[command.pluginId] ?? {});
}

async function executeCommand(command: PluginHostCommand, title: string, invocation?: PluginCommandMenuInvocation) {
  if (command.target?.type === "view") {
    if (!router) throw new Error("Plugin view commands need the application router");
    openPluginViewTab(useWorkbenchStore(), router, command.pluginId, command.target.viewId, title);
    return;
  }
  if (!command.action) throw new Error("Plugin command has no executable action");
  const form = command.inputSchema ? parseCommandInputForm(command.inputSchema) : null;
  if (form) {
    paramRequest.value = { command, title, form, ...(invocation ? { invocation } : {}) };
    return;
  }
  await runPluginActionCommand(command, title, command.payload ?? "", invocation);
}

/** Palette order: commandPalette menu entries by (group, order), then the host order. */
function paletteRank(commands: readonly PluginHostCommand[]) {
  return commands
    .map((command, index) => ({ command, index, override: paletteOverrides.get(commandKey(command)) }))
    .sort(
      (left, right) =>
        Number(!left.override) - Number(!right.override) ||
        (left.override?.group ?? "").localeCompare(right.override?.group ?? "") ||
        (left.override?.order ?? 0) - (right.override?.order ?? 0) ||
        left.index - right.index,
    );
}

function replacePluginCommands(commands: PluginHostCommand[]) {
  lastCommands = commands;
  commandState.value = { commands, contexts };
  for (const dispose of disposers) dispose();
  const { localeChain, t } = useVueI18n();
  const visible = commands.filter(commandWhenHolds);
  disposers = paletteRank(visible).map(({ command, override }, index) => {
    const title = commandTitle(command, localeChain.value);
    return registerCommand({
      id: commandKey(command),
      source: "plugin",
      title,
      subtitle: command.description || t("commandPalette.source.plugin", { name: command.pluginName }),
      keywords: [command.pluginName, command.pluginId, ...(command.keywords ?? [])],
      priority: 5 + index,
      ...(override?.hidden ? { showInPalette: false } : {}),
      execute: () => executeCommand(command, title),
    });
  });
}

/**
 * Runs one plugin command from a host menu. The command must be registered
 * (plugin enabled and effective in this workspace) and its own `when` must
 * hold; activation and the parameter form are those of the palette path (no
 * risk prompt).
 */
export async function executePluginCommand(
  pluginId: string,
  commandId: string,
  invocation?: PluginCommandMenuInvocation,
): Promise<void> {
  const command = lastCommands.find((candidate) => candidate.pluginId === pluginId && candidate.id === commandId);
  if (!command || !commandWhenHolds(command)) throw new Error(`Plugin command ${pluginId}:${commandId} is not available`);
  const { localeChain } = useVueI18n();
  await executeCommand(command, commandTitle(command, localeChain.value), invocation);
}

/**
 * Runs a plugin action command with host-validated input: arguments of a
 * searchProviders result or the payload of a resolved uriHandlers link. The
 * command must be registered, be an action command (not a view target) and
 * have risk "read" — both the declared risk and the effective risk (the
 * higher of the command's and its action's risk; missing reads as not read);
 * its `when` must hold. `payload` wins over `args`; with neither, a command
 * with an inputSchema opens its parameter form.
 */
export async function executePluginCommandWithArgs(
  pluginId: string,
  commandId: string,
  input: { args?: Record<string, string | number | boolean>; payload?: string } = {},
): Promise<void> {
  const command = lastCommands.find((candidate) => candidate.pluginId === pluginId && candidate.id === commandId);
  if (!command || !commandWhenHolds(command) || !command.action || command.target || command.risk !== "read" || command.effectiveRisk !== "read") {
    throw new Error(`Plugin command ${pluginId}:${commandId} is not available`);
  }
  const title = commandTitle(command, useVueI18n().localeChain.value);
  if (typeof input.payload === "string" && input.payload) {
    await runPluginActionCommand(command, title, input.payload);
    return;
  }
  if (input.args && Object.keys(input.args).length) {
    await runPluginActionCommand(command, title, JSON.stringify(input.args));
    return;
  }
  await executeCommand(command, title);
}

/** Applies commandPalette menu entries (hidden, group, order); keyed by `plugin:<pluginId>:<commandId>`. */
export function setPluginCommandPaletteOverrides(overrides: ReadonlyMap<string, PluginCommandPaletteOverride>): void {
  paletteOverrides = new Map(overrides);
  if (running) replacePluginCommands(lastCommands);
}

/** Re-reads plugin commands and contexts; stale responses are ignored and failures keep the last set. */
export async function refreshPluginCommands(): Promise<void> {
  if (!running) return;
  const sequence = ++refreshSequence;
  try {
    const [commands, nextContexts] = await Promise.all([listPluginCommands(), listPluginContexts()]);
    if (sequence !== refreshSequence || !running) return;
    contexts = nextContexts;
    replacePluginCommands(commands);
  } catch {
    // Keep the last known command set on transient failures.
  }
}

function scheduleRefresh(contextsOnly: boolean) {
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    timer = undefined;
    if (!contextsOnly) {
      void refreshPluginCommands();
      return;
    }
    const sequence = ++refreshSequence;
    void listPluginContexts()
      .then((nextContexts) => {
        if (sequence !== refreshSequence || !running) return;
        contexts = nextContexts;
        replacePluginCommands(lastCommands);
      })
      .catch(() => {
        // Keep the last known context snapshot on transient failures.
      });
  }, REFRESH_DEBOUNCE_MS);
}

/** Submits the pending parameter form; errors propagate to the caller. */
export async function confirmPluginCommandParams(values: Record<string, string | number | boolean | undefined>) {
  const request = paramRequest.value;
  if (!request) return;
  paramRequest.value = null;
  const payload = serializeCommandInputValues(request.form, values);
  await runPluginActionCommand(request.command, request.title, payload, request.invocation);
}

export function cancelPluginCommandParams() {
  paramRequest.value = null;
}

/** Starts plugin command registration (desktop runtime only). Returns a stop function. */
export function startPluginCommandSync(appRouter: Router): () => void {
  if (!isDesktopRuntime() || running) return () => undefined;
  running = true;
  router = appRouter;
  const stops = [
    listenAppEvent("plugin:context-changed", () => scheduleRefresh(true)),
    // 插件启用/停用、工作区切换与启动激活都可能改变命令集合，需要整表重新拉取。
    listenAppEvent("plugin:state-changed", () => scheduleRefresh(false)),
    listenAppEvent("plugin:workspace-changed", () => scheduleRefresh(false)),
    listenAppEvent("plugin:startup-completed", () => scheduleRefresh(false)),
  ];
  const scope = effectScope(true);
  const { localeChain } = useVueI18n();
  // Localized titles follow the UI language and the active language pack.
  scope.run(() => watch(localeChain, () => replacePluginCommands(lastCommands)));
  void refreshPluginCommands();
  return () => {
    running = false;
    refreshSequence++;
    if (timer) clearTimeout(timer);
    timer = undefined;
    for (const stop of stops) stop();
    scope.stop();
    for (const dispose of disposers) dispose();
    disposers = [];
    lastCommands = [];
    contexts = {};
    commandState.value = { commands: [], contexts: {} };
    paramRequest.value = null;
    router = undefined;
  };
}
