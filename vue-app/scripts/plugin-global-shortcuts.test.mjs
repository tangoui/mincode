import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parseKeySequence } from "../src/domain/keybindings/keys.ts";
import { hostGlobalShortcutErrorNotice } from "../src/domain/keybindings/plugin-global-shortcut-notices.ts";
import {
  acceleratorPhysical,
  acceleratorsOverlap,
  analyzeGlobalShortcutCandidate,
  chordToAccelerator,
  formatAccelerator,
  GLOBAL_SHORTCUT_IN_APP_KEYS_MAX,
  GLOBAL_SHORTCUT_LIMITS,
  GLOBAL_SHORTCUT_RECORDER_REGISTERED_HINT_KEY,
  globalShortcutAppStandard,
  globalShortcutInAppKeys,
  globalShortcutKeyIssue,
  globalShortcutNoticeKey,
  globalShortcutsChangedByUpdate,
  globalShortcutPreviewSummary,
  globalShortcutSaveError,
  globalShortcutStateTag,
  groupGlobalShortcuts,
  inAppAccelerators,
  parseGlobalShortcutStatuses,
} from "../src/domain/keybindings/plugin-global-shortcuts.ts";
import { messagesEn } from "../src/i18n/locales/en.ts";
import { messagesZh } from "../src/i18n/locales/zh.ts";

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));

function chord(code, modifiers = {}) {
  return { ctrl: false, shift: false, alt: false, meta: false, ...modifiers, code };
}

function status(overrides = {}) {
  return {
    pluginId: "screenshot",
    pluginName: "Screenshot",
    id: "region",
    title: "Capture region",
    defaultKey: "CmdOrCtrl+Shift+A",
    key: "CmdOrCtrl+Shift+A",
    enabled: false,
    state: "disabled",
    ...overrides,
  };
}

test("limits mirror the globalShortcuts contract", () => {
  const spec = contract.points.globalShortcuts;
  assert.equal(spec.status, "enabled");
  assert.equal(GLOBAL_SHORTCUT_LIMITS.maxEntries, spec.maxEntries);
  assert.equal(GLOBAL_SHORTCUT_LIMITS.titleMaxRunes, spec.limits.titleMaxRunes);
  assert.equal(GLOBAL_SHORTCUT_LIMITS.keyMaxLength, spec.limits.keyMaxLength);
  assert.equal(GLOBAL_SHORTCUT_IN_APP_KEYS_MAX, 512);
});

test("statuses from the backend are validated", () => {
  const parsed = parseGlobalShortcutStatuses([
    status({ titleLocalized: { zh: "框选截图" }, inAppOverlap: true }),
    status({ id: "full", state: "conflict", conflictWith: "mincode", enabled: true }),
    status({ id: "bad", state: "nonsense" }),
    { pluginId: "x" },
    null,
  ]);
  assert.deepEqual(parsed.map((item) => item.id), ["region", "full"]);
  assert.equal(parsed[0].titleLocalized.zh, "框选截图");
  assert.equal(parsed[0].inAppOverlap, true);
  assert.equal(parsed[1].conflictWith, "mincode");
  assert.deepEqual(parseGlobalShortcutStatuses(null), []);
  assert.deepEqual(parseGlobalShortcutStatuses({}), []);
});

test("entries are grouped per plugin in backend order", () => {
  const groups = groupGlobalShortcuts([
    status({ id: "a" }),
    status({ pluginId: "draw", pluginName: "Draw", id: "b" }),
    status({ id: "c" }),
  ]);
  assert.deepEqual(groups.map((group) => [group.pluginId, group.pluginName, group.entries.map((entry) => entry.id)]), [
    ["screenshot", "Screenshot", ["a", "c"]],
    ["draw", "Draw", ["b"]],
  ]);
});

