import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  TRAY_ITEMS_MAX,
  TRAY_SYNC_DELAY_MS,
  TRAY_SYNC_RETRY_MS,
  createTraySync,
  handlePluginCommandTarget,
  parseDesktopPluginCommandTarget,
  trayItemsToSync,
  trayPreviewTitles,
} from "../src/domain/plugins/tray.ts";

const source = (path) => readFile(fileURLToPath(new URL(`../src/${path}`, import.meta.url)), "utf8");

function fakeTimers() {
  let now = 0;
  let next = 1;
  const pending = new Map();
  return {
    setTimer(callback, ms) {
      const id = next++;
      pending.set(id, { at: now + ms, callback });
      return id;
    },
    clearTimer(id) {
      pending.delete(id);
    },
    async advance(ms) {
      now += ms;
      for (const [id, timer] of [...pending]) {
        if (timer.at <= now) {
          pending.delete(id);
          timer.callback();
        }
      }
      // Let the serialized invoke chain settle.
      for (let i = 0; i < 5; i++) await Promise.resolve();
    },
  };
}

const commands = [
  { pluginId: "acme", id: "proxy.start", when: "!proxy.running" },
  { pluginId: "acme", id: "proxy.stop", when: "proxy.running" },
  { pluginId: "acme", id: "shot" },
  { pluginId: "beta", id: "open" },
];
const entries = [
  { pluginId: "beta", command: "open" },
  { pluginId: "acme", command: "proxy.start" },
  { pluginId: "acme", command: "proxy.stop" },
  { pluginId: "acme", command: "gone" },
  { pluginId: "ghost", command: "open" },
  { pluginId: "acme", command: "shot" },
];

test("plugin-tray: only registered commands whose when holds", () => {
  assert.deepEqual(trayItemsToSync(entries, commands, { acme: { "proxy.running": false } }, false), [
    { pluginId: "beta", command: "open" },
    { pluginId: "acme", command: "proxy.start" },
    { pluginId: "acme", command: "shot" },
  ]);
  assert.deepEqual(trayItemsToSync(entries, commands, { acme: { "proxy.running": true } }, false), [
    { pluginId: "beta", command: "open" },
    { pluginId: "acme", command: "proxy.stop" },
    { pluginId: "acme", command: "shot" },
  ]);
  // Another plugin's context never satisfies a command's when.
  assert.deepEqual(
    trayItemsToSync([{ pluginId: "acme", command: "proxy.stop" }], commands, { beta: { "proxy.running": true } }, false),
    [],
  );
  // Repeats are sent once; manifest command IDs may contain ':' (proxy:toggle).
  assert.deepEqual(
    trayItemsToSync(
      [
        { pluginId: "beta", command: "open" },
        { pluginId: "beta", command: "open" },
        { pluginId: "beta", command: "proxy:toggle" },
      ],
      [...commands, { pluginId: "beta", id: "proxy:toggle" }],
      {},
      false,
    ),
    [
      { pluginId: "beta", command: "open" },
      { pluginId: "beta", command: "proxy:toggle" },
    ],
  );
  assert.equal(TRAY_ITEMS_MAX, 2048);
});

test("plugin-tray: twenty plugins are all sent so the host picks the first eight by name", () => {
  // 20 plugins × 4 items, in an order unrelated to their names: nothing is cut in order.
  const many = [];
  for (let index = 19; index >= 0; index--) {
    for (let item = 0; item < 4; item++) many.push({ pluginId: `p${String(index).padStart(2, "0")}`, command: `cmd:${item}` });
  }
  const result = trayItemsToSync(many, many.map((entry) => ({ pluginId: entry.pluginId, id: entry.command })), {}, false);
  assert.equal(result.length, 80);
  assert.deepEqual(result, many);
});

test("plugin-tray: locked app syncs an empty set", () => {
  assert.deepEqual(trayItemsToSync(entries, commands, { acme: { "proxy.running": true } }, true), []);
});

