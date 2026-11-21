import assert from "node:assert/strict";
import { readdirSync, readFileSync, existsSync } from "node:fs";
import test from "node:test";

import {
  PLUGIN_DRIVER_SUGGESTIONS,
  pluginDriverSuggestionKey,
  pluginDriverSuggestionKeywords,
  searchPluginDriverSuggestions,
} from "../src/domain/database/plugin-driver-suggestions.ts";

const officialPluginsDirectory = new URL("../../official-plugins/", import.meta.url);

/** Every official plugin manifest that contributes database drivers, read at test time. */
function officialDriverManifests() {
  const manifests = [];
  for (const entry of readdirSync(officialPluginsDirectory, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const file = new URL(`${entry.name}/plugin.json`, officialPluginsDirectory);
    if (!existsSync(file)) continue;
    const manifest = JSON.parse(readFileSync(file, "utf8"));
    const drivers = manifest.contributes?.databaseDrivers;
    if (Array.isArray(drivers) && drivers.length) manifests.push({ manifest, drivers });
  }
  return manifests;
}

// jdbc-bridge drivers that need the vendor jar registered first, with search terms
// the suggestion must answer to (accent-free and Chinese names included).
const JDBC_VENDOR_JAR = {
  cache: ["Caché", "Cache", "InterSystems Caché"],
  sundb: ["SUNDB", "科蓝"],
  oscar: ["神通", "OSCAR", "ShenTong"],
};

const none = () => false;

test("suggestions match official driver manifests", () => {
  const manifests = officialDriverManifests();
  assert.ok(manifests.some(({ manifest }) => manifest.id === "jdbc-bridge"), "jdbc-bridge manifest is read");
  const manifestKeys = new Set();
  for (const { manifest, drivers } of manifests) {
    for (const driver of drivers) {
      const key = `${manifest.id}/${driver.id}`;
      manifestKeys.add(key);
      const matches = PLUGIN_DRIVER_SUGGESTIONS.filter(
        (item) => item.pluginId === manifest.id && item.driverId === driver.id,
      );
      assert.equal(matches.length, 1, `${key} has exactly one suggestion`);
      const [suggestion] = matches;
      assert.equal(suggestion.title, driver.title, `${key} title`);
      assert.deepEqual(suggestion.titleLocalized, driver.titleLocalized, `${key} titleLocalized`);
      assert.equal(suggestion.pluginName, manifest.name, `${key} plugin name`);
      assert.equal(suggestion.replaces, driver.replaces, `${key} replaces`);
      assert.ok(suggestion.keywords.length > 0, `${key} has keywords`);
    }
  }
  // No stale entries: every suggestion names a driver some official manifest declares.
  for (const item of PLUGIN_DRIVER_SUGGESTIONS) {
    assert.ok(manifestKeys.has(`${item.pluginId}/${item.driverId}`), `${item.pluginId}/${item.driverId} is declared`);
  }
  // Only the jdbc-bridge vendor-jar drivers ask for a jar on their card.
  const needsJar = PLUGIN_DRIVER_SUGGESTIONS.filter((entry) => entry.requiresDriverJar);
  assert.deepEqual(needsJar.map((entry) => `${entry.pluginId}/${entry.driverId}`).sort(), Object.keys(JDBC_VENDOR_JAR).map((id) => `jdbc-bridge/${id}`).sort());
  for (const entry of needsJar) {
    for (const keyword of JDBC_VENDOR_JAR[entry.driverId]) assert.ok(entry.keywords.includes(keyword), `${entry.driverId} keyword ${keyword}`);
  }
  assert.equal(PLUGIN_DRIVER_SUGGESTIONS.filter((entry) => entry.pluginId === "jdbc-bridge").length, 10);
  const keys = PLUGIN_DRIVER_SUGGESTIONS.map(pluginDriverSuggestionKey);
  assert.equal(new Set(keys).size, keys.length, "suggestion keys are unique");
});

test("search finds uninstalled plugin databases", () => {
  const keysFor = (query, installed = none) =>
    searchPluginDriverSuggestions(query, installed).map((match) => pluginDriverSuggestionKey(match.suggestion));

  assert.deepEqual(searchPluginDriverSuggestions("", none), []);
  assert.deepEqual(searchPluginDriverSuggestions("   ", none), []);
  assert.ok(keysFor("db2").includes("db2-driver/db2"));
  assert.ok(keysFor("虚谷").includes("xugudb-driver/xugu"));
  assert.ok(keysFor("phoenix").includes("phoenix-driver/phoenix"));
  assert.ok(keysFor("teradata").includes("jdbc-bridge/teradata"));
  assert.ok(keysFor("JDBC 连接").includes("jdbc-bridge/generic"));
  // Vendor names find their own jdbc-bridge driver, with or without the accent.
  for (const [driverId, keywords] of Object.entries(JDBC_VENDOR_JAR)) {
    for (const keyword of keywords) {
      assert.ok(keysFor(keyword).includes(`jdbc-bridge/${driverId}`), `${keyword} finds jdbc-bridge/${driverId}`);
    }
  }
  assert.ok(keysFor("cache").includes("jdbc-bridge/cache"));
  // Installed plugin drivers are already listed as real driver cards: no install suggestion.
  const db2Installed = (pluginId, driverId) => pluginId === "db2-driver" && driverId === "db2";
  assert.ok(!keysFor("db2", db2Installed).includes("db2-driver/db2"));
  // With jdbc-bridge installed its drivers are listed as driver cards: no suggestion.
  const jdbcInstalled = (pluginId) => pluginId === "jdbc-bridge";
  assert.deepEqual(keysFor("神通", jdbcInstalled), []);
  assert.ok(!keysFor("teradata", jdbcInstalled).includes("jdbc-bridge/teradata"));
  assert.deepEqual(keysFor("no-such-database-xyz"), []);
});

test("installed driver cards reuse suggestion keywords", () => {
  assert.ok(pluginDriverSuggestionKeywords("xugudb-driver", "xugu").includes("虚谷"));
  assert.ok(pluginDriverSuggestionKeywords("xugudb-driver", "xugu").includes("虚谷数据库"));
  assert.ok(pluginDriverSuggestionKeywords("jdbc-bridge", "informix").includes("GBase 8s"));
  // Vendor keywords belong to their own driver, not to the generic driver card.
  assert.ok(pluginDriverSuggestionKeywords("jdbc-bridge", "oscar").includes("神通"));
  assert.ok(!pluginDriverSuggestionKeywords("jdbc-bridge", "generic").includes("神通"));
  assert.deepEqual(pluginDriverSuggestionKeywords("third-party", "x"), []);
});