test("state tags cover every backend state and name the conflict partner", () => {
  const all = [
    status({ id: "full", title: "Full screen", state: "enabled", enabled: true }),
    status({ id: "region", state: "conflict", enabled: true, conflictWith: "screenshot/full" }),
    status({ id: "host", state: "conflict", enabled: true, conflictWith: "mincode" }),
    status({ id: "gone", state: "conflict", enabled: true, conflictWith: "removed/x" }),
  ];
  const titleOf = (entry) => entry.title;
  const tag = (entry) => globalShortcutStateTag(entry, all, titleOf);
  assert.deepEqual(tag(all[0]), { key: "settings.keybindings.pluginGlobal.state.enabled", type: "success" });
  assert.deepEqual(tag(status()), { key: "settings.keybindings.pluginGlobal.state.disabled", type: "info" });
  assert.deepEqual(tag(all[1]), { key: "settings.keybindings.pluginGlobal.state.conflict", type: "danger", name: "Screenshot · Full screen" });
  assert.deepEqual(tag(all[2]), { key: "settings.keybindings.pluginGlobal.state.conflict", type: "danger", name: "MinCode" });
  assert.deepEqual(tag(all[3]), { key: "settings.keybindings.pluginGlobal.state.conflict", type: "danger", name: "removed/x" });
  assert.equal(tag(status({ state: "occupied", enabled: true })).key, "settings.keybindings.pluginGlobal.state.occupied");
  assert.equal(tag(status({ state: "occupied", enabled: true })).type, "warning");
  assert.equal(tag(status({ state: "unsupported" })).key, "settings.keybindings.pluginGlobal.state.unsupported");
  assert.equal(tag(status({ state: "inactive", enabled: true })).key, "settings.keybindings.pluginGlobal.state.inactive");
});

test("accelerators map to physical chords like the backend", () => {
  assert.equal(acceleratorPhysical("CmdOrCtrl+Shift+A", "mac"), "Shift+Cmd+a");
  assert.equal(acceleratorPhysical("CmdOrCtrl+Shift+A", "windows"), "Ctrl+Shift+a");
  assert.equal(acceleratorPhysical("Super+Shift+A", "windows"), "Shift+Win+a");
  assert.equal(acceleratorPhysical("Option+Return", "mac"), "Alt+return");
  assert.equal(acceleratorPhysical("Alt+Enter", "mac"), "Alt+return");
  assert.equal(acceleratorPhysical("not a key", "mac"), "");
  assert.ok(acceleratorsOverlap("CmdOrCtrl+Shift+K", "Super+Shift+K", "mac"));
  assert.ok(!acceleratorsOverlap("CmdOrCtrl+Shift+K", "Super+Shift+K", "win"));
  assert.ok(acceleratorsOverlap("Cmd+Shift+K", "Ctrl+Shift+K", "linux"));
});

test("accelerators are displayed with the keybinding formatter", () => {
  assert.equal(formatAccelerator("CmdOrCtrl+Shift+A", "mac"), "⇧⌘A");
  assert.equal(formatAccelerator("CmdOrCtrl+Shift+A", "win"), "Ctrl+Shift+A");
  assert.equal(formatAccelerator("Super+Alt+Page Up", "win"), "Alt+Win+PageUp");
  assert.equal(formatAccelerator("Ctrl+Option+Space", "mac"), "⌃⌥␣");
  assert.equal(formatAccelerator("CmdOrCtrl+Shift+1", "linux"), "Ctrl+Shift+1");
  // Keys the in-app formatter cannot express fall back to the raw text.
  assert.equal(formatAccelerator("CmdOrCtrl+Plus", "mac"), "CmdOrCtrl+Plus");
  assert.equal(formatAccelerator("", "mac"), "");
});

test("recorded chords become accelerators the backend accepts", () => {
  assert.equal(chordToAccelerator(chord("KeyA", { meta: true, shift: true }), "mac"), "CmdOrCtrl+Shift+A");
  assert.equal(chordToAccelerator(chord("KeyA", { ctrl: true, shift: true }), "win"), "CmdOrCtrl+Shift+A");
  assert.equal(chordToAccelerator(chord("KeyA", { ctrl: true, meta: true }), "mac"), "CmdOrCtrl+Ctrl+A");
  assert.equal(chordToAccelerator(chord("Digit1", { meta: true, alt: true }), "linux"), "Alt+Super+1");
  assert.equal(chordToAccelerator(chord("PageDown", { ctrl: true }), "win"), "CmdOrCtrl+Page Down");
  assert.equal(chordToAccelerator(chord("ArrowUp", { alt: true }), "mac"), "Alt+Up");
  assert.equal(chordToAccelerator(chord("Slash", { ctrl: true }), "linux"), "CmdOrCtrl+/");
  assert.equal(chordToAccelerator(chord("Insert", { ctrl: true }), "win"), null);
  // Round trip through the display formatter.
  for (const platform of ["mac", "win", "linux"]) {
    const recorded = chord("KeyK", platform === "mac" ? { meta: true, shift: true } : { ctrl: true, shift: true });
    assert.equal(formatAccelerator(chordToAccelerator(recorded, platform), platform), platform === "mac" ? "⇧⌘K" : "Ctrl+Shift+K");
  }
});

