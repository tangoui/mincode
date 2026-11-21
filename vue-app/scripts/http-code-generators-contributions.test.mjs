import assert from "node:assert/strict";
import { readdir } from "node:fs/promises";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  applyCodeFilter,
  CodeTemplateError,
  compileCodeTemplate,
  renderCodeTemplate,
} from "../src/domain/http/code-template.ts";
import {
  buildRedactedHttpCodeModel,
  redactBodyText,
  secretPlaceholder,
} from "../src/domain/http/code-generator-model.ts";
import {
  buildPluginHttpCodeGeneratorRegistry,
  HTTP_CODE_TEMPLATE_LIMITS,
  HTTP_CODE_TEMPLATE_VOCABULARY,
  httpCodeGeneratorPreviewSummary,
  renderPluginHttpCode,
} from "../src/domain/plugins/http-code-generators.ts";

const root = new URL("../../", import.meta.url);
const corpus = JSON.parse(await readFile(new URL("tools/verify/fixtures/codetmpl/render.json", root), "utf8"));
const contract = JSON.parse(await readFile(new URL("plugins/api/contribution-points.json", root), "utf8"));
const OUTPUT_MAX = contract.points.httpCodeGenerators.limits.outputMaxBytes;

test("the TypeScript template engine matches the shared Go corpus", () => {
  assert.ok(corpus.cases.length >= 30);
  for (const item of corpus.cases) {
    const source = item.repeat ? item.repeat.unit.repeat(item.repeat.count) : item.template;
    let output;
    let code;
    try {
      const template = compileCodeTemplate(source, HTTP_CODE_TEMPLATE_LIMITS, HTTP_CODE_TEMPLATE_VOCABULARY);
      output = renderCodeTemplate(template, item.model ?? {}, item.escape ?? "none", item.outputMaxBytes || OUTPUT_MAX);
    } catch (error) {
      assert.ok(error instanceof CodeTemplateError, `${item.name}: ${error}`);
      code = error.code;
    }
    if (item.error) assert.equal(code, item.error, item.name);
    else {
      assert.equal(code, undefined, item.name);
      assert.equal(output, item.output, item.name);
    }
  }
});

test("the contract vocabulary is used verbatim", () => {
  const lists = contract.points.httpCodeGenerators.lists;
  assert.deepEqual(HTTP_CODE_TEMPLATE_VOCABULARY.variables, lists.variables);
  assert.deepEqual(HTTP_CODE_TEMPLATE_VOCABULARY.filters, lists.filters);
  assert.equal(HTTP_CODE_TEMPLATE_LIMITS.maxDepth, contract.points.httpCodeGenerators.limits.sectionDepthMax);
});

test("filters escape for their target language", () => {
  assert.equal(applyCodeFilter("shell", "it's"), "'it'\\''s'");
  assert.equal(applyCodeFilter("powershell", "it\u2019s"), "'it\u2019\u2019s'");
  assert.equal(applyCodeFilter("cstring", 'a"\\\n\x01'), '"a\\"\\\\\\n\\001"');
  assert.equal(applyCodeFilter("json", "a\u2028</script>"), '"a\\u2028</script>"');
  assert.equal(applyCodeFilter("url", "a b/\u00e9"), "a%20b%2F%C3%A9");
  assert.equal(applyCodeFilter("xml", `<a href="x">'&'</a>`), "&lt;a href=&quot;x&quot;&gt;&apos;&amp;&apos;&lt;/a&gt;");
  assert.equal(applyCodeFilter("upper", "abc\u00e9"), "ABC\u00e9");
});

const SENTINEL = "SENTINELsecret0123456789";

