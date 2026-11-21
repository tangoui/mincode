import assert from "node:assert/strict";
import test from "node:test";
import {
  parseContributionsChanged,
  parseContributionsSnapshot,
  pointContributions,
  pointContributionsKey,
} from "../src/domain/plugins/contributions.ts";
import { registerKeymapContributions } from "../src/domain/plugins/keymap-contributions.ts";
import { getKeymapPreset } from "../src/domain/keybindings/preset.ts";
import { registerBuiltinKeymapPresets } from "../src/domain/keybindings/presets/index.ts";
import { loadOfficialKeymapPlugins } from "./keymap-plugin-presets.mjs";

const snapshot = {
  revision: 7,
  plugins: [
    {
      pluginId: "keymaps",
      pluginName: "Keymaps",
      version: "1.0.0",
      source: "official",
      runtime: "declarative",
      state: "stopped",
      activeInWorkspace: true,
      points: {
        keymaps: {
          fingerprint: "abc",
          entries: [{ id: "keymaps.x" }],
          issues: [{ point: "keymaps", entry: 1, id: "keymaps.bad", message: "bad rule" }, { message: "" }],
        },
      },
    },
    { pluginId: "", points: {} },
    { pluginId: "no-points" },
    "garbage",
  ],
};

test("snapshot parsing drops malformed plugins, points and issues", () => {
  const parsed = parseContributionsSnapshot(snapshot);
  assert.equal(parsed.revision, 7);
  assert.equal(parsed.plugins.length, 1);
  const point = parsed.plugins[0].points.keymaps;
  assert.equal(point.entries.length, 1);
  assert.deepEqual(point.issues, [{ point: "keymaps", entry: 1, id: "keymaps.bad", message: "bad rule" }]);
  assert.deepEqual(parseContributionsSnapshot(null), { revision: 0, plugins: [] });
  assert.equal(parseContributionsSnapshot({ revision: -1, plugins: [] }).revision, 0);
});

test("change events are validated", () => {
  assert.deepEqual(parseContributionsChanged({ revision: 3, pluginId: " p ", points: ["keymaps", 4] }), {
    revision: 3,
    pluginId: "p",
    points: ["keymaps"],
  });
  assert.deepEqual(parseContributionsChanged("x"), { revision: 0 });
});

test("point key changes only when the point's data changes", () => {
  const parsed = parseContributionsSnapshot(snapshot);
  const key = pointContributionsKey(pointContributions(parsed, "keymaps"));
  assert.equal(pointContributions(parsed, "menus").length, 0);
  const same = parseContributionsSnapshot({ ...snapshot, revision: 8 });
  assert.equal(pointContributionsKey(pointContributions(same, "keymaps")), key);
  const changed = structuredClone(snapshot);
  changed.plugins[0].points.keymaps.fingerprint = "def";
  assert.notEqual(pointContributionsKey(pointContributions(parseContributionsSnapshot(changed), "keymaps")), key);
  const inactive = structuredClone(snapshot);
  inactive.plugins[0].activeInWorkspace = false;
  assert.notEqual(pointContributionsKey(pointContributions(parseContributionsSnapshot(inactive), "keymaps")), key);
});

test("keymap consumer registers official presets, reports issues per stage and disposes", () => {
  registerBuiltinKeymapPresets();
  const plugins = loadOfficialKeymapPlugins();
  assert.ok(plugins.length > 0);
  const contributions = plugins.map((plugin) => ({
    pluginId: plugin.pluginId,
    pluginName: plugin.pluginName,
    version: plugin.manifest.version,
    source: "official",
    runtime: "declarative",
    activeInWorkspace: true,
    point: {
      fingerprint: "f",
      entries: [...plugin.presets, { id: `${plugin.pluginId}.broken`, name: "Broken" }],
      issues: [{ point: "keymaps", entry: 9, id: `${plugin.pluginId}.skipped`, message: "host rejected" }],
    },
  }));
  const result = registerKeymapContributions(contributions);
  const expected = plugins.flatMap((plugin) => plugin.presets.map((preset) => preset.id));
  assert.deepEqual(result.registered, expected);
  for (const id of expected) assert.ok(getKeymapPreset(id), `${id} registered`);
  const host = result.issues.filter((issue) => issue.stage === "host");
  const frontend = result.issues.filter((issue) => issue.stage === "frontend");
  assert.deepEqual(host.map((issue) => issue.preset), plugins.map((plugin) => `${plugin.pluginId}.skipped`));
  assert.deepEqual(frontend.map((issue) => issue.preset), plugins.map((plugin) => `${plugin.pluginId}.broken`));
  assert.ok(frontend.every((issue, index) => issue.entry === plugins[index].presets.length));
  result.dispose();
  for (const id of expected) assert.equal(getKeymapPreset(id), undefined, `${id} unregistered`);
});
