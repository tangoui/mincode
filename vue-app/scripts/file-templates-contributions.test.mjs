import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  buildPluginFileTemplateRegistry,
  fileTemplatePreviewSummary,
  isValidTemplateName,
  planTemplateWrites,
  renderedTargetPathError,
  renderTargetPaths,
  renderTemplateText,
  templateTargetPathError,
  templateVariables,
} from "../src/domain/projects/file-templates.ts";

const KEY_A = "a".repeat(64);
const KEY_B = "b".repeat(64);

function contribution(entries, overrides = {}) {
  return {
    pluginId: "acme",
    pluginName: "Acme Tools",
    version: "1.0.0",
    source: "installed",
    runtime: "declarative",
    activeInWorkspace: false,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

const fileTemplate = {
  id: "vue-component",
  kind: "file",
  title: "Vue component",
  category: "Vue",
  defaultName: "MyWidget",
  files: [{ path: "{{mincode.namePascal}}.vue", source: KEY_A, size: 40 }],
};

const projectTemplate = {
  id: "go-service",
  kind: "project",
  title: "Go service",
  files: [
    { path: "go.mod", source: KEY_A, size: 20 },
    { path: "cmd/{{mincode.nameKebab}}/main.go", source: KEY_B, size: 30 },
  ],
};

test("variables are derived from the name and the date", () => {
  const variables = templateVariables("  my cool-Widget ", "demo", new Date(2026, 8, 5));
  assert.deepEqual(variables, {
    name: "my cool-Widget",
    fileName: "my cool-Widget",
    projectName: "demo",
    date: "2026-09-05",
    year: "2026",
    nameCamel: "myCoolWidget",
    namePascal: "MyCoolWidget",
    nameKebab: "my-cool-widget",
    nameSnake: "my_cool_widget",
  });
});

test("only allowlisted mincode variables are substituted", () => {
  const variables = templateVariables("Widget", "demo", new Date(2026, 0, 1));
  assert.equal(
    renderTemplateText("{{mincode.name}} {{mincode.home}} {{ name }} {{mincode.year}} {{mincode.name", variables),
    "Widget {{mincode.home}} {{ name }} 2026 {{mincode.name",
  );
});

test("registry re-checks entries and ignores the workspace", () => {
  const registry = buildPluginFileTemplateRegistry([
    contribution([
      fileTemplate,
      projectTemplate,
      { ...fileTemplate, id: "two", files: [fileTemplate.files[0], { path: "b.txt", source: KEY_B, size: 1 }] },
      { ...fileTemplate, id: "git", files: [{ path: ".git/config", source: KEY_A, size: 1 }] },
      { ...fileTemplate, id: "env", files: [{ path: ".env", source: KEY_A, size: 1 }] },
      { ...fileTemplate, id: "escape", files: [{ path: "../x", source: KEY_A, size: 1 }] },
      { ...fileTemplate, id: "unknown", files: [{ path: "{{mincode.home}}.txt", source: KEY_A, size: 1 }] },
      { ...fileTemplate, id: "big", files: [{ path: "a.txt", source: KEY_A, size: 262145 }] },
    ]),
  ]);
  assert.deepEqual(registry.templates.map((item) => item.key), ["acme.vue-component", "acme.go-service"]);
  assert.equal(registry.issues.length, 6);
  assert.ok(registry.issues.every((issue) => issue.stage === "frontend"));
});

test("target path rules before and after rendering", () => {
  assert.equal(templateTargetPathError("src/{{mincode.name}}.ts"), null);
  assert.match(templateTargetPathError("/abs"), /relative/);
  assert.match(templateTargetPathError("a/node_modules/x"), /node_modules/);
  assert.match(templateTargetPathError("a/b/c/d/e/f/g/h/i"), /deep/);
  assert.match(templateTargetPathError("keys/id_rsa"), /sensitive/);
  assert.equal(renderedTargetPathError("src/My Widget.vue"), null);
  assert.match(renderedTargetPathError("src/.git/x"), /\.git/);
  assert.match(renderedTargetPathError("a/../b"), /invalid/);
  assert.match(renderedTargetPathError("a/\u202eb"), /invalid/);
  assert.equal(isValidTemplateName("MyWidget"), true);
  for (const bad of ["", "a/b", "a\\b", "..", "x..y", ".env", "node_modules", "a\u202eb", "x".repeat(65)]) {
    assert.equal(isValidTemplateName(bad), false, bad);
  }
});

test("rendered paths are re-validated and must stay unique", () => {
  const [template] = buildPluginFileTemplateRegistry([contribution([projectTemplate])]).templates;
  const targets = renderTargetPaths(template, templateVariables("Api Server", "Api Server"));
  assert.deepEqual(targets.map((item) => item.relativePath), ["go.mod", "cmd/api-server/main.go"]);
  const [dup] = buildPluginFileTemplateRegistry([
    contribution([{ ...projectTemplate, files: [{ path: "{{mincode.name}}.txt", source: KEY_A, size: 1 }, { path: "{{mincode.fileName}}.txt", source: KEY_B, size: 1 }] }]),
  ]).templates;
  assert.throws(() => renderTargetPaths(dup, templateVariables("x", "p")), /conflicting target/);
  const [sensitive] = buildPluginFileTemplateRegistry([
    contribution([{ ...fileTemplate, files: [{ path: "{{mincode.name}}", source: KEY_A, size: 1 }] }]),
  ]).templates;
  assert.throws(() => renderTargetPaths(sensitive, templateVariables("credentials", "p")), /sensitive/);
});

test("write plan joins the parent folder and renders contents", () => {
  const [template] = buildPluginFileTemplateRegistry([contribution([projectTemplate])]).templates;
  const variables = templateVariables("api", "api", new Date(2026, 0, 1));
  const sources = new Map([
    [KEY_A, "module example.com/{{mincode.name}}\n"],
    [KEY_B, "// {{mincode.year}} {{other}}\n"],
  ]);
  const plan = planTemplateWrites(template, variables, "api", sources);
  assert.deepEqual(plan.map((item) => item.filePath), ["api/go.mod", "api/cmd/api/main.go"]);
  assert.equal(plan[0].content, "module example.com/api\n");
  assert.equal(plan[1].content, "// 2026 {{other}}\n");
  assert.equal(plan[1].bytes, Buffer.byteLength(plan[1].content));
  assert.equal(planTemplateWrites(template, variables, "", sources)[0].filePath, "go.mod");
  assert.throws(() => planTemplateWrites(template, variables, "", new Map([[KEY_A, ""]])), /unavailable/);
});

test("install preview lists templates", () => {
  assert.deepEqual(fileTemplatePreviewSummary({ fileTemplates: [fileTemplate, projectTemplate] }), [
    { title: "Vue component", kind: "file", files: 1 },
    { title: "Go service", kind: "project", files: 2 },
  ]);
  assert.deepEqual(fileTemplatePreviewSummary({}), []);
});

test("code name transforms remain identifiers for numeric and non-Latin names", () => {
 for (const name of ['123 app', '你好', 'hello "world"']) {
  const vars = templateVariables(name, 'demo');
  assert.match(vars.namePascal, /^[A-Za-z_][A-Za-z0-9_]*$/);
  assert.match(vars.nameCamel, /^[A-Za-z_][A-Za-z0-9_]*$/);
  assert.match(vars.nameSnake, /^[A-Za-z_][A-Za-z0-9_]*$/);
 }
});

test("rendered file/directory prefix collisions fail before writes",()=>{
 const t={kind:"project",files:[{path:"a",source:KEY_A,size:1},{path:"a/b.txt",source:KEY_B,size:1}]};
 assert.throws(()=>renderTargetPaths(t,templateVariables("Example","demo")));
});

test("catalog overflow is reported instead of silently hidden",()=>{
 const entries=Array.from({length:65},(_,i)=>({...fileTemplate,id:`entry-${i}`}));
 const registry=buildPluginFileTemplateRegistry([contribution(entries)]);
 assert.equal(registry.templates.length,64);
 assert.ok(registry.issues.some(issue=>issue.message.includes('limit')));
});
