import {
  buildHttpRequestHeaders,
  buildHttpRequestUrl,
  resolveHttpRequestBodyType,
} from "./curl";
import type { HttpBodyFieldType, HttpBodyType, HttpRequestDraft } from "./request-collection";

export interface NativeHttpFormField {
  name: string;
  value: string;
  fieldType: HttpBodyFieldType;
  fileName?: string;
  contentType?: string;
}

export interface NativeHttpRequestPayload {
  method: string;
  url: string;
  headers: [string, string][];
  body?: string;
  bodyType: HttpBodyType;
  formFields?: NativeHttpFormField[];
  binaryFilePath?: string;
  timeoutMs: number;
}

export const resolveHttpBodyType = resolveHttpRequestBodyType;

export function buildNativeHttpRequestPayload(
  request: HttpRequestDraft,
  expand: (value: string) => string = (value) => value
): NativeHttpRequestPayload {
  const bodyType = resolveHttpBodyType(request);
  const expandedRequest = {
    ...request,
    url: expand(request.url.trim()),
    params: request.params.map((row) => ({ ...row, value: expand(row.value) })),
    headers: request.headers.map((row) => ({ ...row, value: expand(row.value) })),
    authToken: expand(request.authToken),
    authUsername: expand(request.authUsername ?? ""),
    authPassword: expand(request.authPassword ?? ""),
    apiKeyName: expand(request.apiKeyName ?? ""),
    apiKeyValue: expand(request.apiKeyValue ?? ""),
    oauth2AccessToken: expand(request.oauth2AccessToken ?? ""),
  };
  const url = buildHttpRequestUrl(expandedRequest);
  const headers = buildHttpRequestHeaders(expandedRequest);

  const payload: NativeHttpRequestPayload = {
    method: request.method.trim().toUpperCase() || "GET",
    url,
    headers,
    bodyType,
    timeoutMs: 30_000,
  };

  if (["json", "xml", "text", "html", "javascript", "raw"].includes(bodyType)) {
    if (request.body) payload.body = expand(request.body);
  } else if (bodyType === "graphql") {
    const variablesText = expand(request.graphqlVariables ?? "").trim();
    let variables: unknown = undefined;
    if (variablesText) {
      try {
        variables = JSON.parse(variablesText);
      } catch {
        variables = variablesText;
      }
    }
    payload.body = JSON.stringify({
      query: expand(request.graphqlQuery ?? ""),
      ...(variables === undefined ? {} : { variables }),
    });
  } else if (bodyType === "x-www-form-urlencoded") {
    payload.body = new URLSearchParams(
      (request.formBody ?? [])
        .filter((row) => row.enabled && row.key.trim())
        .map((row) => [expand(row.key.trim()), expand(row.value)])
    ).toString();
  } else if (bodyType === "form-data") {
    payload.formFields = (request.formBody ?? [])
      .filter((row) => row.enabled && row.key.trim())
      .map((row) => ({
        name: expand(row.key.trim()),
        value: row.fieldType === "file" ? row.value : expand(row.value),
        fieldType: row.fieldType,
        ...(row.fileName ? { fileName: row.fileName } : {}),
        ...(row.contentType?.trim() ? { contentType: row.contentType.trim() } : {}),
      }));
  } else if (bodyType === "binary" && request.binaryFilePath) {
    payload.binaryFilePath = request.binaryFilePath;
  }

  return payload;
}