test("key issues mirror the host rules (modifier and reserved combos)", () => {
  assert.equal(globalShortcutKeyIssue("CmdOrCtrl+Shift+A"), undefined);
  assert.equal(globalShortcutKeyIssue("Shift+A"), "modifier");
  assert.equal(globalShortcutKeyIssue("A"), "modifier");
  assert.equal(globalShortcutKeyIssue("CmdOrCtrl+Shift+3"), "reserved");
  assert.equal(globalShortcutKeyIssue("CmdOrCtrl+Q"), "reserved");
  assert.equal(globalShortcutKeyIssue("Alt+F4"), "reserved");
  assert.equal(globalShortcutKeyIssue("Super+L"), "reserved");
  assert.equal(globalShortcutKeyIssue("Ctrl+Alt+Delete"), "reserved");
  assert.equal(globalShortcutKeyIssue("Ctrl+Cmd+Shift+4"), "reserved");
  assert.equal(globalShortcutKeyIssue("CmdOrCtrl+Shift+Q"), "reserved");
  assert.equal(globalShortcutKeyIssue("Cmd+Option+Escape"), "reserved");
  assert.equal(globalShortcutKeyIssue("Super+Shift+S"), "reserved");
  assert.equal(globalShortcutKeyIssue("CmdOrCtrl+Shift+Escape"), "reserved");
  assert.equal(globalShortcutKeyIssue("Ctrl+Shift+S"), undefined);
  assert.equal(globalShortcutKeyIssue("Shift+Shift+A"), "invalid");
  assert.equal(globalShortcutKeyIssue(""), "invalid");
});

test("in-app keys: first chords with a non-Shift modifier, deduplicated and capped", () => {
  const binding = (text, platform) => ({ sequence: parseKeySequence(text, platform) });
  const keys = inAppAccelerators(
    [
      binding("meta+shift+p", "mac"),
      binding("meta+k meta+s", "mac"),
      binding("meta+k meta+t", "mac"),
      binding("shift shift", "mac"),
      binding("f5", "mac"),
      binding("shift+f5", "mac"),
      binding("ctrl+insert", "mac"),
      binding("ctrl+alt+up", "mac"),
    ],
    "mac",
  );
  assert.deepEqual(keys, ["CmdOrCtrl+Shift+P", "CmdOrCtrl+K", "Ctrl+Alt+Up"]);
  const many = Array.from({ length: 700 }, (_, index) => ({
    sequence: { kind: "chords", chords: [chord(`F${(index % 24) + 1}`, { ctrl: true, alt: index >= 24, shift: index >= 48, meta: index % 2 === 0 })] },
  }));
  assert.ok(inAppAccelerators(many, "win").length <= GLOBAL_SHORTCUT_IN_APP_KEYS_MAX);
});

test("candidate analysis flags invalid keys and in-app overlap", () => {
  const inApp = ["CmdOrCtrl+Shift+P"];
  const ok = analyzeGlobalShortcutCandidate({ chord: chord("KeyA", { meta: true, shift: true }), platform: "mac", inAppKeys: inApp });
  assert.deepEqual(ok, { key: "CmdOrCtrl+Shift+A", inAppOverlap: false });
  const overlap = analyzeGlobalShortcutCandidate({ chord: chord("KeyP", { meta: true, shift: true }), platform: "mac", inAppKeys: inApp });
  assert.equal(overlap.inAppOverlap, true);
  // A registered combo never reaches the recorder, so there is no activation-shortcut check.
  assert.equal("activationOverlap" in ok, false);
  assert.equal(analyzeGlobalShortcutCandidate({ chord: chord("KeyA", { shift: true }), platform: "mac", inAppKeys: [] }).issue, "modifier");
  assert.equal(analyzeGlobalShortcutCandidate({ chord: chord("Digit3", { meta: true, shift: true }), platform: "mac", inAppKeys: [] }).issue, "reserved");
  assert.equal(analyzeGlobalShortcutCandidate({ chord: chord("Insert", { ctrl: true }), platform: "win", inAppKeys: [] }).issue, "invalid");
});

