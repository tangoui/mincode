import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { databaseTypeBrand } from "../src/domain/database/type-brands.ts";
import { usageTone } from "../src/domain/ui/usage-tone.ts";

test("database brands keep their identity color and fall back for unknown drivers", () => {
  assert.deepEqual(databaseTypeBrand("mysql"), { color: "#4479a1", icon: "database" });
  assert.deepEqual(databaseTypeBrand("mongodb"), { color: "#3c9745", icon: "leaf" });
  assert.deepEqual(databaseTypeBrand("not-a-type"), { color: "#718096", icon: "database" });
});

test("resource usage turns warning and danger at fixed thresholds", () => {
  assert.equal(usageTone(undefined), "idle");
  assert.equal(usageTone(0), "normal");
  assert.equal(usageTone(74), "normal");
  assert.equal(usageTone(75), "warning");
  assert.equal(usageTone(89), "warning");
  assert.equal(usageTone(90), "danger");
});

test("group filters show the all-groups option instead of the empty placeholder", async () => {
  for (const path of ["../src/components/database/DatabaseConnectionOverview.vue", "../src/components/ssh/SshServerOverview.vue"]) {
    const source = await readFile(new URL(path, import.meta.url), "utf8");
    assert.match(source, /<el-select[^>]*v-model="selectedGroup"[^>]*:empty-values="\[null, undefined\]"/, path);
  }
});

test("SSH overview colors the in-use load bars with the shared usage tone", async () => {
  const source = await readFile(new URL("../src/components/ssh/SshServerOverview.vue", import.meta.url), "utf8");
  assert.match(source, /import \{ usageTone \} from "@\/domain\/ui\/usage-tone"/);
  assert.match(source, /:class="toneClass\(profile\.id, /);
});

test("database overview marks each connection type with its brand color", async () => {
  const source = await readFile(new URL("../src/components/database/DatabaseConnectionOverview.vue", import.meta.url), "utf8");
  assert.match(source, /databaseConnectionBrand\(connection\)\.color/);
  assert.match(source, /db-connection-overview__type-swatch/);
});

test("plugin driver connections take the driver's own color instead of the gray fallback", async () => {
  const { databaseConnectionBrand } = await import("../src/domain/database/type-brands.ts");
  assert.equal(databaseConnectionBrand({ dbType: "plugin", driver: { pluginId: "hive-driver", driverId: "hive" } }).color, "#d9a21b");
  assert.equal(databaseConnectionBrand({ dbType: "plugin", driver: { pluginId: "phoenix-driver", driverId: "phoenix" } }).color, "#b4321e");
  assert.equal(databaseConnectionBrand({ dbType: "plugin", driver: { pluginId: "db2-driver", driverId: "db2" } }).color, "#0f62fe");
  assert.equal(databaseConnectionBrand({ dbType: "plugin", driver: { pluginId: "xugudb-driver", driverId: "xugu" } }).color, "#0e7c66");
  // Built-in types keep their color; an unknown third-party driver keeps the neutral fallback.
  assert.equal(databaseConnectionBrand({ dbType: "mysql" }).color, "#4479a1");
  assert.equal(databaseConnectionBrand({ dbType: "plugin", driver: { pluginId: "acme", driverId: "x" } }).color, "#718096");
});

test("overview and sidebar tint plugin connections by driver and show the plugin icon", async () => {
  for (const path of ["../src/components/database/DatabaseConnectionOverview.vue", "../src/components/database/sidebar/DatabaseConnectionSidebar.vue"]) {
    const source = await readFile(new URL(path, import.meta.url), "utf8");
    assert.match(source, /databaseConnectionBrand\(connection\)\.color/, path);
    assert.doesNotMatch(source, /<DatabaseTypeIcon :db-type="connection\.dbType" size="small" \/>/, path);
  }
  const icon = await readFile(new URL("../src/components/database/DatabaseTypeIcon.vue", import.meta.url), "utf8");
  assert.match(icon, /databaseConnectionBrand\(/);
});