function sentinelRequest(overrides = {}) {
  return {
    id: "r1",
    name: "Create user",
    method: "post",
    url: `https://admin:${SENTINEL}@api.example.test/users?access_token=${SENTINEL}&page=2#frag`,
    params: [
      { id: "p1", key: "api_key", value: SENTINEL, enabled: true },
      { id: "p2", key: "q", value: "{{search}}", enabled: true },
      { id: "p3", key: "jwt", value: `eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.${SENTINEL}`, enabled: true },
    ],
    headers: [
      { id: "h1", key: "Authorization", value: `Bearer ${SENTINEL}`, enabled: true },
      { id: "h2", key: "Cookie", value: `sid=${SENTINEL}`, enabled: true },
      { id: "h3", key: "X-Client-Secret", value: SENTINEL, enabled: true },
      { id: "h4", key: "X-Trace", value: `ghp_${SENTINEL}abcdefghij`, enabled: true },
      { id: "h5", key: "Accept", value: "application/json", enabled: true },
      { id: "h6", key: "X-Disabled", value: SENTINEL, enabled: false },
    ],
    body: `{\n  "name": "Ada",\n  "password": "${SENTINEL}",\n  "nested": { "refreshToken": "${SENTINEL}", "pin": 1234 },\n  "note": "Bearer ${SENTINEL}",\n  "host": "{{host}}"\n}`,
    bodyType: "json",
    authType: "bearer",
    authToken: SENTINEL,
    authUsername: SENTINEL,
    authPassword: SENTINEL,
    apiKeyName: "X-Api-Key",
    apiKeyValue: SENTINEL,
    oauth2ClientSecret: SENTINEL,
    oauth2AccessToken: SENTINEL,
    documentation: "",
    ...overrides,
  };
}

async function fixtureTemplates() {
  const base = new URL("tools/verify/fixtures/contributions/valid/", root);
  const templates = [];
  for (const dir of await readdir(base)) {
    if (!dir.startsWith("httpCodeGenerators-")) continue;
    const manifest = JSON.parse(await readFile(new URL(`${dir}/plugin.json`, base), "utf8"));
    for (const entry of manifest.contributes.httpCodeGenerators) {
      templates.push({ entry, text: await readFile(new URL(`${dir}/${entry.template}`, base), "utf8") });
    }
  }
  return templates;
}

test("no secret reaches any fixture template, for every auth and body type", async () => {
  const templates = await fixtureTemplates();
  assert.ok(templates.length >= 3);
  const variants = [
    {},
    { authType: "basic" },
    { authType: "api-key", apiKeyAddTo: "header" },
    { authType: "api-key", apiKeyAddTo: "query" },
    { authType: "oauth2" },
    { bodyType: "x-www-form-urlencoded", formBody: [
      { id: "f1", key: "password", value: SENTINEL, enabled: true, fieldType: "text" },
      { id: "f2", key: "user", value: "ada", enabled: true, fieldType: "text" },
    ] },
    { bodyType: "form-data", formBody: [
      { id: "f1", key: "upload", value: `/Users/me/${SENTINEL}.txt`, enabled: true, fieldType: "file" },
    ] },
    { bodyType: "raw", body: `grant_type=password&client_secret=${SENTINEL}&username=ada` },
    { bodyType: "xml", body: `<login><password>${SENTINEL}</password></login>` },
    { bodyType: "graphql", graphqlQuery: "query { me { id } }", graphqlVariables: `{"token":"${SENTINEL}"}` },
    { bodyType: "binary", binaryFilePath: `/tmp/${SENTINEL}.bin` },
    { bodyType: "text", body: `-----BEGIN RSA PRIVATE KEY-----\n${SENTINEL}\n-----END RSA PRIVATE KEY-----` },
  ];
  for (const variant of variants) {
    const request = sentinelRequest(variant);
    const model = buildRedactedHttpCodeModel(request);
    assert.ok(!JSON.stringify(model).includes("SENTINEL"), `model leaks for ${JSON.stringify(variant)}: ${JSON.stringify(model)}`);
    for (const { entry, text } of templates) {
      const output = renderPluginHttpCode(entry, text, request);
      assert.ok(!output.includes("SENTINEL"), `${entry.id} leaks for ${JSON.stringify(variant)}:\n${output}`);
    }
  }
});

