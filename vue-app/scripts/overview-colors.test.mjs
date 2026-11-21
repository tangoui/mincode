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
