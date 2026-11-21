import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  JSON_SCHEMA_ASSOCIATIONS,
  TOML_SCHEMA_ASSOCIATIONS,
  lspConfigurationValue,
  lspSchemaAssociationNotification,
} from "../src/domain/projects/lsp-schemas.ts";

test("json-ls receives schema associations for common config files", () => {
  const notification = lspSchemaAssociationNotification("json-ls");
  assert.equal(notification?.method, "json/schemaAssociations");
  const associations = notification.params;
  assert.deepEqual(associations["package.json"], ["https://www.schemastore.org/package.json"]);
  assert.ok(associations["tsconfig.json"] && associations["tsconfig.*.json"]);
  assert.ok(associations["composer.json"]);
  for (const urls of Object.values(JSON_SCHEMA_ASSOCIATIONS)) {
    for (const url of urls) assert.match(url, /^https:\/\//);
  }
  assert.equal(lspSchemaAssociationNotification("gopls"), undefined);
  assert.equal(lspSchemaAssociationNotification("plugin:vendor.x/json"), undefined);
});

test("taplo gets direct associations instead of its incompatible default catalog", () => {
  const config = lspConfigurationValue("taplo", "evenBetterToml");
  assert.equal(config.schema.enabled, true);
  assert.deepEqual(config.schema.catalogs, []);
  assert.equal(config.schema.associations, TOML_SCHEMA_ASSOCIATIONS);
  const cargo = Object.keys(TOML_SCHEMA_ASSOCIATIONS).find(pattern => new RegExp(pattern).test("file:///p/crate/Cargo.toml"));
  assert.equal(TOML_SCHEMA_ASSOCIATIONS[cargo], "https://www.schemastore.org/cargo.json");
  assert.ok(!Object.keys(TOML_SCHEMA_ASSOCIATIONS).some(pattern => new RegExp(pattern).test("file:///p/notCargo.toml")));
  assert.equal(lspConfigurationValue("taplo", "other"), null);
  assert.equal(lspConfigurationValue("yaml-ls", "evenBetterToml"), null);
});

test("the runtime sends associations after initialized and answers configuration per section", async () => {
  const runtime = await readFile(new URL("../src/composables/project-language-server-runtime.ts", import.meta.url), "utf8");
  const start = runtime.indexOf('"initialized", {});');
  assert.match(runtime.slice(start, start + 500), /lspSchemaAssociationNotification\(serverId\)/);
  const configuration = runtime.slice(runtime.indexOf('case "workspace/configuration"'), runtime.indexOf('case "workspace/applyEdit"'));
  assert.match(configuration, /lspConfigurationValue\(serverId, /);
});