test("the recorder shows a localized hint about combos that are already registered", () => {
  assert.equal(GLOBAL_SHORTCUT_RECORDER_REGISTERED_HINT_KEY, "settings.keybindings.pluginGlobal.recorderRegisteredHint");
  assert.equal(messagesZh[GLOBAL_SHORTCUT_RECORDER_REGISTERED_HINT_KEY], "已被系统、MinCode 或其他程序注册的组合可能无法录制；如需使用，请先关闭占用它的快捷键");
  assert.ok(messagesEn[GLOBAL_SHORTCUT_RECORDER_REGISTERED_HINT_KEY]);
  assert.equal(messagesZh["settings.keybindings.pluginGlobal.activationOverlap"], undefined);
});

test("known backend save errors map to localized messages", () => {
  const missing = globalShortcutSaveError('plugin "screenshot" has no global shortcut "region"');
  assert.equal(missing.key, "settings.keybindings.pluginGlobal.error.missing");
  assert.equal(messagesZh[missing.key], "该快捷键已不存在（插件可能已卸载）");
  const unavailable = globalShortcutSaveError("global shortcut settings are not available");
  assert.equal(unavailable.key, "settings.keybindings.pluginGlobal.error.unavailable");
  assert.equal(messagesZh[unavailable.key], "全局快捷键暂不可用");
  const other = globalShortcutSaveError('global shortcut key: "Shift+A" needs at least one modifier other than Shift');
  assert.equal(other.key, "settings.keybindings.pluginGlobal.saveFailed");
  assert.equal(messagesZh[other.key], "保存失败：{error}");
  assert.match(other.error, /needs at least one modifier/);
  for (const result of [missing, unavailable, other]) assert.ok(messagesEn[result.key], result.key);
});

test("preview titles are truncated by code points at the contract limit", () => {
  const long = "😀".repeat(60);
  const [entry] = globalShortcutPreviewSummary({ globalShortcuts: [{ id: "x", title: long, key: "Ctrl+Alt+K" }] });
  assert.equal(Array.from(entry.title).length, GLOBAL_SHORTCUT_LIMITS.titleMaxRunes);
  assert.equal(entry.title, "😀".repeat(GLOBAL_SHORTCUT_LIMITS.titleMaxRunes));
});

test("install preview lists declared shortcuts with their default key", () => {
  assert.deepEqual(
    globalShortcutPreviewSummary({
      globalShortcuts: [
        { id: "full", title: "Full screen", titleLocalized: { zh: "整个屏幕" }, key: "CmdOrCtrl+Shift+1", run: { type: "action", action: "capture" } },
        { id: "region", title: "Region", key: "CmdOrCtrl+Shift+A", run: { type: "action", action: "capture" } },
        { id: "nokey", title: "No key" },
        "junk",
      ],
    }),
    [
      { id: "full", title: "Full screen", titleLocalized: { zh: "整个屏幕" }, key: "CmdOrCtrl+Shift+1" },
      { id: "region", title: "Region", key: "CmdOrCtrl+Shift+A" },
    ],
  );
  assert.deepEqual(globalShortcutPreviewSummary(undefined), []);
  assert.deepEqual(globalShortcutPreviewSummary({ globalShortcuts: {} }), []);
  const many = Array.from({ length: 9 }, (_, index) => ({ id: `s${index}`, title: `S${index}`, key: "Ctrl+Alt+K" }));
  assert.equal(globalShortcutPreviewSummary({ globalShortcuts: many }).length, GLOBAL_SHORTCUT_LIMITS.maxEntries);
});

test("rejection notices map backend codes to localized keys", () => {
  for (const reason of ["disabled", "inactive", "unavailable", "locked", "startFailed", "busy", "failed"]) {
    const key = globalShortcutNoticeKey(`globalShortcut.${reason}`);
    assert.equal(key, `pluginContrib.globalShortcuts.notice.${reason}`);
    assert.ok(messagesZh[key], `zh ${key}`);
    assert.ok(messagesEn[key], `en ${key}`);
  }
  assert.equal(globalShortcutNoticeKey("globalShortcut.other"), undefined);
  assert.equal(globalShortcutNoticeKey("plugin.x"), undefined);
  assert.equal(globalShortcutNoticeKey(undefined), undefined);
});

