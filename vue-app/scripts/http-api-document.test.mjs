import assert from "node:assert/strict";
import test from "node:test";
import { parse as parseYaml } from "yaml";
import {
  importApiDocument,
  importedApiCollection,
  mergeImportedCollection,
  mergeImportedEnvironments,
  serializeApiDocument,
} from "../src/domain/http/api-document.ts";

function ids() {
  let next = 0;
  return (prefix) => `${prefix}-${++next}`;
}

const NOW = 1_700_000_000_000;
const options = { now: NOW, environmentName: "Imported" };
const load = (document, filename = "doc.json") =>
  importApiDocument(typeof document === "string" ? document : JSON.stringify(document), filename, ids(), options);

function allRequests(folder) {
  return [...folder.requests, ...folder.children.flatMap(allRequests)];
}

test("imports Postman v2.1 folders, auth inheritance, bodies and variables", () => {
  const result = load({
    info: { name: "Shop", schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
    auth: { type: "bearer", bearer: [{ key: "token", value: "{{token}}" }] },
    variable: [{ key: "baseUrl", value: "https://api.example.com" }],
    item: [
      {
        name: "Users",
        item: [
          {
            name: "Get user",
            request: {
              method: "GET",
              url: {
                raw: "{{baseUrl}}/users/:id?verbose=1",
                query: [{ key: "verbose", value: "1" }, { key: "debug", value: "true", disabled: true }],
                variable: [{ key: "id", value: "" }],
              },
              header: [{ key: "Accept", value: "application/json" }],
            },
          },
          {
            name: "Upload avatar",
            request: {
              method: "POST",
              auth: { type: "noauth" },
              url: "{{baseUrl}}/avatar",
              body: { mode: "formdata", formdata: [{ key: "file", type: "file", src: "/Users/me/a.png" }, { key: "note", value: "hi" }] },
            },
          },
        ],
      },
      {
        name: "Login",
        request: {
          method: "post",
          url: "{{baseUrl}}/login",
          body: { mode: "raw", raw: "{\"user\":\"a\"}", options: { raw: { language: "json" } } },
          description: { content: "Signs in" },
        },
      },
      { name: "Search", request: { method: "POST", url: "{{baseUrl}}/graphql", body: { mode: "graphql", graphql: { query: "{ me { id } }", variables: "{}" } } } },
    ],
  });
  assert.equal(result.kind, "collection");
  assert.equal(result.source, "postman");
  assert.equal(result.requestCount, 4);
  assert.equal(result.name, "Shop");
  assert.deepEqual(result.children.map((folder) => folder.name), ["Users"]);

  const [getUser, upload] = result.children[0].requests;
  assert.equal(getUser.url, "{{baseUrl}}/users/{{id}}");
  assert.deepEqual(getUser.params.map((row) => [row.key, row.value, row.enabled]), [["verbose", "1", true], ["debug", "true", false]]);
  assert.equal(getUser.authType, "bearer");
  assert.equal(getUser.authToken, "{{token}}");
  assert.equal(upload.authType, "none");
  assert.equal(upload.bodyType, "form-data");
  assert.deepEqual(upload.formBody.map((field) => [field.key, field.fieldType, field.value, field.fileName]), [
    ["file", "file", "", "a.png"],
    ["note", "text", "hi", undefined],
  ]);

  const [login, search] = result.requests;
  assert.equal(login.method, "POST");
  assert.equal(login.bodyType, "json");
  assert.equal(login.documentation, "Signs in");
  assert.equal(search.bodyType, "graphql");
  assert.equal(search.graphqlQuery, "{ me { id } }");
  assert.deepEqual(result.environments[0].vars.map((row) => [row.key, row.value]), [["baseUrl", "https://api.example.com"]]);
});

test("imports a Postman environment file", () => {
  const result = load({
    name: "Staging",
    _postman_variable_scope: "environment",
    values: [{ key: "baseUrl", value: "https://staging", enabled: true }, { key: "old", value: "x", enabled: false }],
  });
  assert.equal(result.kind, "environment");
  assert.equal(result.environment.name, "Staging");
  assert.deepEqual(result.environment.vars.map((row) => [row.key, row.enabled]), [["baseUrl", true], ["old", false]]);
});

test("rejects Postman v1 and unknown documents with clear codes", () => {
  assert.throws(() => load({ info: { name: "x", schema: "https://schema.getpostman.com/json/collection/v1.0.0/" }, item: [] }), /unsupported_postman_version/);
  assert.throws(() => load({ hello: "world" }), /unsupported_api_document/);
  assert.throws(() => load("{not json"), /invalid_api_document/);
  assert.throws(() => load({ openapi: "2.0", paths: {} }), /unsupported_openapi_version/);
});

const OPENAPI_YAML = `
openapi: 3.0.3
info: { title: Pets }
servers:
  - url: https://{env}.example.com/v1
    variables: { env: { default: api } }
security: [{ bearer: [] }]
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer }
    key: { type: apiKey, in: query, name: api_key }
  parameters:
    PetId: { name: petId, in: path, required: true, schema: { type: integer, example: 7 } }
  schemas:
    Pet:
      type: object
      properties:
        name: { type: string, example: Rex }
        tags: { type: array, items: { type: string } }
paths:
  /pets/{petId}:
    parameters: [{ $ref: '#/components/parameters/PetId' }]
    get:
      tags: [Pets/Read]
      summary: Get pet
      parameters: [{ name: fields, in: query, schema: { type: string, default: all } }]
    put:
      tags: [Pets]
      summary: Update pet
      security: [{ key: [] }]
      requestBody:
        content:
          application/json:
            schema: { $ref: '#/components/schemas/Pet' }
  /upload:
    post:
      security: []
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
                title: { type: string, example: hello }
`;

test("imports OpenAPI 3 with refs, tags as folders, servers as environment and security", () => {
  const result = load(OPENAPI_YAML, "pets.yaml");
  assert.equal(result.source, "openapi");
  assert.equal(result.requestCount, 3);
  assert.deepEqual(result.environments[0].vars.map((row) => [row.key, row.value]), [["baseUrl", "https://api.example.com/v1"]]);
  const pets = result.children.find((folder) => folder.name === "Pets");
  const read = pets.children.find((folder) => folder.name === "Read");
  const get = read.requests[0];
  assert.equal(get.url, "{{baseUrl}}/pets/7");
  assert.deepEqual(get.params.map((row) => [row.key, row.value]), [["fields", "all"]]);
  assert.equal(get.authType, "bearer");
  const put = pets.requests[0];
  assert.equal(put.authType, "api-key");
  assert.equal(put.apiKeyAddTo, "query");
  assert.deepEqual(JSON.parse(put.body), { name: "Rex", tags: [""] });
  const upload = result.requests[0];
  assert.equal(upload.authType, "none");
  assert.equal(upload.bodyType, "form-data");
  assert.deepEqual(upload.formBody.map((field) => [field.key, field.fieldType, field.value]), [["file", "file", ""], ["title", "text", "hello"]]);
});

test("imports Swagger 2.0 body and formData parameters", () => {
  const result = load({
    swagger: "2.0",
    info: { title: "Legacy" },
    host: "legacy.example.com",
    basePath: "/api",
    schemes: ["http"],
    paths: {
      "/items": {
        post: {
          summary: "Create",
          consumes: ["application/json"],
          parameters: [{ in: "body", name: "body", schema: { type: "object", properties: { id: { type: "integer" } } } }],
        },
      },
      "/files": {
        post: { summary: "Upload", parameters: [{ in: "formData", name: "doc", type: "file" }] },
      },
    },
  });
  assert.equal(result.source, "swagger");
  assert.deepEqual(result.environments[0].vars.map((row) => row.value), ["http://legacy.example.com/api"]);
  const [create, upload] = result.requests;
  assert.equal(create.bodyType, "json");
  assert.deepEqual(JSON.parse(create.body), { id: 0 });
  assert.equal(upload.bodyType, "form-data");
  assert.equal(upload.formBody[0].fieldType, "file");
});

test("imports an Apifox project export", () => {
  const result = load({
    apifoxProject: "1.0.0",
    $schema: { app: "apifox", type: "project", version: "1.2.0" },
    info: { name: "Box" },
    apiCollection: [
      {
        name: "根目录",
        items: [
          {
            name: "认证",
            auth: { type: "bearer", bearer: { token: "{{token}}" } },
            items: [
              {
                name: "登录",
                api: {
                  method: "post",
                  path: "/api/auth/login",
                  parameters: { query: [{ name: "lang", example: "zh" }], path: [], header: [{ name: "X-Trace", example: "1", enable: false }], cookie: [{ name: "sid", example: "abc" }] },
                  auth: {},
                  requestBody: { type: "application/json", examples: [{ value: "{\"account\":\"admin\"}", mediaType: "application/json" }] },
                  description: "登录接口",
                },
              },
              {
                name: "用户详情",
                api: {
                  method: "get",
                  path: "/api/users/{id}",
                  serverId: "files",
                  parameters: { path: [{ name: "id", example: "42" }] },
                  auth: { type: "noauth" },
                  requestBody: { type: "none" },
                },
              },
            ],
          },
          {
            name: "上传",
            api: {
              method: "post",
              path: "/upload",
              requestBody: { type: "multipart/form-data", parameters: [{ name: "file", type: "file" }, { name: "kind", example: "avatar" }] },
            },
          },
        ],
      },
    ],
    environments: [
      { name: "开发环境", baseUrl: "127.0.0.1:8001", baseUrls: { default: "127.0.0.1:8001", files: "https://files.dev" }, variables: [{ name: "token", value: "dev-token" }] },
    ],
    globalVariables: [{ id: "1", variables: [{ name: "tenant", value: "", initialValue: "t1" }] }],
  });
  assert.equal(result.source, "apifox");
  assert.equal(result.name, "Box");
  assert.equal(result.requestCount, 3);
  const auth = result.children.find((folder) => folder.name === "认证");
  const [login, user] = auth.requests;
  assert.equal(login.url, "{{baseUrl}}/api/auth/login");
  assert.equal(login.authType, "bearer", "empty auth inherits from folder");
  assert.equal(login.authToken, "{{token}}");
  assert.equal(login.bodyType, "json");
  assert.equal(login.body, "{\"account\":\"admin\"}");
  assert.deepEqual(login.headers.map((row) => [row.key, row.value, row.enabled]), [["X-Trace", "1", false], ["Cookie", "sid=abc", true]]);
  assert.equal(user.url, "{{baseUrl_files}}/api/users/42");
  assert.equal(user.authType, "none");
  assert.equal(result.requests[0].bodyType, "form-data");
  assert.deepEqual(
    result.environments[0].vars.map((row) => [row.key, row.value]),
    [["baseUrl", "http://127.0.0.1:8001"], ["baseUrl_files", "https://files.dev"], ["tenant", "t1"], ["token", "dev-token"]]
  );
});

test("imports an Apipost 7 nested project export", () => {
  const result = load({
    project: { name: "Mall", variable: { appId: { value: "1", current_value: "2" } } },
    apis: [
      {
        name: "订单",
        target_type: "folder",
        request: { auth: { type: "kv", kv: { key: "X-Key", value: "{{key}}" } } },
        children: [
          {
            name: "订单详情",
            target_type: "api",
            method: "GET",
            url: "/orders/:orderId?expand=items",
            request: {
              query: { parameter: [{ key: "expand", value: "items", is_checked: "1" }, { key: "debug", value: "1", is_checked: -1 }] },
              resful: { parameter: [{ key: "orderId", value: "" }] },
              header: { parameter: [{ key: "Accept", value: "*/*", is_checked: 1 }] },
              body: { mode: "none" },
              description: "查询订单",
            },
          },
          {
            name: "创建订单",
            target_type: "api",
            method: "POST",
            url: "https://other.example.com/orders",
            request: { auth: { type: "noauth" }, body: { mode: "formdata", parameter: [{ key: "sku", value: "A1", type: "Text", is_checked: 1 }] } },
          },
        ],
      },
      { name: "说明", target_type: "doc" },
    ],
    envs: [{ name: "测试环境", pre_url: "https://test.example.com", list: { key: { current_value: "k-1" } } }],
  });
  assert.equal(result.source, "apipost");
  assert.equal(result.requestCount, 2);
  const [detail, create] = result.children[0].requests;
  assert.equal(detail.url, "{{baseUrl}}/orders/{{orderId}}");
  assert.deepEqual(detail.params.map((row) => [row.key, row.enabled]), [["expand", true], ["debug", false]]);
  assert.equal(detail.authType, "api-key");
  assert.equal(detail.apiKeyName, "X-Key");
  assert.equal(detail.documentation, "查询订单");
  assert.equal(create.url, "https://other.example.com/orders");
  assert.equal(create.authType, "none");
  assert.equal(create.bodyType, "form-data");
  assert.deepEqual(result.environments[0].vars.map((row) => [row.key, row.value]), [["baseUrl", "https://test.example.com"], ["appId", "2"], ["key", "k-1"]]);
});

test("imports an Apipost 8 flat list with parent ids", () => {
  const result = load({
    project: { name: "Flat" },
    apis: [
      { target_id: "b", parent_id: "a", target_type: "api", name: "Ping", method: "GET", url: "/ping", sort: 2, request: { body: { mode: "json", raw: "{}" } } },
      { target_id: "a", parent_id: "0", target_type: "folder", name: "Health", sort: 1 },
      { target_id: "c", parent_id: "missing", target_type: "api", name: "Orphan", method: "GET", url: "/orphan" },
    ],
    global: { envs: [{ name: "Prod", server_list: [{ uri: "https://prod" }], env_var_list: { region: { value: "cn" } } }] },
  });
  assert.equal(result.requestCount, 2);
  assert.equal(result.children[0].name, "Health");
  assert.equal(result.children[0].requests[0].url, "{{baseUrl}}/ping");
  assert.equal(result.children[0].requests[0].bodyType, "json");
  assert.equal(result.requests[0].name, "Orphan");
  assert.deepEqual(result.environments[0].vars.map((row) => [row.key, row.value]), [["baseUrl", "https://prod"], ["region", "cn"]]);
});

function sampleCollection() {
  const request = (overrides) => ({
    id: overrides.name,
    method: "GET",
    params: [],
    headers: [],
    body: "",
    bodyType: "none",
    formBody: [],
    authType: "none",
    documentation: "",
    updatedAt: NOW,
    ...overrides,
  });
  return {
    id: "c1",
    name: "Demo",
    requests: [
      request({
        name: "Create user",
        method: "POST",
        url: "{{baseUrl}}/users",
        headers: [
          { id: "h1", key: "X-Trace", value: "1", enabled: true },
          { id: "h2", key: "Authorization", value: "Bearer abcdefghijklmnopqrstuvwxyz", enabled: true },
        ],
        bodyType: "json",
        body: "{\"name\":\"a\"}",
        authType: "bearer",
        authToken: "literal-secret",
        documentation: "Creates a user",
      }),
    ],
    children: [
      {
        id: "c2",
        name: "Files",
        requests: [
          request({
            name: "Upload",
            method: "POST",
            url: "{{baseUrl}}/files/{{fileId}}",
            params: [{ id: "p1", key: "token", value: "raw-token", enabled: true }, { id: "p2", key: "v", value: "2", enabled: false }],
            bodyType: "form-data",
            formBody: [{ id: "f1", key: "doc", value: "", enabled: true, fieldType: "file", fileName: "a.pdf" }],
            authType: "api-key",
            apiKeyName: "X-Key",
            apiKeyValue: "{{apiKey}}",
          }),
        ],
        children: [],
      },
    ],
  };
}

const ENVIRONMENT = { id: "e1", name: "Dev", vars: [{ id: "v1", key: "baseUrl", value: "https://dev.example.com", enabled: true }, { id: "v2", key: "password", value: "hunter2", enabled: true }] };

test("exports Postman v2.1 with folders, bodies, auth and redacted secrets", () => {
  const document = JSON.parse(serializeApiDocument(sampleCollection(), "postman", ENVIRONMENT));
  assert.match(document.info.schema, /v2\.1\.0/);
  assert.deepEqual(document.item.map((item) => item.name), ["Files", "Create user"]);
  const create = document.item[1].request;
  assert.deepEqual(create.auth, { type: "bearer", bearer: [{ key: "token", value: "", type: "string" }] });
  assert.deepEqual(create.header.map((row) => [row.key, row.value]), [["X-Trace", "1"], ["Authorization", ""]]);
  assert.deepEqual(create.body, { mode: "raw", raw: "{\"name\":\"a\"}", options: { raw: { language: "json" } } });
  const upload = document.item[0].item[0].request;
  assert.equal(upload.url.raw, "{{baseUrl}}/files/{{fileId}}?token=");
  assert.deepEqual(upload.url.query.map((row) => [row.key, row.value, row.disabled]), [["token", "", undefined], ["v", "2", true]]);
  assert.deepEqual(upload.body.formdata[0], { key: "doc", type: "file", src: "a.pdf" });
  assert.deepEqual(upload.auth.apikey[1], { key: "value", value: "{{apiKey}}", type: "string" });
  assert.deepEqual(document.variable, [{ key: "baseUrl", value: "https://dev.example.com" }, { key: "password", value: "" }]);
});

test("exports OpenAPI with servers from the environment, folder tags, path params and security", () => {
  const document = parseYaml(serializeApiDocument(sampleCollection(), "openapi-yaml", ENVIRONMENT));
  assert.deepEqual(document.servers, [{ url: "https://dev.example.com" }]);
  const create = document.paths["/users"].post;
  assert.deepEqual(create.security, [{ bearerAuth: [] }]);
  assert.deepEqual(create.requestBody.content["application/json"].example, { name: "a" });
  assert.equal(create.parameters.some((parameter) => parameter.name === "Authorization"), false);
  const upload = document.paths["/files/{fileId}"].post;
  assert.deepEqual(upload.tags, ["Files"]);
  assert.equal(upload["x-apifox-folder"], "Files");
  assert.deepEqual(upload.parameters[0], { name: "fileId", in: "path", required: true, schema: { type: "string" } });
  assert.equal(upload.parameters.find((parameter) => parameter.name === "token").schema.example, undefined);
  assert.deepEqual(upload.requestBody.content["multipart/form-data"].schema.properties.doc, { type: "string", format: "binary" });
  assert.deepEqual(document.components.securitySchemes.apiKeyAuth, { type: "apiKey", name: "X-Key", in: "header" });
});

test("exported Postman and OpenAPI files import back with structure intact", () => {
  for (const format of ["postman", "openapi-json"]) {
    const result = load(serializeApiDocument(sampleCollection(), format, ENVIRONMENT));
    assert.equal(result.requestCount, 2, format);
    assert.deepEqual(result.children.map((folder) => folder.name), ["Files"], format);
    assert.equal(result.children[0].requests[0].bodyType, "form-data", format);
    assert.equal(result.requests[0].bodyType, "json", format);
  }
});

test("creates and merges imported collections with folders and environments", () => {
  const idFactory = ids();
  const imported = load({
    info: { name: "Merge", schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
    variable: [{ key: "baseUrl", value: "https://x" }],
    item: [{ name: "A", item: [{ name: "One", request: { method: "GET", url: "https://x/one" } }] }],
  });
  const created = importedApiCollection(imported, idFactory, () => ({ id: "fallback", name: "Default", vars: [] }));
  assert.equal(created.children[0].name, "A");
  assert.equal(created.activeEnvironmentId, created.environments[0].id);

  const target = {
    id: "t",
    name: "Target",
    requests: [],
    children: [{ id: "a", name: "A", requests: [{ id: "keep", name: "Old", method: "GET", url: "https://x/one", params: [], headers: [], body: "", authType: "none", documentation: "", updatedAt: 1 }], children: [] }],
    environments: [{ id: "env", name: "Imported", vars: [] }],
    activeEnvironmentId: "env",
  };
  mergeImportedCollection(target, imported, idFactory);
  mergeImportedEnvironments(target, imported.environments);
  assert.equal(target.children.length, 1);
  assert.equal(target.children[0].requests.length, 1);
  assert.equal(target.children[0].requests[0].id, "keep");
  assert.equal(target.children[0].requests[0].name, "One");
  assert.equal(target.environments.length, 1, "same-name environment is not duplicated");
});

test("imports large collections and only rejects pathological nesting", () => {
  const schema = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json";
  const many = Array.from({ length: 20_000 }, (_, index) => ({ name: `r${index}`, request: { method: "GET", url: `https://x/${index}` } }));
  assert.equal(load({ info: { name: "many", schema }, item: many }).requestCount, 20_000);

  let item = [{ name: "leaf", request: { method: "GET", url: "https://x" } }];
  for (let depth = 0; depth < 100; depth += 1) item = [{ name: `f${depth}`, item }];
  assert.equal(load({ info: { name: "deep", schema }, item }).requestCount, 1);
  for (let depth = 100; depth < 300; depth += 1) item = [{ name: `f${depth}`, item }];
  assert.throws(() => load({ info: { name: "deeper", schema }, item }), /api_document_too_deep/);
  assert.equal(allRequests(load(OPENAPI_YAML, "x.yaml")).length, 3);
});
