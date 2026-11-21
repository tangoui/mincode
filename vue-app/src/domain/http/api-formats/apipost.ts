import type { HttpEnvironment } from "../request-collection";
import {
  bodyField,
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
import { splitUrlQuery } from "./export-safety";

/**
 * Apipost 原生项目导出（v7：{project, apis, envs}，apis 为嵌套树）。
 * 也兼容内部 v8 结构：apis 为扁平列表，用 target_id / parent_id 组树。
 */
export function isApipostProject(document: UnknownRecord) {
  return (
    isRecord(document.project) &&
    Array.isArray(document.apis) &&
    (Array.isArray(document.envs) || Array.isArray(document.env) || records(document.apis).some((item) => "target_type" in item))
  );
}

/** 参数行可能是 {parameter:[...]} 包装，也可能直接是数组。 */
function parameterRows(value: unknown) {
  if (Array.isArray(value)) return records(value);
  return isRecord(value) ? records(value.parameter) : [];
}

function checked(row: UnknownRecord) {
  const flag = row.is_checked;
  return flag === undefined || flag === 1 || flag === "1" || flag === true;
}

function importAuth(value: unknown): ImportedAuth | undefined {
  if (!isRecord(value)) return undefined;
  const type = text(value.type).toLowerCase();
  switch (type) {
    case "":
    case "inherit":
      return undefined;
    case "bearer": {
      const bearer = isRecord(value.bearer) ? value.bearer : {};
      return { type: "bearer", token: valueString(bearer.key ?? bearer.token) };
    }
    case "basic": {
      const basic = isRecord(value.basic) ? value.basic : {};
      return { type: "basic", username: valueString(basic.username), password: valueString(basic.password) };
    }
    case "kv":
    case "apikey": {
      const kv = isRecord(value.kv) ? value.kv : isRecord(value.apikey) ? value.apikey : {};
      return {
        type: "api-key",
        apiKeyName: valueString(kv.key),
        apiKeyValue: valueString(kv.value),
        apiKeyAddTo: kv.in === "query" ? "query" : "header",
      };
    }
    default:
      return { type: "none" };
  }
}

function importBody(value: unknown, idFactory: IdFactory): Partial<ImportedRequestInput> {
  if (!isRecord(value)) return { bodyType: "none" };
  const mode = text(value.mode).toLowerCase();
  if (mode === "form-data" || mode === "formdata" || mode === "urlencoded") {
    return {
      bodyType: mode === "urlencoded" ? "x-www-form-urlencoded" : "form-data",
      formBody: parameterRows(value.parameter)
        .filter((row) => text(row.key))
        .map((row) =>
          bodyField(
            idFactory,
            text(row.key),
            row.value,
            checked(row),
            text(row.type).toLowerCase() === "file" ? "file" : "text",
            text(row.description)
          )
        ),
    };
  }
  if (mode === "file" || mode === "binary") return { bodyType: "binary" };
  if (!mode || mode === "none") return { bodyType: "none" };
  const raw = valueString(value.raw);
  return { bodyType: mode === "raw" ? (/^\s*[[{]/.test(raw) ? "json" : "raw") : rawBodyType(mode), body: raw };
}

interface ApipostNode {
  item: UnknownRecord;
  children: ApipostNode[];
}

/** v8 扁平列表按 parent_id 组树；v7 本身就是嵌套的 children。 */
function buildTree(apis: UnknownRecord[]): ApipostNode[] {
  const nested = apis.some((item) => Array.isArray(item.children));
  if (nested || !apis.some((item) => "parent_id" in item)) {
    const convert = (items: UnknownRecord[], depth: number): ApipostNode[] => {
      if (depth > IMPORT_MAX_DEPTH) throw new Error("api_document_too_deep");
      return items.map((item) => ({ item, children: convert(records(item.children), depth + 1) }));
    };
    return convert(apis, 0);
  }
  const nodes = new Map<string, ApipostNode>();
  for (const item of apis) nodes.set(valueString(item.target_id), { item, children: [] });
  const roots: ApipostNode[] = [];
  for (const node of nodes.values()) {
    const parentId = valueString(node.item.parent_id);
    const parent = parentId && parentId !== "0" ? nodes.get(parentId) : undefined;
    // 父节点缺失或指向自身时挂到根上，避免环。
    if (parent && parent !== node) parent.children.push(node);
    else roots.push(node);
  }
  const sortKey = (node: ApipostNode) => Number(node.item.sort ?? 0);
  const sort = (list: ApipostNode[], depth: number) => {
    if (depth > IMPORT_MAX_DEPTH) throw new Error("api_document_too_deep");
    list.sort((left, right) => sortKey(left) - sortKey(right));
    for (const node of list) sort(node.children, depth + 1);
  };
  sort(roots, 0);
  return roots;
}

function variableEntries(value: unknown): Array<[string, unknown, boolean, string]> {
  if (Array.isArray(value)) {
    return records(value)
      .filter((item) => text(item.key ?? item.name))
      .map((item) => [text(item.key ?? item.name), item.current_value || item.value || "", true, text(item.description)]);
  }
  if (!isRecord(value)) return [];
  return Object.entries(value).map(([key, item]) =>
    isRecord(item)
      ? [key, item.current_value || item.value || "", true, text(item.description)]
      : [key, item, true, ""]
  );
}

export function importApipost(
  document: UnknownRecord,
  idFactory: IdFactory,
  options: { now: number; environmentName: string }
): ImportedApiCollection {
  const project = isRecord(document.project) ? document.project : {};
  // 前置 URL 由环境提供：有 pre_url / server_list 的环境时，相对地址前加 {{baseUrl}}。
  const rawEnvs = [...records(document.envs), ...records(document.env)];
  const global = isRecord(document.global) ? document.global : {};
  const v8Envs = records(global.envs);
  const hasBaseUrl =
    rawEnvs.some((env) => text(env.pre_url)) ||
    v8Envs.some((env) => records(env.server_list).some((server) => text(server.uri)));

  const walk = (nodes: ApipostNode[], folder: ImportedApiFolder, inherited: ImportedAuth | undefined) => {
    for (const { item, children } of nodes) {
      const targetType = text(item.target_type).toLowerCase();
      const request = isRecord(item.request) ? item.request : {};
      if (targetType === "folder") {
        const child = emptyFolder(text(item.name, "Folder"));
        walk(children, child, importAuth(request.auth) ?? inherited);
        folder.children.push(child);
        continue;
      }
      if (targetType !== "api" && targetType !== "graphql") continue;
      const restful = parameterRows(request.resful ?? request.restful);
      const pathValues = new Map(
        restful.filter((row) => text(row.key) && valueString(row.value)).map((row) => [text(row.key), valueString(row.value)])
      );
      const { base, query: urlQuery } = splitUrlQuery(text(item.url));
      const queryRows = parameterRows(request.query);
      const params = queryRows.length
        ? queryRows
            .filter((row) => text(row.key))
            .map((row) => keyValueRow(idFactory, "param", text(row.key), row.value, checked(row), text(row.description)))
        : urlQuery.map(([key, value]) => keyValueRow(idFactory, "param", key, value));
      const headers = parameterRows(request.header)
        .filter((row) => text(row.key))
        .map((row) => keyValueRow(idFactory, "header", text(row.key), row.value, checked(row), text(row.description)));
      const cookies = parameterRows(request.cookie).filter((row) => text(row.key) && checked(row));
      if (cookies.length) {
        headers.push(
          keyValueRow(idFactory, "header", "Cookie", cookies.map((row) => `${text(row.key)}=${valueString(row.value)}`).join("; "))
        );
      }
      const path = normalizePathVariables(base, pathValues);
      folder.requests.push(
        createImportedRequest(
          {
            name: text(item.name),
            method: text(item.method, "GET"),
            url: hasBaseUrl && !/^(https?:)?\/\//i.test(path) ? joinUrl("{{baseUrl}}", path) : path,
            params,
            headers,
            ...importBody(request.body, idFactory),
            auth: importAuth(request.auth) ?? inherited,
            documentation: text(request.description),
          },
          idFactory,
          options.now
        )
      );
    }
  };

  const root = emptyFolder(text(project.name, "Apipost"));
  walk(buildTree(records(document.apis)), root, importAuth(project.auth));

  const projectVars = variableEntries(project.variable);
  const globalVars = variableEntries(global.global_vars);
  const environments: HttpEnvironment[] = [
    ...rawEnvs.map((env) =>
      environment(idFactory, text(env.name, options.environmentName), [
        ...(text(env.pre_url) ? [["baseUrl", text(env.pre_url), true, ""] as [string, unknown, boolean, string]] : []),
        ...projectVars,
        ...variableEntries(env.list),
      ])
    ),
    ...v8Envs.map((env) => {
      const server = records(env.server_list).find((item) => text(item.uri));
      return environment(idFactory, text(env.name, options.environmentName), [
        ...(server ? [["baseUrl", text(server.uri), true, ""] as [string, unknown, boolean, string]] : []),
        ...globalVars,
        ...variableEntries(env.env_var_list),
      ]);
    }),
  ];
  if (!environments.length && (projectVars.length || globalVars.length)) {
    environments.push(environment(idFactory, options.environmentName, [...projectVars, ...globalVars]));
  }
  return { ...pruneFolder(root), source: "apipost", environments };
}
