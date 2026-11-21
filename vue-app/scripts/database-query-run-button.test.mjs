import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "vue/compiler-sfc";

const source = await readFile(new URL("../src/components/database/query/DatabaseQueryWorkspace.vue", import.meta.url), "utf8");

function runButtons() {
  const { descriptor } = parse(source);
  const found = [];
  const visit = (node, inGroup) => {
    const group = inGroup || node.tag === "el-button-group";
    if (group && node.tag === "el-button") {
      found.push(Object.fromEntries(node.props.map(prop => [prop.name, prop.value?.content ?? true])));
    }
    for (const child of [...(node.children ?? []), ...(node.branches ?? [])]) visit(child, group);
  };
  visit(descriptor.template.ast, false);
  return found;
}

test("the run split button is a light primary action, not a solid block in the compact toolbar", () => {
  const buttons = runButtons();
  assert.equal(buttons.length, 2, "run button and run-all dropdown trigger");
  for (const button of buttons) {
    assert.equal(button.type, "primary");
    assert.equal(button.plain, true, "both halves use the plain primary style");
  }
});

test("the split divider stays visible against the light primary background", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/database/redis-workspace.css", import.meta.url), "utf8");
  // Element Plus declares the variable on .el-button itself, so it must be overridden there.
  const rule = css.match(/\.db-sql-toolbar \.el-button-group \.el-button--primary\.is-plain \{([^}]*)\}/)?.[1] ?? "";
  // The default divider is rgba(255,255,255,.5); it must resolve to a theme colour instead.
  assert.match(rule, /--el-button-divide-border-color:\s*var\(--el-color-primary-light-5\)/);
});