test("plugin-tray: sync debounces and dedupes", async () => {
  const timers = fakeTimers();
  const calls = [];
  const sync = createTraySync({
    invoke: async (args) => {
      calls.push(structuredClone(args));
      return { shown: args.items.length, dropped: 0, omitted: 0 };
    },
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  const locale = ["zh-CN", "zh"];
  const one = [{ pluginId: "acme", command: "shot" }];
  const two = [...one, { pluginId: "beta", command: "open" }];
  assert.equal(TRAY_SYNC_DELAY_MS, 200);

  sync.update(locale, one);
  await timers.advance(50);
  sync.update(locale, two);
  await timers.advance(50);
  sync.update(locale, one);
  await timers.advance(199);
  assert.equal(calls.length, 0);
  await timers.advance(1);
  assert.deepEqual(calls, [{ locale: ["zh-CN", "zh"], items: one }]);

  // The same set is not sent again.
  sync.update(locale, [{ pluginId: "acme", command: "shot" }]);
  await timers.advance(200);
  assert.equal(calls.length, 1);

  // A new language changes the labels: sent again.
  sync.update(["en"], one);
  await timers.advance(200);
  assert.deepEqual(calls[1], { locale: ["en"], items: one });

  // resync forgets the last set (a new snapshot): the same set goes out once more.
  sync.resync();
  sync.update(["en"], one);
  await timers.advance(200);
  assert.equal(calls.length, 3);

  // Cleanup sends one empty set; a second cleanup sends nothing.
  sync.clear(["en"]);
  await timers.advance(200);
  assert.deepEqual(calls[3], { locale: ["en"], items: [] });
  sync.clear(["en"]);
  await timers.advance(200);
  assert.equal(calls.length, 4);

  // A cleanup followed by a restart within the delay sends only the new set (no empty flash).
  sync.update(["en"], two);
  await timers.advance(200);
  sync.clear(["en"]);
  sync.resync();
  sync.update(["en"], two);
  await timers.advance(200);
  assert.equal(calls.length, 6);
  assert.deepEqual(calls[5], { locale: ["en"], items: two });
});

test("plugin-tray: a failed sync is retried with backoff", async () => {
  const timers = fakeTimers();
  let failures = 3;
  const calls = [];
  const sync = createTraySync({
    invoke: async (args) => {
      calls.push(args);
      if (failures-- > 0) throw new Error("host down");
      return {};
    },
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  const items = [{ pluginId: "acme", command: "shot" }];
  assert.deepEqual(TRAY_SYNC_RETRY_MS, [1000, 2000]);
  sync.update(["en"], items);
  await timers.advance(200);
  assert.equal(calls.length, 1);
  await timers.advance(999);
  assert.equal(calls.length, 1);
  await timers.advance(1);
  assert.equal(calls.length, 2);
  await timers.advance(2000);
  assert.equal(calls.length, 3);
  // Retries are bounded; the next change sends again (and succeeds).
  await timers.advance(10000);
  assert.equal(calls.length, 3);
  sync.update(["en"], items);
  await timers.advance(200);
  assert.equal(calls.length, 4);
  await timers.advance(10000);
  assert.equal(calls.length, 4);

  // A newer set scheduled during a failed request replaces the retry.
  const newer = [{ pluginId: "beta", command: "open" }];
  failures = 1;
  sync.update(["en"], newer.concat(items));
  await timers.advance(200);
  sync.update(["en"], newer);
  await timers.advance(200);
  await timers.advance(5000);
  assert.deepEqual(calls.slice(5).map((args) => args.items), [newer]);
});

test("plugin-tray: parse plugin-command target", () => {
  assert.deepEqual(parseDesktopPluginCommandTarget('{"pluginId":"acme.proxy","commandId":"proxy.toggle"}'), {
    pluginId: "acme.proxy",
    commandId: "proxy.toggle",
  });
  // Command IDs follow the manifest contribution ID shape (':' allowed); plugin IDs do not.
  assert.deepEqual(parseDesktopPluginCommandTarget('{"pluginId":"acme.proxy","commandId":"proxy:toggle"}'), {
    pluginId: "acme.proxy",
    commandId: "proxy:toggle",
  });
  for (const value of [
    '{"pluginId":"acme:x","commandId":"run"}',
    '{"pluginId":"acme","commandId":":run"}',
    '{"pluginId":"acme","commandId":"run","extra":1}',
    '{"pluginId":"acme","commandId":7}',
    '{"pluginId":1,"commandId":"run"}',
    '{"pluginId":"acme","commandId":"has space"}',
    '{"pluginId":"acme"}',
    '["acme","run"]',
    "null",
    "not json",
    "",
    undefined,
    42,
    { pluginId: "acme", commandId: "run" },
    `{"pluginId":"acme","commandId":"${"a".repeat(129)}"}`,
  ]) {
    assert.equal(parseDesktopPluginCommandTarget(value), undefined, String(value));
  }
});

test("App plugin-command target is ignored while locked", async () => {
  const runs = [];
  const run = (...args) => {
    runs.push(args);
    return Promise.resolve();
  };
  const value = '{"pluginId":"acme","commandId":"shot"}';
  assert.equal(handlePluginCommandTarget(value, { locked: true, run }), false);
  assert.deepEqual(runs, []);
  assert.equal(handlePluginCommandTarget("not json", { locked: false, run }), false);
  assert.deepEqual(runs, []);
  assert.equal(handlePluginCommandTarget(value, { locked: false, run }), true);
  assert.deepEqual(runs, [["tray", "acme", "shot"]]);

  // App.vue loads the dispatcher and the runner on demand and reads the lock at click time.
  const app = await source("App.vue");
  assert.match(app, /target\?\.kind === "plugin-command"/);
  assert.match(app, /import\("@\/domain\/plugins\/tray"\)/);
  assert.match(app, /import\("@\/services\/plugin-shell-commands"\)/);
  assert.match(app, /locked: appLock\.locked/);
  // A chunk that fails to load shows the unavailable toast, never an unhandled rejection.
  const branch = app.slice(app.indexOf('target?.kind === "plugin-command"'), app.indexOf('target?.kind === "ai-conversation"'));
  assert.match(branch, /pluginContrib\.shell\.unavailable/);
  assert.equal(branch.match(/\.catch\(unavailable\)/g)?.length, 2);
});

test("plugin-tray: install preview titles", () => {
  const contributes = {
    commands: [
      { id: "proxy.toggle", title: "Toggle capture", titleLocalized: { zh: "切换抓包" } },
      { id: "shot", title: "Screenshot" },
    ],
    menus: {
      tray: [{ command: "proxy.toggle" }, { command: "shot" }, { command: "missing" }, { nope: true }],
      "editor/context": [{ command: "shot" }],
    },
  };
  assert.deepEqual(trayPreviewTitles(contributes, ["zh"]), ["切换抓包", "Screenshot", "missing"]);
  assert.deepEqual(trayPreviewTitles(contributes, ["en"]), ["Toggle capture", "Screenshot", "missing"]);
  assert.deepEqual(trayPreviewTitles(undefined, ["en"]), []);
  assert.deepEqual(trayPreviewTitles({ menus: { tray: "x" } }, ["en"]), []);
});

test("plugin-tray: the tray runtime stays out of the entry chunk", async () => {
  const menus = await source("services/contributions/menus.ts");
  // Only a dynamic import of the sync service, never a static one.
  assert.match(menus, /import\("@\/services\/plugin-tray"\)/);
  assert.doesNotMatch(menus, /from\s+["']@\/services\/plugin-tray["']/);
  assert.doesNotMatch(menus, /from\s+["']@\/domain\/plugins\/tray["']/);
  const app = await source("App.vue");
  assert.doesNotMatch(app, /from\s+["']@\/domain\/plugins\/tray["']/);
  assert.doesNotMatch(app, /from\s+["']@\/services\/plugin-shell-commands["']/);
  const service = await source("services/plugin-tray.ts");
  assert.match(service, /plugin_host_tray_set/);
  assert.match(service, /isPopupWindow\(\)/);
  assert.match(service, /isDesktopRuntime\(\)/);
});