test("the redacted model keeps structure, placeholders and env references", () => {
  const model = buildRedactedHttpCodeModel(sentinelRequest());
  assert.equal(model.vars.method, "POST");
  assert.equal(model.vars.scheme, "https");
  assert.equal(model.vars.host, "api.example.test");
  assert.equal(model.vars.path, "/users");
  assert.equal(model.vars.urlBase, "https://api.example.test/users");
  assert.ok(model.vars.url.startsWith("https://api.example.test/users?access_token=YOUR_ACCESS_TOKEN&page=2&api_key=YOUR_API_KEY&q={{search}}"));
  const header = (name) => model.lists.headers.find((item) => item.name === name)?.value;
  assert.equal(header("Authorization"), "Bearer YOUR_TOKEN");
  assert.equal(header("Cookie"), "YOUR_COOKIE");
  assert.equal(header("X-Client-Secret"), "YOUR_X_CLIENT_SECRET");
  assert.equal(header("X-Trace"), "YOUR_SECRET");
  assert.equal(header("Accept"), "application/json");
  assert.equal(header("X-Disabled"), undefined);
  assert.equal(header("Content-Type"), "application/json");
  assert.ok(model.vars.body.includes('"password": "YOUR_PASSWORD"'));
  assert.ok(model.vars.body.includes('"pin": 1234'));
  assert.ok(model.vars.body.includes('"host": "{{host}}"'));
  assert.deepEqual(model.lists.variables.map((item) => item.name).sort(), ["host", "search"]);
  assert.equal(model.flags.hasJsonBody, true);
  assert.equal(model.flags.hasVariables, true);

  const envAuth = buildRedactedHttpCodeModel(sentinelRequest({ authToken: "{{token}}", headers: [] }));
  assert.equal(envAuth.lists.headers.find((item) => item.name === "Authorization")?.value, "Bearer {{token}}");
  const binary = buildRedactedHttpCodeModel(sentinelRequest({ bodyType: "binary" }));
  assert.equal(binary.vars.body, "");
  assert.equal(binary.flags.hasBinaryBody, true);
  assert.equal(secretPlaceholder("apiKey"), "YOUR_API_KEY");
  assert.equal(redactBodyText("a=1&token=abc"), "a=1&token=YOUR_TOKEN");
});

test("control and bidi characters never reach the output", () => {
  const request = sentinelRequest({
    name: "a\u202eb",
    headers: [{ id: "h", key: "X-Note", value: "x\u2066y\u0007z\r\nInjected: 1", enabled: true }],
    authType: "none",
  });
  const model = buildRedactedHttpCodeModel(request);
  assert.equal(model.vars.requestName, "ab");
  assert.equal(model.lists.headers[0].value, "xyzInjected: 1");
});

test("the registry re-checks entries and the preview lists titles", () => {
  const key = "a".repeat(64);
  const registry = buildPluginHttpCodeGeneratorRegistry([{
    pluginId: "acme",
    pluginName: "Acme",
    version: "1.0.0",
    source: "installed",
    runtime: "declarative",
    activeInWorkspace: false,
    point: {
      fingerprint: "f",
      entries: [
        { id: "httpie", title: "HTTPie", escape: "shell", template: key },
        { id: "httpie", title: "Dup", escape: "shell", template: key },
        { id: "bad", title: "Bad", escape: "bash", template: key },
        { id: "nokey", title: "No key", escape: "json", template: "codegen/x.tmpl" },
        { id: "bidi", title: "a\u202eb", escape: "json", template: key },
      ],
      issues: [{ point: "httpCodeGenerators", entry: 9, message: "host issue" }],
    },
  }]);
  assert.deepEqual(registry.generators.map((item) => item.key), ["acme.httpie"]);
  assert.equal(registry.issues.length, 5);
  assert.deepEqual(httpCodeGeneratorPreviewSummary({ httpCodeGenerators: [{ title: "HTTPie" }, { title: "" }] }), ["HTTPie"]);
});

test("the consumer, dialog and install preview are wired", async () => {
  const read = (path) => readFile(new URL(path, root), "utf8");
  const index = await read("vue-app/src/services/contributions/index.ts");
  assert.match(index, /\/\/ httpCodeGenerators \(Phase G\)\n\s+registerHttpCodeGeneratorContributionConsumer,/);
  const dialog = await read("vue-app/src/components/http/HttpCodeGeneratorDialog.vue");
  assert.match(dialog, /renderPluginHttpCode/);
  assert.match(dialog, /redactionNotice/);
  assert.doesNotMatch(dialog, /v-html/);
  const service = await read("vue-app/src/services/http-code-generator-service.ts");
  assert.match(service, /crypto\.subtle\.digest\("SHA-256"/);
  assert.doesNotMatch(service, /invokeBackend|plugin_host_action/);
  const manager = await read("vue-app/src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /httpCodeGeneratorPreviewSummary/);
  for (const locale of ["zh", "en"]) {
    const text = await read(`vue-app/src/i18n/locales/${locale}.ts`);
    for (const key of ["group", "redactionNotice", "renderFailed", "tooLarge", "unavailable", "previewSummary"]) {
      assert.ok(text.includes(`"pluginContrib.httpCodeGenerators.${key}"`), `${locale} ${key}`);
    }
  }
});
