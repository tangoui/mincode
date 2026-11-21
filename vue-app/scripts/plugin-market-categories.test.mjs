import assert from "node:assert/strict";
import test from "node:test";
import {
  isManifestCategory,
  isPluginCategory,
  manifestCategories,
  mergePluginCatalog,
  pluginCategories,
} from "../src/domain/plugins/catalog.ts";

const marketOnly = ["messaging", "devops", "editor", "ai", "document", "language", "theme", "entertainment"];

test("marketplace display categories extend the eight manifest categories", () => {
  assert.deepEqual([...manifestCategories], ["database", "spreadsheet", "web", "api", "network", "development", "productivity", "other"]);
  for (const category of manifestCategories) assert.ok(pluginCategories.includes(category), category);
  for (const category of marketOnly) {
    assert.equal(isPluginCategory(category), true, category);
    // plugin.json keeps the old list so packages still install on older desktops.
    assert.equal(isManifestCategory(category), false, category);
  }
  assert.equal(pluginCategories.at(-1), "other");
});

test("a cloud listing's curated category wins over the bundled manifest category", () => {
  const official = [{ id: "lang-de", category: "productivity" }];
  const cloud = [{ product_key: "lang-de", category: "language" }, { product_key: "docker-manager", category: "devops" }];
  const entries = mergePluginCatalog([], official, cloud);
  assert.equal(entries.find((entry) => entry.id === "lang-de").category, "language");
  assert.equal(entries.find((entry) => entry.id === "docker-manager").category, "devops");
  assert.equal(mergePluginCatalog([], official, []).find((entry) => entry.id === "lang-de").category, "productivity");
});
