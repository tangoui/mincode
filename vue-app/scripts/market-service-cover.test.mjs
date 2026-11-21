import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

// market-service pulls in the desktop bridge, which is only resolvable inside the
// desktop bundle. Swap the four imports for local equivalents so the parsing
// rules under test are the real ones from src/services/market-service.ts.
const replacements = [
  [
    `import { isPluginCategory, pluginHomepage, type PluginCategory } from "@/domain/plugins/catalog";`,
    `const pluginCategories = ["database", "spreadsheet", "web", "api", "network", "messaging", "development", "devops", "editor", "ai", "document", "productivity", "language", "theme", "entertainment", "other"];
     const isPluginCategory = (value) => pluginCategories.includes(value);
     const pluginHomepage = (value) => typeof value === "string" && (value.startsWith("https://") || value.startsWith("http://"));`,
  ],
  [
    `import { parsePluginInstallPreview, type PluginInstallPreview } from "@/domain/plugins/install-preview";`,
    `const parsePluginInstallPreview = () => { throw new Error("unused in this test"); };`,
  ],
  [
    `import { PLUGIN_ICONS } from "@/domain/plugin-icons";`,
    `const PLUGIN_ICONS = ["setting"];`,
  ],
  [
    `import { invokeBackend } from "@/services/desktop-bridge";`,
    `const invokeBackend = async () => { throw new Error("unused in this test"); };`,
  ],
];

let source = await readFile(new URL("../src/services/market-service.ts", import.meta.url), "utf8");
for (const [from, to] of replacements) {
  assert.ok(source.includes(from), `market-service.ts no longer imports ${from}`);
  source = source.replace(from, to);
}
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const { marketCoverUrl, parseMarketPage } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`
);

test("marketCoverUrl keeps only plain https image sources", () => {
  assert.equal(marketCoverUrl("https://cdn.example.org/covers/devtools.png"), "https://cdn.example.org/covers/devtools.png");
  assert.equal(marketCoverUrl("https://cdn.example.org/a.png?v=2"), "https://cdn.example.org/a.png?v=2");
  for (const rejected of [
    "http://cdn.example.org/a.png",
    "data:image/png;base64,AAAA",
    "javascript:alert(1)",
    "https://user:pass@cdn.example.org/a.png",
    "https://cdn.example.org/a.png#fragment",
    "https://",
    "",
    "   ",
    null,
    undefined,
    42,
    `https://cdn.example.org/${"a".repeat(500)}.png`,
  ]) {
    assert.equal(marketCoverUrl(rejected), "", `must reject ${String(rejected).slice(0, 60)}`);
  }
});

function pageWith(item) {
  return { page: 1, limit: 20, items: [{ ...baseItem(), ...item }] };
}

function baseItem() {
  return {
    product_key: "devtools",
    name: "DevTools",
    slug: "devtools",
    description: "Tools",
    homepage: "https://example.org/",
    icon: "setting",
    category: "development",
    tags: ["devtools"],
    author: "MinCode",
    latest_published_version: "0.1.0",
    updated_at: 1750000000,
    requires_paid_host: false,
  };
}

test("parseMarketPage keeps a valid cover and degrades an unusable one", () => {
  const [valid] = parseMarketPage(pageWith({ cover_url: "https://cdn.example.org/devtools.png" }), 1, 20);
  assert.equal(valid.cover_url, "https://cdn.example.org/devtools.png");

  const [invalid] = parseMarketPage(pageWith({ cover_url: "http://cdn.example.org/devtools.png" }), 1, 20);
  assert.equal(invalid.cover_url, "", "an http cover must not reach the WebView");
  assert.equal(invalid.product_key, "devtools", "a bad cover must not drop the listing");

  const [missing] = parseMarketPage(pageWith({}), 1, 20);
  assert.equal(missing.cover_url, "");
});

test("parseMarketPage still rejects a listing whose cover is not a string", () => {
  assert.throws(
    () => parseMarketPage(pageWith({ cover_url: { url: "https://cdn.example.org/a.png" } }), 1, 20),
    /Invalid market plugin metadata/
  );
});

test("parseMarketPage keeps host compatibility and degrades malformed versions", () => {
  const [rated] = parseMarketPage(pageWith({ min_host_version: "0.2.0", max_host_version_exclusive: "", host_compatibility: "host_too_old", compatible_version: "0.0.9" }), 1, 20);
  assert.deepEqual([rated.min_host_version, rated.max_host_version_exclusive, rated.host_compatibility, rated.compatible_version], ["0.2.0", "", "host_too_old", "0.0.9"]);
  const [legacy] = parseMarketPage(pageWith({}), 1, 20);
  assert.deepEqual([legacy.min_host_version, legacy.host_compatibility, legacy.compatible_version], ["", "", null]);
  const [bad] = parseMarketPage(pageWith({ min_host_version: "v1", compatible_version: 3 }), 1, 20);
  assert.deepEqual([bad.min_host_version, bad.compatible_version], ["", null]);
  assert.throws(() => parseMarketPage(pageWith({ host_compatibility: "maybe" }), 1, 20), /Invalid market plugin metadata/);
});
