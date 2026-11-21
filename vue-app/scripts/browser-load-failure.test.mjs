import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const viewSource = await readFile(
  new URL("../src/views/BrowserWorkspaceView.vue", import.meta.url),
  "utf8",
);
const zhLocale = await readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
const enLocale = await readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");
const browserStoreSource = await readFile(
  new URL("../src/stores/browser-workspace.ts", import.meta.url),
  "utf8",
);
const markdownLinkServiceSource = await readFile(
  new URL("../src/services/markdown-link-navigation.ts", import.meta.url),
  "utf8",
);
const appSource = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");

// Extract the body of ensureTabMounted so assertions stay scoped to that function.
function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.ok(start >= 0, `missing ${signature}`);
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    else if (source[index] === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(start, index + 1);
    }
  }
  throw new Error(`unbalanced braces in ${signature}`);
}

test("embedded tab mount keeps the tab when the page fails to become ready", () => {
  const body = functionBody(viewSource, "async function ensureTabMounted");

  // The ready-wait must be guarded: a dead link should not propagate the raw
  // "browser navigation did not become ready before timeout" error to callers
  // (which used to surface as a scary toast) — the tab stays open instead.
  assert.match(body, /waitEmbeddedBrowserReady\([\s\S]*?\)\s*;/);
  assert.match(body, /try\s*\{[\s\S]*?waitEmbeddedBrowserReady[\s\S]*?\}\s*catch/);
  // Failure keeps the tab: tear down the native overlay, record the error on
  // the tab so the workspace renders the failure panel, and return instead of
  // rethrowing. Open-request waiters (AI callers) still get the failure.
  assert.match(body, /tab\.mounted = false;/);
  assert.match(body, /tab\.loadError = /);
  assert.match(body, /hideEmbeddedBrowser\(\)/);
  assert.match(body, /finishOpenRequest\(tab, \w+\)/);
  assert.doesNotMatch(body, /renderLoadFailure/);
});

test("workspace renders a load-failure panel with retry and system browser actions", () => {
  assert.match(viewSource, /v-if="activeTab\.loadError"/);
  assert.match(viewSource, /browser\.loadFailedTitle/);
  assert.match(viewSource, /browser\.loadFailedHint/);
  assert.match(viewSource, /retryFailedLoad/);
  assert.match(viewSource, /browser\.loadFailedRetry/);
  assert.match(functionBody(viewSource, "async function retryFailedLoad"), /ensureTabMounted\(/);
});

test("loading tabs keep their requested URL when the native view reports about:blank", () => {
  const syncBody = functionBody(viewSource, "async function syncCurrentUrl");

  assert.match(syncBody, /tab\.loading[\s\S]*?about:blank[\s\S]*?return/);
});

test("user markdown links do not enter the AI browser-request completion channel", () => {
  assert.match(browserStoreSource, /origin: "user" \| "ai"/);
  assert.match(browserStoreSource, /function requestUserOpenTab\(/);
  assert.match(browserStoreSource, /function requestAiOpenTab\(/);
  assert.match(markdownLinkServiceSource, /requestUserOpenTab\(url\)/);
  assert.match(appSource, /requestAiOpenTab\(request\.url, requestId\)/);

  const consumeBody = functionBody(viewSource, "async function consumeRequestedTabs");
  assert.match(consumeBody, /request\.origin === "ai" \? request\.id : undefined/);
});

test("load failure page copy exists in both locales", () => {
  for (const locale of [zhLocale, enLocale]) {
    assert.match(locale, /"browser\.loadFailedTitle"/);
    assert.match(locale, /"browser\.loadFailedHint"/);
    assert.match(locale, /"browser\.loadFailedRetry"/);
  }
});

test("AI tab requests load their handler lazily and still answer when loading fails", () => {
  // browser-ai-tabs pulls in the AI sanitizers; keep it out of the entry chunk.
  assert.doesNotMatch(appSource, /import\s[^;]*from "@\/services\/browser-ai-tabs"/);
  assert.doesNotMatch(browserStoreSource, /from "@\/(services\/ai-tools\/shared|services\/browser-ai-tabs|stores\/browser-page-messages)"/);
  const start = appSource.indexOf('"browser:ai-tab-request"');
  assert.ok(start >= 0);
  const listener = appSource.slice(start, appSource.indexOf("stopPopupState", start));
  assert.match(listener, /import\("@\/services\/browser-ai-tabs"\)[\s\S]*?answerAiTabRequest\(browserWorkspace, request\)/);
  assert.match(listener, /\.catch\([\s\S]*?completeEmbeddedBrowserTabRequest\(\{ requestId, error: message \}\)/);
});

test("embedded tab mount resyncs the native view after the mount resolves", () => {
  const body = functionBody(viewSource, "async function ensureTabMounted");
  // Bounds are measured before the async mount; a resize while it is in flight is
  // dropped by sync() (the tab is not mounted yet), leaving a stale, narrower webview.
  const mounted = body.indexOf("tab.mounted = true;");
  assert.ok(mounted > 0, "the mount marks the tab mounted");
  const resync = body.indexOf("scheduleSync();", mounted);
  assert.ok(resync > mounted, "a sync is scheduled once the tab is mounted");
  assert.ok(resync < body.indexOf("waitEmbeddedBrowserReady", mounted), "before waiting for the page to load");
});