test("MinCode's own shortcut errors map to localized notices", () => {
  const occupied = hostGlobalShortcutErrorNotice("global-shortcut-occupied: CmdOrCtrl+Shift+Space");
  assert.deepEqual(occupied, { key: "settings.globalShortcutOccupied", params: { key: "CmdOrCtrl+Shift+Space" } });
  // The desktop runtime may wrap the message.
  assert.deepEqual(hostGlobalShortcutErrorNotice("RuntimeError: global-shortcut-occupied: Cmd+K"), {
    key: "settings.globalShortcutOccupied",
    params: { key: "Cmd+K" },
  });
  assert.equal(
    messagesZh["settings.globalShortcutOccupied"],
    "MinCode 唤起快捷键 {key} 已被插件全局快捷键占用，请在 设置 → 快捷键 中关闭占用它的那一条",
  );
  assert.ok(messagesEn["settings.globalShortcutOccupied"].includes("{key}"));
  for (const message of ["global-shortcut-unavailable", "failed to register global shortcut: taken", ""]) {
    assert.deepEqual(hostGlobalShortcutErrorNotice(message), { key: "settings.globalShortcutFailed", params: { error: message } });
  }
  assert.ok(messagesZh["settings.globalShortcutFailed"].includes("{error}"));
  assert.ok(messagesEn["settings.globalShortcutFailed"].includes("{error}"));
});

test("every state tag key exists in both locales", () => {
  for (const state of ["enabled", "disabled", "conflict", "occupied", "unsupported", "inactive"]) {
    const key = globalShortcutStateTag(status({ state, conflictWith: state === "conflict" ? "mincode" : undefined }), [], (entry) => entry.title).key;
    assert.ok(messagesZh[key], `zh ${key}`);
    assert.ok(messagesEn[key], `en ${key}`);
  }
});

test("reserved combos added in the final wave are refused for any key", () => {
  for (const key of ["Ctrl+Space", "Ctrl+Option+Space", "Ctrl+Alt+Space", "Super+Space", "Alt+Tab", "Option+Tab", "Ctrl+Alt+L"]) {
    assert.equal(globalShortcutKeyIssue(key), "reserved", key);
  }
});

test("application-standard combos are allowed with a warning", () => {
  for (const key of ["CmdOrCtrl+C", "CmdOrCtrl+V", "Cmd+S", "Super+W", "CmdOrCtrl+P"]) {
    assert.equal(globalShortcutKeyIssue(key), undefined, key);
    assert.equal(globalShortcutAppStandard(key, "mac"), true, key);
  }
  for (const key of ["CmdOrCtrl+C", "Ctrl+S"]) {
    assert.equal(globalShortcutAppStandard(key, "win"), true, key);
    assert.equal(globalShortcutAppStandard(key, "linux"), true, key);
  }
  for (const key of ["CmdOrCtrl+Shift+C", "CmdOrCtrl+Alt+S", "CmdOrCtrl+B", "Ctrl+Alt+V"]) {
    assert.equal(globalShortcutAppStandard(key, "mac"), false, key);
    assert.equal(globalShortcutAppStandard(key, "win"), false, key);
  }
  const copy = analyzeGlobalShortcutCandidate({ chord: chord("KeyC", { meta: true }), platform: "mac", inAppKeys: [] });
  assert.deepEqual(copy, { key: "CmdOrCtrl+C", inAppOverlap: false, appStandard: true });
  const save = analyzeGlobalShortcutCandidate({ chord: chord("KeyS", { ctrl: true }), platform: "win", inAppKeys: [] });
  assert.equal(save.appStandard, true);
  assert.equal(save.issue, undefined);
  const key = "settings.keybindings.pluginGlobal.appStandard";
  assert.equal(messagesZh[key], "这是应用常用快捷键，开启后会在所有程序中被拦截");
  assert.ok(messagesEn[key]);
});

test("the application-standard warning only checks the current platform", () => {
  // Ctrl+S is Save on Windows / Linux, not on macOS (Cmd+S).
  assert.equal(globalShortcutAppStandard("Ctrl+S", "mac"), false);
  // Super+W is Cmd+W on macOS, but Win+W elsewhere.
  assert.equal(globalShortcutAppStandard("Super+W", "win"), false);
  assert.equal(globalShortcutAppStandard("Super+W", "linux"), false);
  const macCtrlSave = analyzeGlobalShortcutCandidate({ chord: chord("KeyS", { ctrl: true }), platform: "mac", inAppKeys: [] });
  assert.deepEqual(macCtrlSave, { key: "Ctrl+S", inAppOverlap: false });
});

