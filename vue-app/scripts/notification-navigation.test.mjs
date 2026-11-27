import {
  desktopNotificationTarget,
  parseDesktopAiConversationTarget,
  readNotificationTarget,
} from "../src/domain/notifications/target.ts";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = async (path) => readFile(new URL(`../${path}`, import.meta.url), "utf8");

test("notification targets are persisted, allowlisted, and activated from the notification card", async () => {
  const [store, center, row, target, navigation] = await Promise.all([
    source("src/stores/notifications.ts"),
    source("src/components/layout/NotificationCenterButton.vue"),
    source("src/components/layout/NotificationListItem.vue"),
    source("src/domain/notifications/target.ts"),
    source("src/services/notification-navigation.ts"),
  ]);

  assert.match(store, /target\?: NotificationTarget/);
  assert.match(store, /target: readNotificationTarget\(value\.target\)/);
  assert.match(center, /const target = item\.target \?\? fallbackTarget\(item\.source\)/);
  assert.match(center, /requestNotificationNavigation\(target\)/);
  // Each card is its own row component (a new notification renders only its row); a click on the
  // card reports the item, and the center activates it.
  assert.match(row, /@click="emit\('activate', props\.item\)"/);
  assert.match(center, /@activate="activateNotification"/);
  assert.match(target, /workspaceViews\.includes/);
  assert.match(target, /kind: "ai-conversation"/);
  assert.match(center, /source === "ai"/);
  assert.match(center, /update: "settings"/);
  assert.match(navigation, /invokeBackend\("desktop_navigate", nativeTarget\)/);
});

test("AI conversation notification targets reach top-level navigation", async () => {
  const [app, notifications] = await Promise.all([
    source("src/App.vue"),
    source("src/services/notifications.ts"),
  ]);

  assert.match(app, /const aiStore = await loadAiAssistantStore\(\);/);
  assert.match(app, /await aiStore\.switchToConversation\(/);
  assert.match(app, /aiStore\.show\(switched \? aiStore\.mode : "agentic"\)/);
  assert.match(app, /parseDesktopAiConversationTarget\(target\.value\)/);
  assert.match(notifications, /desktopNotificationTarget\(input\.target\)/);
});

test("notification target parsing rejects unknown views and malformed desktop payloads", () => {
  assert.deepEqual(readNotificationTarget({ kind: "view", view: "database" }), {
    kind: "view",
    view: "database",
  });
  assert.equal(readNotificationTarget({ kind: "view", view: "shell" }), undefined);
  assert.equal(readNotificationTarget({ kind: "ai-conversation" }), undefined);
  assert.equal(parseDesktopAiConversationTarget("not-json"), undefined);

  const target = {
    kind: "ai-conversation",
    conversationId: "conversation-1",
    workspaceScopeId: "workspace:one",
  };
  const desktopTarget = desktopNotificationTarget(target);
  assert.equal(desktopTarget?.kind, "ai-conversation");
  assert.deepEqual(parseDesktopAiConversationTarget(desktopTarget?.value), target);
});
