import { h } from "vue";
import { ElMessageBox } from "element-plus";
import router from "@/router";
import { useVueI18n } from "@/i18n/messages";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import {
  parsePluginDiffOpen,
  parsePluginTerminalOpen,
  parsePluginViewOpen,
  promptEventId,
  type PluginTerminalOpenRequest,
} from "@/domain/plugins/host-ui";
import type { PluginPrompt, PromptAnswer } from "@/domain/plugins/host-prompts";
import { openPluginViewWithRequest } from "@/services/plugin-view-open-requests";
import { listPluginViews } from "@/services/plugin-service";
import { localizedPluginTitle } from "@/services/plugin-titles";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";

/**
 * Acts on the generic host UI requests plugins send (host_ui.go): every
 * plugin goes through the same code, and requests that run something
 * (a terminal command) are shown to the user first.
 */
export function installPluginHostUiBridge(): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  const store = usePluginHostUiStore();
  const stops = [
    listenAppEvent("plugin:view-open", (event) => void openView(event)),
    listenAppEvent("plugin:diff-open", (event) => {
      const diff = parsePluginDiffOpen(event);
      if (diff) store.pushDiff(diff);
    }),
    listenAppEvent("plugin:terminal-open", (event) => {
      const request = parsePluginTerminalOpen(event);
      if (request) void openTerminal(request);
    }),
    listenAppEvent("plugin:prompt", (event) => void showPrompt(event)),
    listenAppEvent<{ id?: string }>("plugin:prompt-cancel", (event) => {
      if (typeof event?.id !== "string") return;
      parsingPrompts.delete(event.id);
      store.removePrompt(event.id);
    }),
  ];
  return () => {
    for (const stop of stops) stop();
  };
}

/** Prompt ids whose event is being parsed; a cancel that arrives meanwhile removes its id. */
const parsingPrompts = new Set<string>();

/** The prompt parser (host-prompts.ts), loaded with the first prompt so it stays out of the entry bundle. */
function loadPromptParser() {
  return import("@/domain/plugins/host-prompts");
}

async function showPrompt(event: unknown) {
  const id = promptEventId(event);
  if (id) parsingPrompts.add(id);
  const prompt = await loadPromptParser().then(({ parsePluginPrompt }) => parsePluginPrompt(event), () => undefined);
  // Cancelled (timeout, plugin stopped) while the parser loaded: nothing to show or answer.
  if (id && !parsingPrompts.delete(id)) return;
  if (!prompt) {
    // Never leave a plugin waiting on a prompt the host cannot show.
    if (id) void invokeBackend("plugin_host_prompt_reply", { id, reply: { cancelled: true } }).catch(() => undefined);
    return;
  }
  // A prompt the host cannot show is answered as cancelled right away.
  if (!usePluginHostUiStore().pushPrompt(prompt)) void answerPrompt(prompt, { cancelled: true });
}

export async function answerPrompt(prompt: PluginPrompt, answer: PromptAnswer) {
  const { promptReply } = await loadPromptParser();
  const reply = promptReply(prompt, answer);
  if (!reply) return false;
  usePluginHostUiStore().removePrompt(prompt.id);
  // The prompt may already be closed (timeout, plugin stopped); nothing to do then.
  await invokeBackend("plugin_host_prompt_reply", reply).catch(() => undefined);
  return true;
}

async function openView(event: unknown) {
  const request = parsePluginViewOpen(event);
  if (!request) return;
  const views = await listPluginViews().catch(() => []);
  const view = views.find((item) => item.pluginId === request.targetPluginId && item.id === request.viewId);
  if (!view) return;
  const title = localizedPluginTitle(view);
  if (!request.entry) {
    openPluginViewTab(useWorkbenchStore(), router, request.targetPluginId, request.viewId, title);
    return;
  }
  await openPluginViewWithRequest(request.targetPluginId, request.viewId, {
    source: "plugin",
    from: request.pluginId,
    entry: request.entry,
    ...(request.params ? { params: request.params } : {}),
  }, title);
}

async function openTerminal(request: PluginTerminalOpenRequest) {
  const { t } = useVueI18n();
  let command = request.command;
  if (command) {
    try {
      const body = t("pluginHostUi.terminalConfirmBody", { plugin: request.pluginName, command, cwd: request.cwd || t("pluginHostUi.terminalDefaultCwd") });
      await ElMessageBox.confirm(
        h("div", { style: "white-space: pre-wrap; overflow-wrap: anywhere" }, body),
        t("pluginHostUi.terminalConfirmTitle"),
        // No default focus: an Enter meant for the editor must not run the command.
        { type: "warning", autofocus: false, distinguishCancelAndClose: true, confirmButtonText: t("pluginHostUi.terminalRun"), cancelButtonText: t("pluginHostUi.terminalOpenOnly") },
      );
    } catch (action) {
      if (action !== "cancel") return;
      command = undefined;
    }
  }
  const workbench = useWorkbenchStore();
  const tab = workbench.createTab("terminal", request.title || request.pluginName);
  if (!tab) return;
  useTerminalWorkspaceStore().activateTab(tab.id, {
    ...(request.cwd ? { initialCwd: request.cwd } : {}),
    ...(command ? { initialCommand: command } : {}),
  });
  await router.push({ name: "terminal" });
}