test("the backend's unavailable error is matched exactly", () => {
  assert.equal(globalShortcutSaveError("global shortcut settings are not available").key, "settings.keybindings.pluginGlobal.error.unavailable");
  assert.equal(globalShortcutSaveError("save global shortcut setting: disk not available").key, "settings.keybindings.pluginGlobal.saveFailed");
  assert.equal(globalShortcutSaveError("Error: global shortcut settings are not available").key, "settings.keybindings.pluginGlobal.error.unavailable");
});

test("in-app keys include Monaco's default editor keys", () => {
  const keys = globalShortcutInAppKeys([{ sequence: parseKeySequence("meta+shift+p", "mac") }], "mac");
  assert.ok(keys.includes("CmdOrCtrl+Shift+P"));
  // Monaco: delete line (mod+shift+k) and add selection to next find match (mod+d).
  assert.ok(keys.includes("CmdOrCtrl+Shift+K"), keys.join(", "));
  assert.ok(keys.includes("CmdOrCtrl+D"), keys.join(", "));
  assert.ok(globalShortcutInAppKeys([], "win").includes("CmdOrCtrl+Shift+K"));
  assert.ok(globalShortcutInAppKeys([], "win").length <= GLOBAL_SHORTCUT_IN_APP_KEYS_MAX);
  assert.equal(messagesZh["settings.keybindings.pluginGlobal.inAppOverlap"], "在 MinCode 内按此键会执行插件快捷键，而不是应用内命令");
  assert.ok(messagesEn["settings.keybindings.pluginGlobal.inAppOverlap"]);
});

test("statuses carry the update flag and its note exists in both locales", () => {
  const [changed, plain] = parseGlobalShortcutStatuses([status({ changedByUpdate: true }), status({ id: "full", changedByUpdate: "yes" })]);
  assert.equal(changed.changedByUpdate, true);
  assert.equal(plain.changedByUpdate, undefined);
  const key = "settings.keybindings.pluginGlobal.changedByUpdate";
  assert.equal(messagesZh[key], "插件更新后此快捷键已变更，请确认后重新开启");
  assert.ok(messagesEn[key]);
});

test("the update preview names the enabled shortcuts it turns off", () => {
  const entries = globalShortcutPreviewSummary({
    globalShortcuts: [
      { id: "full", title: "Full screen", key: "CmdOrCtrl+Shift+1" },
      { id: "region", title: "Region", key: "CmdOrCtrl+Shift+B" },
    ],
  });
  assert.deepEqual(globalShortcutsChangedByUpdate(entries, ["region", "gone"]).map((entry) => entry.title), ["Region"]);
  assert.deepEqual(globalShortcutsChangedByUpdate(entries, undefined), []);
  for (const locale of [messagesZh, messagesEn]) assert.ok(locale["pluginContrib.globalShortcuts.previewChangedByUpdate"]);
});

test("the settings UI wires the final-wave notes", async () => {
  const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
  const recorder = await read("../src/components/settings/KeybindingRecorderDialog.vue");
  // Screen readers hear both hints of the global recorder.
  assert.match(recorder, /aria-describedby="isGlobal \? 'app-keybinding-recorder-hint app-keybinding-recorder-registered-hint'/);
  assert.match(recorder, /id="app-keybinding-recorder-registered-hint"/);
  assert.match(recorder, /globalAnalysis\?\.appStandard/);
  const section = await read("../src/components/settings/PluginGlobalShortcutsSection.vue");
  assert.match(section, /globalShortcutInAppKeys\(store\.resolution\.bindings, platform\)/);
  assert.match(section, /row\.changedByUpdate/);
  assert.match(section, /settings\.keybindings\.pluginGlobal\.changedByUpdate/);
  const manager = await read("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /globalShortcutsChangedByUpdate\(previewGlobalShortcuts\.value, preview\.value\?\.globalShortcutsChanged\)/);
  assert.match(manager, /pluginContrib\.globalShortcuts\.previewChangedByUpdate/);
});
