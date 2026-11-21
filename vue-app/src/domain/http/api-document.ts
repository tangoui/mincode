import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import type { HttpEnvironment, HttpRequestCollection, SavedHttpRequest } from "./request-collection";
import { importApifox, isApifoxProject } from "./api-formats/apifox";
import { importApipost, isApipostProject } from "./api-formats/apipost";
import { exportOpenApiDocument, importOpenApi, isOpenApiDocument } from "./api-formats/openapi";
import {
  exportPostmanCollection,
  importPostman,
  importPostmanEnvironment,
  isPostmanCollection,
  isPostmanEnvironment,
} from "./api-formats/postman";
import {
  countImportedRequests,
  importedFolderToCollection,
  isRecord,
  type IdFactory,
  type ImportedApiCollection,
} from "./api-formats/shared";

export type { IdFactory, ImportedApiCollection, ImportedApiFolder, ApiImportSource } from "./api-formats/shared";
export { exportOpenApiDocument, exportPostmanCollection };

export type ApiDocumentFormat = "openapi-json" | "openapi-yaml" | "postman";

export interface ApiDocumentImportOptions {
  now?: number;
  /** 导入的环境没有名字时使用的名称（随界面语言）。 */
  environmentName?: string;
}

export type ApiDocumentImport =
  | ({ kind: "collection"; requestCount: number } & ImportedApiCollection)
  | { kind: "environment"; environment: HttpEnvironment };

function parseDocument(content: string, filename: string): unknown {
  const trimmed = content.replace(/^\uFEFF/, "").trim();
  try {
    // 扩展名不可靠（Apifox 导出 .apifox.json、有人把 YAML 存成 .txt），按内容判断。
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) return JSON.parse(trimmed);
    if (/\.json$/i.test(filename)) return JSON.parse(trimmed);
    // 别名上限只防“YAML 炸弹”（少量别名展开成海量数据），正常文档用不到这么多。
    return parseYaml(trimmed, { maxAliasCount: 10_000 });
  } catch {
    throw new Error("invalid_api_document");
  }
}

/** 识别 OpenAPI 3 / Swagger 2 / Postman v2.0-2.1（集合与环境）/ Apifox / Apipost 文件。 */
export function importApiDocument(
  content: string,
  filename: string,
  idFactory: IdFactory,
  options: ApiDocumentImportOptions = {}
): ApiDocumentImport {
  const parsed = parseDocument(content, filename);
  if (!isRecord(parsed)) throw new Error("invalid_api_document");
  const settings = { now: options.now ?? Date.now(), environmentName: options.environmentName ?? "Imported" };
  if (isPostmanEnvironment(parsed)) {
    return { kind: "environment", environment: importPostmanEnvironment(parsed, idFactory) };
  }
  let imported: ImportedApiCollection;
  if (isOpenApiDocument(parsed)) imported = importOpenApi(parsed, idFactory, settings);
  else if (isApifoxProject(parsed)) imported = importApifox(parsed, idFactory, settings);
  else if (isApipostProject(parsed)) imported = importApipost(parsed, idFactory, settings);
  else if (isPostmanCollection(parsed)) imported = importPostman(parsed, idFactory, settings);
  else if (isRecord(parsed.info) && Array.isArray(parsed.item)) throw new Error("unsupported_postman_version");
  else throw new Error("unsupported_api_document");
  return { kind: "collection", requestCount: countImportedRequests(imported), ...imported };
}

/** 新建集合：保留目录结构与环境。 */
export function importedApiCollection(
  imported: ImportedApiCollection,
  idFactory: IdFactory,
  fallbackEnvironment: () => HttpEnvironment
): HttpRequestCollection {
  const environments = imported.environments.length ? imported.environments : [fallbackEnvironment()];
  return {
    ...importedFolderToCollection(imported, idFactory),
    environments,
    activeEnvironmentId: environments[0]!.id,
  };
}

function requestIdentity(request: SavedHttpRequest) {
  let normalized = request.url.trim();
  try {
    const url = new URL(normalized);
    url.search = "";
    url.hash = "";
    normalized = url.toString().replace(/\/$/, "");
  } catch {
    normalized = normalized.split(/[?#]/)[0]!.replace(/\/$/, "");
  }
  return `${request.method.toUpperCase()} ${normalized}`;
}

export function mergeImportedRequests(existing: SavedHttpRequest[], incoming: SavedHttpRequest[]) {
  const merged = existing.map((request) => ({ ...request }));
  const indexes = new Map(merged.map((request, index) => [requestIdentity(request), index]));
  for (const request of incoming) {
    const index = indexes.get(requestIdentity(request));
    if (index === undefined) {
      indexes.set(requestIdentity(request), merged.length);
      merged.push(request);
    } else {
      merged[index] = { ...request, id: merged[index]!.id };
    }
  }
  return merged;
}

/**
 * 合并到已有集合：同名目录递归合并，请求按“方法 + 地址”去重更新。
 * 导入的环境中，目标集合没有的同名环境追加进去（只有顶级集合持有环境）。
 */
export function mergeImportedCollection(
  target: HttpRequestCollection,
  imported: ImportedApiCollection,
  idFactory: IdFactory
) {
  const mergeFolder = (into: HttpRequestCollection, folder: ImportedApiCollection | ImportedApiCollection["children"][number]) => {
    into.requests = mergeImportedRequests(into.requests, folder.requests);
    into.children ??= [];
    for (const child of folder.children) {
      const existing = into.children.find((item) => item.name === child.name);
      if (existing) mergeFolder(existing, child);
      else into.children.push(importedFolderToCollection(child, idFactory));
    }
  };
  mergeFolder(target, imported);
}

export function mergeImportedEnvironments(target: HttpRequestCollection, environments: readonly HttpEnvironment[]) {
  if (!environments.length) return;
  const names = new Set((target.environments ?? []).map((item) => item.name));
  target.environments = [...(target.environments ?? []), ...environments.filter((item) => !names.has(item.name))];
  target.activeEnvironmentId ??= target.environments[0]?.id;
}

export function serializeApiDocument(
  collection: HttpRequestCollection,
  format: ApiDocumentFormat,
  activeEnvironment?: HttpEnvironment
) {
  if (format === "postman") return JSON.stringify(exportPostmanCollection(collection, activeEnvironment), null, 2);
  const document = exportOpenApiDocument(collection, activeEnvironment);
  return format === "openapi-yaml" ? stringifyYaml(document) : JSON.stringify(document, null, 2);
}
