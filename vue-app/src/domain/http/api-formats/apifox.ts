import type { HttpEnvironment } from "../request-collection";
import {
  bodyField,
  bodyText,
  createImportedRequest,
  emptyFolder,
  environment,
  IMPORT_MAX_DEPTH,
  isRecord,
  joinUrl,
  keyValueRow,
  normalizePathVariables,
  pruneFolder,
  rawBodyType,
  records,
  text,
  valueString,
  type IdFactory,
  type ImportedApiCollection,
  type ImportedApiFolder,
  type ImportedAuth,
  type ImportedRequestInput,
  type UnknownRecord,
} from "./shared";

/** Apifox「Apifox 格式」项目导出（*.apifox.json）。 */
export function isApifoxProject(document: UnknownRecord) {
  const schema = isRecord(document.$schema) ? document.$schema : {};
  return typeof document.apifoxProject === "string" || schema.app === "apifox";
}

/** 参数行的启用标志：缺省视为启用。 */
function enabled(row: UnknownRecord) {
  return row.enable !== false && row.enabled !== false;
}

function rowValue(row: UnknownRecord) {
  return row.example ?? row.value ?? row.default ?? "";
}

function importAuth(value: unknown): ImportedAuth | undefined {
  if (!isRecord(value) || !text(value.type)) return undefined;
  const type = text(value.type).toLowerCase();
  const params = isRecord(value[type]) ? value[type] : {};
  switch (type) {
    case "inherit":
      return undefined;
    case "bearer":
    case "jwt":
      return { type: "bearer", token: valueString(params.token) };
    case "basic":
      return { type: "basic", username: valueString(params.username), password: valueString(params.password) };
    case "apikey":
      return {
        type: "api-key",
        apiKeyName: valueString(params.key),
        apiKeyValue: valueString(params.value),
        apiKeyAddTo: params.in === "query" ? "query" : "header",
      };
    default:
      return { type: "none" };
  }
}

function importBody(value: unknown, idFactory: IdFactory): Partial<ImportedRequestInput> {
  if (!isRecord(value)) return { bodyType: "none" };
  const type = text(value.type).toLowerCase();
  if (!type || type === "none") return { bodyType: "none" };
  if (type === "multipart/form-data" || type === "application/x-www-form-urlencoded") {
    return {
      bodyType: type === "multipart/form-data" ? "form-data" : "x-www-form-urlencoded",
      formBody: records(value.parameters)
        .filter((row) => text(row.name))
        .map((row) =>
          bodyField(idFactory, text(row.name), rowValue(row), enabled(row), row.type === "file" ? "file" : "text", text(row.description))
        ),
    };
  }
  if (type === "application/octet-stream") return { bodyType: "binary" };
  if (type === "graphql") {
    const graphql = isRecord(value.graphql) ? value.graphql : {};
    return { bodyType: "graphql", graphqlQuery: text(graphql.query), graphqlVariables: bodyText(graphql.variables) };
  }
  const examples = records(value.examples);
  const example = examples.find((item) => text(item.mediaType) === type) ?? examples[0];
  const raw = example ? example.value : (value.example ?? value.data ?? "");
  return { bodyType: rawBodyType(type), body: bodyText(raw) };
}

/** 环境的 baseUrls 以 serverId 区分；导出的请求使用 {{baseUrl}} 或 {{baseUrl_<serverId>}}。 */
function baseUrlVariable(serverId: string) {
  return !serverId || serverId === "default" ? "baseUrl" : `baseUrl_${serverId}`;
}

