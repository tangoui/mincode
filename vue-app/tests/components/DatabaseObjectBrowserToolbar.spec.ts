import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import DatabaseObjectBrowser from "@/components/database/DatabaseObjectBrowser.vue";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// happy-dom resolves ?raw CSS to an empty module; read the real stylesheet.
const css = readFileSync(resolve(process.cwd(), "src/styles/workspaces/database/object-browser.css"), "utf8");

function rule(selector: string): string {
  const start = css.indexOf(`${selector} {`);
  expect(start, selector).toBeGreaterThanOrEqual(0);
  return css.slice(start, css.indexOf("}", start));
}

describe("database object browser toolbar", () => {
  it("wraps instead of overflowing when its own width is too narrow", () => {
    const toolbar = rule(".db-object-browser__toolbar");
    expect(toolbar).toMatch(/display:\s*flex/);
    expect(toolbar).toMatch(/flex-wrap:\s*wrap/);
    expect(toolbar).not.toMatch(/(?:^|\s)height:\s*42px/m);
    expect(toolbar).not.toMatch(/flex:\s*0 0 42px/);
    expect(css).toMatch(/container-name:\s*db-object-toolbar/);
    expect(css).toMatch(/@container db-object-toolbar \(max-width:/);
  });

  it("lets the search grow and shrink while fixed controls keep their full size", () => {
    expect(rule(".db-object-browser__toolbar > .db-object-browser__search.el-input")).toMatch(/flex:\s*1 1 \d+px/);
    for (const selector of [
      ".db-object-browser__toolbar > .db-object-browser__filters.el-segmented",
      ".db-object-browser__toolbar > .db-object-browser__sort.el-select",
      ".db-object-browser__toolbar > .el-button",
    ]) {
      expect(rule(selector), selector).toMatch(/flex:\s*none/);
    }
  });

  it("still renders every control so none is hidden by overflow", () => {
    const wrapper = mount(DatabaseObjectBrowser, {
      props: { database: "app", databases: ["app"], objects: [], searchText: "" },
    });
    const toolbar = wrapper.get(".db-object-browser__toolbar");
    for (const selector of [".db-object-browser__database", ".db-object-browser__search", ".db-object-browser__filters", ".db-object-browser__sort"]) {
      expect(toolbar.find(selector).exists(), selector).toBe(true);
    }
    wrapper.unmount();
  });
});
