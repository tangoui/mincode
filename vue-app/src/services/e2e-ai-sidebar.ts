// Loaded only behind the native -tags e2e gate; no messages are sent to a model.
import router from "@/router";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import { loadPersistedValue } from "@/services/persistent-store";
import { AI_CONVERSATION_PREFERENCES_KEY } from "@/stores/ai-conversation-preferences";
import { requestOpenSettings } from "@/services/settings-navigation";
import { messagesZh } from "@/i18n/locales/zh";
import { messagesEn } from "@/i18n/locales/en";
import type { MessageKey } from "@/i18n/messages";

async function until<T>(get: () => T | undefined | false | null, label: string): Promise<T> {
  const deadline = performance.now() + 15000;
  while (performance.now() < deadline) {
    const value = get();
    if (value) return value;
    await new Promise<void>(resolve => setTimeout(resolve, 50));
  }
  throw new Error(`AI sidebar smoke timed out: ${label}`);
}
const frames = async () => { for (let i = 0; i < 3; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve())); };
function labelled(root: ParentNode, key: MessageKey): HTMLElement | undefined {
  return [...root.querySelectorAll<HTMLElement>("button[aria-label]")].find(el =>
    [messagesZh[key], messagesEn[key]].includes(el.getAttribute("aria-label") ?? ""),
  );
}
export async function aiSidebarScenario(log: { done(step: string): void }) {
  const assistant = useAiAssistantStore();
  const preferences = useAiConversationPreferencesStore();
  await assistant.hydrate();
  await preferences.setSidebarPosition("right");
  assistant.setPlacement("tab");
  assistant.show();
  await router.push({ name: "ai" });
  const body = await until(() => document.querySelector<HTMLElement>(".is-tab-surface .ai-assistant-body"), "AI tab");
  const sidebar = await until(() => body.querySelector<HTMLElement>(".ai-conversation-sidebar"), "conversation list");
  const content = await until(() => body.querySelector<HTMLElement>(".ai-assistant-content"), "chat content");
  const textarea = await until(() => content.querySelector<HTMLTextAreaElement>("textarea"), "composer");
  textarea.value = "AI sidebar smoke unsent draft";
  textarea.dispatchEvent(new Event("input", { bubbles: true }));
  await frames();
  const onLeft = () => sidebar.getBoundingClientRect().x < content.getBoundingClientRect().x;
  if (onLeft()) throw new Error("explicit right position was not applied");
  const moveLeft = await until(() => labelled(document, "ai.conversation.moveListLeft"), "header move-left");
  moveLeft.click();
  await until(onLeft, "list moved left");
  if (content.querySelector("textarea") !== textarea || textarea.value !== "AI sidebar smoke unsent draft") throw new Error("switch remounted or cleared composer");
  log.done("header moved list right-to-left without remounting or losing unsent draft");
  const before = sidebar.getBoundingClientRect().width;
  const dragger = await until(() => body.querySelector<HTMLElement>(".el-splitter-bar__dragger"), "dragger");
  const rect = dragger.getBoundingClientRect();
  const x = rect.x + rect.width / 2, y = rect.y + rect.height / 2;
  dragger.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, clientX: x, clientY: y }));
  window.dispatchEvent(new MouseEvent("mousemove", { clientX: x + 40, clientY: y }));
  window.dispatchEvent(new MouseEvent("mouseup"));
  await until(() => sidebar.getBoundingClientRect().width > before + 20, "left sidebar resize");
  const collapse = await until(() => labelled(document, "ai.conversation.collapse"), "collapse list");
  collapse.click();
  await until(() => !body.querySelector(".ai-conversation-sidebar"), "collapsed list");
  const expand = await until(() => labelled(document, "ai.conversation.expand"), "expand list");
  expand.click();
  const restored = await until(() => body.querySelector<HTMLElement>(".ai-conversation-sidebar"), "expanded list");
  await until(() => restored.getBoundingClientRect().width > before + 20, "restored width");
  log.done("left sidebar drag, collapse and expand retained adjusted width");
  requestOpenSettings("ai");
  // The setting is an ElSegmented: its radio inputs carry no value attribute,
  // so find the group by its aria-label and the items by their label text.
  const segmented = await until(() => [...document.querySelectorAll<HTMLElement>(".settings-dialog .el-segmented")]
    .find(el => [messagesZh["settings.ai.sidebarPosition"], messagesEn["settings.ai.sidebarPosition"]].includes(el.getAttribute("aria-label") ?? "")), "settings sidebar position");
  const choice = (key: MessageKey) => [...segmented.querySelectorAll<HTMLElement>(".el-segmented__item")]
    .find(el => [messagesZh[key], messagesEn[key]].includes(el.textContent?.trim() ?? ""));
  const radio = await until(() => choice("settings.ai.sidebarPositionRight"), "settings right choice");
  if (!choice("settings.ai.sidebarPositionLeft")?.classList.contains("is-selected")) throw new Error("settings and header out of sync");
  radio.click();
  await until(() => preferences.sidebarPosition === "right", "settings moved list right");
  const persisted = await loadPersistedValue<{ sidebarPosition: string }>(AI_CONVERSATION_PREFERENCES_KEY);
  if (persisted?.sidebarPosition !== "right") throw new Error("position was not saved natively");
  const dialog = document.querySelector<HTMLElement>(".settings-dialog");
  const close = dialog && labelled(dialog, "common.close");
  close?.click();
  await frames();
  if (body.classList.contains("has-left-conversations")) throw new Error("settings did not update tab layout");
  log.done("settings and header share choice; right position persisted through native storage");
  await preferences.setSidebarPosition("left");
  assistant.setPlacement("drawer");
  await router.push({ name: "dashboard" });
  assistant.show();
  const drawer = await until(() => document.querySelector<HTMLElement>(".ai-assistant-drawer:not(.is-tab-surface) .ai-assistant-body"), "drawer layout");
  if (drawer.classList.contains("has-left-conversations") || labelled(document, "ai.conversation.moveListRight")) throw new Error("tab preference leaked into drawer");
  log.done("right drawer ignored tab-only left preference and hid position toggle");
}