export function importApifox(
  document: UnknownRecord,
  idFactory: IdFactory,
  options: { now: number; environmentName: string }
): ImportedApiCollection {
  const info = isRecord(document.info) ? document.info : {};
  const environments: HttpEnvironment[] = [];
  const serverIds = new Set<string>(["default"]);

  const walk = (
    items: unknown,
    folder: ImportedApiFolder,
    inheritedAuth: ImportedAuth | undefined,
    inheritedServer: string,
    depth: number
  ) => {
    if (depth > IMPORT_MAX_DEPTH) throw new Error("api_document_too_deep");
    for (const item of records(items)) {
      const api = isRecord(item.api) ? item.api : undefined;
      if (!api) {
        if (!Array.isArray(item.items)) continue;
        const child = emptyFolder(text(item.name, "Folder"));
        walk(item.items, child, importAuth(item.auth) ?? inheritedAuth, text(item.serverId) || inheritedServer, depth + 1);
        folder.children.push(child);
        continue;
      }
      const parameters = isRecord(api.parameters) ? api.parameters : {};
      const pathValues = new Map(
        records(parameters.path)
          .filter((row) => text(row.name) && valueString(rowValue(row)))
          .map((row) => [text(row.name), valueString(rowValue(row))])
      );
      const serverId = text(api.serverId) || inheritedServer;
      serverIds.add(serverId || "default");
      const path = normalizePathVariables(text(api.path), pathValues);
      const cookies = records(parameters.cookie).filter((row) => text(row.name) && enabled(row));
      const headers = records(parameters.header)
        .filter((row) => text(row.name))
        .map((row) => keyValueRow(idFactory, "header", text(row.name), rowValue(row), enabled(row), text(row.description)));
      if (cookies.length) {
        headers.push(
          keyValueRow(
            idFactory,
            "header",
            "Cookie",
            cookies.map((row) => `${text(row.name)}=${valueString(rowValue(row))}`).join("; ")
          )
        );
      }
      folder.requests.push(
        createImportedRequest(
          {
            name: text(item.name, text(api.name)),
            method: text(api.method, "GET"),
            url: /^(https?:)?\/\//i.test(path) ? path : joinUrl(`{{${baseUrlVariable(serverId)}}}`, path),
            params: records(parameters.query)
              .filter((row) => text(row.name))
              .map((row) => keyValueRow(idFactory, "param", text(row.name), rowValue(row), enabled(row), text(row.description))),
            headers,
            ...importBody(api.requestBody, idFactory),
            auth: importAuth(api.auth) ?? inheritedAuth,
            documentation: text(api.description),
          },
          idFactory,
          options.now
        )
      );
    }
  };

  const root = emptyFolder(text(info.name, "Apifox"));
  const collections = records(document.apiCollection);
  // 第一层通常只有一个「根目录」，展开后直接挂到集合下。
  for (const collection of collections) {
    walk(collection.items, root, importAuth(collection.auth), text(collection.serverId), 0);
  }

  const globals = records(document.globalVariables).flatMap((group) => records(group.variables));
  for (const item of records(document.environments)) {
    const baseUrls = isRecord(item.baseUrls) ? item.baseUrls : {};
    const vars: Array<[string, unknown, boolean, string]> = [];
    for (const serverId of serverIds) {
      const url = valueString(baseUrls[serverId] ?? (serverId === "default" ? item.baseUrl : ""));
      if (url) vars.push([baseUrlVariable(serverId), /^[a-z]+:\/\//i.test(url) || url.startsWith("{{") ? url : `http://${url}`, true, ""]);
    }
    for (const variable of [...globals, ...records(item.variables)]) {
      const name = text(variable.name);
      if (name) vars.push([name, variable.value || variable.initialValue || "", true, text(variable.description)]);
    }
    environments.push(environment(idFactory, text(item.name, options.environmentName), vars));
  }
  if (!environments.length && globals.length) {
    environments.push(
      environment(
        idFactory,
        options.environmentName,
        globals.map((variable): [string, unknown, boolean, string] => [
          text(variable.name),
          variable.value || variable.initialValue || "",
          true,
          text(variable.description),
        ])
      )
    );
  }
  return { ...pruneFolder(root), source: "apifox", environments };
}
