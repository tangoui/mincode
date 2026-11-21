import type {
  HttpAuthType,
  HttpBodyField,
  HttpBodyType,
  HttpKeyValueRow,
  HttpRequestDraft,
} from "./request-collection";

export interface ParsedCurlRequest {
  method: string;
  url: string;
  params: HttpKeyValueRow[];
  headers: HttpKeyValueRow[];
  body: string;
  bodyType: HttpBodyType;
  formBody: HttpBodyField[];
  binaryFilePath: string;
  binaryFileName: string;
  authType: HttpAuthType;
  authToken: string;
  authUsername: string;
  authPassword: string;
}

export type HttpRowIdFactory = (kind: "param" | "header") => string;

const CURL_OPTIONS_WITH_VALUE = new Set([
  "-m",
  "--max-time",
  "--connect-timeout",
  "--retry",
  "--retry-delay",
  "--retry-max-time",
  "-x",
  "--proxy",
  "--proxy-user",
  "--cert",
  "--key",
  "--cacert",
  "--capath",
  "--resolve",
  "--interface",
  "-o",
  "--output",
  "-T",
  "--upload-file",
  "--limit-rate",
]);

let curlRowSequence = 0;

function createDefaultRowId(kind: "param" | "header") {
  curlRowSequence += 1;
  return `curl-${kind}-${Date.now()}-${curlRowSequence}`;
}

function decodeBase64(value: string) {
  try {
    return atob(value);
  } catch {
    return "";
  }
}

function encodeBase64(value: string) {
  try {
    const bytes = new TextEncoder().encode(value);
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary);
  } catch {
    return "";
  }
}

function shellQuote(value: string) {
  return `'${value.replace(/'/g, `'"'"'`)}'`;
}

function activeRows<T extends HttpKeyValueRow>(rows: T[] | undefined) {
  return (rows ?? []).filter((row) => row.enabled && row.key.trim());
}

function appendQuery(baseUrl: string, query: string) {
  if (!query) return baseUrl;
  const hashIndex = baseUrl.indexOf("#");
  const beforeHash = hashIndex >= 0 ? baseUrl.slice(0, hashIndex) : baseUrl;
  const hash = hashIndex >= 0 ? baseUrl.slice(hashIndex) : "";
  const separator = beforeHash.includes("?") ? "&" : "?";
  return `${beforeHash}${separator}${query}${hash}`;
}

const BODY_CONTENT_TYPES: Partial<Record<HttpBodyType, string>> = {
  json: "application/json",
  xml: "application/xml",
  text: "text/plain",
  html: "text/html",
  javascript: "application/javascript",
  "x-www-form-urlencoded": "application/x-www-form-urlencoded",
  graphql: "application/json",
};

export function resolveHttpRequestBodyType(
  request: Pick<HttpRequestDraft, "body" | "bodyType">
): HttpBodyType {
  return request.bodyType ?? (request.body ? "raw" : "none");
}

export function buildHttpRequestBody(request: HttpRequestDraft): string | undefined {
  const bodyType = resolveHttpRequestBodyType(request);
  if (["json", "xml", "text", "html", "javascript", "raw"].includes(bodyType)) {
    return request.body || undefined;
  }
  if (bodyType === "graphql") {
    const variablesText = request.graphqlVariables?.trim() ?? "";
    let variables: unknown;
    if (variablesText) {
      try {
        variables = JSON.parse(variablesText);
      } catch {
        variables = variablesText;
      }
    }
    return JSON.stringify({
      query: request.graphqlQuery ?? "",
      ...(variables === undefined ? {} : { variables }),
    });
  }
  if (bodyType === "x-www-form-urlencoded") {
    return new URLSearchParams(
      activeRows(request.formBody).map((row) => [row.key.trim(), row.value])
    ).toString();
  }
  return undefined;
}

export function buildHttpRequestUrl(
  request: Pick<HttpRequestDraft, "url" | "params"> &
    Partial<Pick<HttpRequestDraft, "authType" | "apiKeyName" | "apiKeyValue" | "apiKeyAddTo">>
) {
  const rows = activeRows(request.params).map((row) => [row.key.trim(), row.value] as [string, string]);
  if (
    request.authType === "api-key" &&
    request.apiKeyAddTo === "query" &&
    request.apiKeyName?.trim() &&
    request.apiKeyValue
  ) {
    rows.push([request.apiKeyName.trim(), request.apiKeyValue]);
  }
  return appendQuery(request.url.trim(), new URLSearchParams(rows).toString());
}

export function buildHttpRequestHeaders(request: HttpRequestDraft): [string, string][] {
  const headers = activeRows(request.headers).map(
    (row) => [row.key.trim(), row.value] as [string, string]
  );
  const hasHeader = (name: string) =>
    headers.some(([key]) => key.trim().toLowerCase() === name.toLowerCase());
  const addHeader = (name: string, value: string) => {
    if (!hasHeader(name) && name.trim() && value) headers.push([name.trim(), value]);
  };
  const token = request.authToken.trim();
  if (request.authType === "bearer" && token) addHeader("Authorization", `Bearer ${token}`);
  if (request.authType === "basic") {
    if (request.authUsername || request.authPassword) {
      addHeader(
        "Authorization",
        `Basic ${encodeBase64(`${request.authUsername ?? ""}:${request.authPassword ?? ""}`)}`
      );
    } else if (token) {
      addHeader("Authorization", `Basic ${token}`);
    }
  }
  if (
    request.authType === "api-key" &&
    request.apiKeyAddTo !== "query" &&
    request.apiKeyName?.trim() &&
    request.apiKeyValue
  ) {
    addHeader(request.apiKeyName.trim(), request.apiKeyValue);
  }
  if (request.authType === "oauth2" && request.oauth2AccessToken) {
    addHeader("Authorization", `Bearer ${request.oauth2AccessToken}`);
  }
  const contentType = BODY_CONTENT_TYPES[resolveHttpRequestBodyType(request)];
  if (contentType) addHeader("Content-Type", contentType);
  return headers;
}

export function tokenizeCurlCommand(command: string): string[] {
  const tokens: string[] = [];
  const source = command.replace(/\\\r?\n/g, " ").trim();
  let current = "";
  let quote: "'" | '"' | undefined;
  let tokenStarted = false;

  for (let index = 0; index < source.length; index += 1) {
    const character = source[index]!;
    if (quote) {
      if (character === quote) {
        quote = undefined;
      } else if (character === "\\" && quote === '"' && index + 1 < source.length) {
        index += 1;
        current += source[index];
      } else {
        current += character;
      }
      tokenStarted = true;
      continue;
    }

    if (character === "'" || character === '"') {
      quote = character;
      tokenStarted = true;
    } else if (/\s/.test(character)) {
      if (tokenStarted) {
        tokens.push(current);
        current = "";
        tokenStarted = false;
      }
    } else if (character === "\\" && index + 1 < source.length) {
      index += 1;
      current += source[index];
      tokenStarted = true;
    } else {
      current += character;
      tokenStarted = true;
    }
  }

  if (tokenStarted) tokens.push(current);
  return tokens;
}

function splitUrl(rawUrl: string, createId: HttpRowIdFactory) {
  const hashIndex = rawUrl.indexOf("#");
  const beforeHash = hashIndex >= 0 ? rawUrl.slice(0, hashIndex) : rawUrl;
  const hash = hashIndex >= 0 ? rawUrl.slice(hashIndex) : "";
  const queryIndex = beforeHash.indexOf("?");
  if (queryIndex < 0) return { url: rawUrl, params: [] as HttpKeyValueRow[] };

  const params = Array.from(new URLSearchParams(beforeHash.slice(queryIndex + 1))).map(
    ([key, value]) => ({ id: createId("param"), key, value, enabled: true })
  );
  return { url: `${beforeHash.slice(0, queryIndex)}${hash}`, params };
}

function optionValue(token: string, longName: string) {
  const prefix = `${longName}=`;
  return token.startsWith(prefix) ? token.slice(prefix.length) : undefined;
}

function formRows(value: string, createId: HttpRowIdFactory) {
  return Array.from(new URLSearchParams(value)).map(([key, rowValue]) => ({
    id: createId("param"),
    key,
    value: rowValue,
    enabled: true,
  }));
}

function bodyField(value: string, createId: HttpRowIdFactory): HttpBodyField | undefined {
  const separator = value.indexOf("=");
  if (separator <= 0) return undefined;
  const key = value.slice(0, separator).trim();
  const fieldValue = value.slice(separator + 1);
  const isFile = fieldValue.startsWith("@");
  const typeMarker = isFile ? fieldValue.match(/;type=([^;]+)$/i) : undefined;
  const rawValue = typeMarker ? fieldValue.slice(0, typeMarker.index) : fieldValue;
  const filePath = isFile ? rawValue.slice(1) : rawValue;
  return {
    id: createId("param"),
    key,
    value: filePath,
    enabled: true,
    fieldType: isFile ? "file" : "text",
    ...(isFile ? { fileName: filePath.split(/[\\/]/).pop() || filePath } : {}),
    ...(typeMarker ? { contentType: typeMarker[1] } : {}),
  };
}

function hasHeader(headers: HttpKeyValueRow[], key: string, pattern?: RegExp) {
  return headers.some(
    (row) =>
      row.key.trim().toLowerCase() === key.toLowerCase() && (!pattern || pattern.test(row.value))
  );
}

export function parseCurlCommand(
  command: string,
  createId: HttpRowIdFactory = createDefaultRowId
): ParsedCurlRequest | null {
  const tokens = tokenizeCurlCommand(command);
  const executable = tokens[0]?.split(/[\\/]/).pop()?.toLowerCase();
  if (executable !== "curl") return null;

  let method = "GET";
  let rawUrl = "";
  let authType: HttpAuthType = "none";
  let authToken = "";
  let authUsername = "";
  let authPassword = "";
  let forceGet = false;
  let binaryFilePath = "";
  const headers: HttpKeyValueRow[] = [];
  const dataParts: string[] = [];
  const encodedParts: string[] = [];
  const formBody: HttpBodyField[] = [];

  const takeNext = (index: number) => tokens[index + 1] ?? "";
  const addHeader = (header: string) => {
    const separator = header.indexOf(":");
    if (separator <= 0) return;
    const key = header.slice(0, separator).trim();
    const value = header.slice(separator + 1).trim();
    if (key.toLowerCase() === "authorization" && /^bearer\s+/i.test(value)) {
      authType = "bearer";
      authToken = value.replace(/^bearer\s+/i, "");
    } else if (key.toLowerCase() === "authorization" && /^basic\s+/i.test(value)) {
      authType = "basic";
      authToken = value.replace(/^basic\s+/i, "");
    } else {
      headers.push({ id: createId("header"), key, value, enabled: true });
    }
  };

  for (let index = 1; index < tokens.length; index += 1) {
    const token = tokens[index]!;
    const requestValue = optionValue(token, "--request");
    const headerValue = optionValue(token, "--header");
    const urlValue = optionValue(token, "--url");
    const userValue = optionValue(token, "--user");
    const formValue = optionValue(token, "--form") ?? optionValue(token, "--form-string");
    const rawDataValue = ["--data", "--data-raw"]
      .map((option) => optionValue(token, option))
      .find((value) => value !== undefined);
    const binaryDataValue = optionValue(token, "--data-binary");
    const encodedDataValue = optionValue(token, "--data-urlencode");

    if (token === "-X" || token === "--request") {
      method = takeNext(index).toUpperCase() || method;
      index += 1;
    } else if (/^-X.+/.test(token)) {
      method = token.slice(2).toUpperCase() || method;
    } else if (requestValue !== undefined) {
      method = requestValue.toUpperCase() || method;
    } else if (token === "-H" || token === "--header") {
      addHeader(takeNext(index));
      index += 1;
    } else if (headerValue !== undefined) {
      addHeader(headerValue);
    } else if (["-F", "--form", "--form-string"].includes(token)) {
      const field = bodyField(takeNext(index), createId);
      if (field) formBody.push(field);
      index += 1;
    } else if (/^-F.+/.test(token)) {
      const field = bodyField(token.slice(2), createId);
      if (field) formBody.push(field);
    } else if (formValue !== undefined) {
      const field = bodyField(formValue, createId);
      if (field) formBody.push(field);
    } else if (["-d", "--data", "--data-raw"].includes(token)) {
      dataParts.push(takeNext(index));
      index += 1;
    } else if (/^-d.+/.test(token)) {
      dataParts.push(token.slice(2));
    } else if (rawDataValue !== undefined) {
      dataParts.push(rawDataValue);
    } else if (token === "--data-urlencode") {
      encodedParts.push(takeNext(index));
      index += 1;
    } else if (encodedDataValue !== undefined) {
      encodedParts.push(encodedDataValue);
    } else if (token === "--data-binary") {
      const value = takeNext(index);
      if (value.startsWith("@")) binaryFilePath = value.slice(1);
      else dataParts.push(value);
      index += 1;
    } else if (binaryDataValue !== undefined) {
      if (binaryDataValue.startsWith("@")) binaryFilePath = binaryDataValue.slice(1);
      else dataParts.push(binaryDataValue);
    } else if (token === "-u" || token === "--user") {
      const credentials = takeNext(index);
      const separator = credentials.indexOf(":");
      authType = "basic";
      authToken = encodeBase64(credentials);
      authUsername = separator >= 0 ? credentials.slice(0, separator) : credentials;
      authPassword = separator >= 0 ? credentials.slice(separator + 1) : "";
      index += 1;
    } else if (userValue !== undefined) {
      const separator = userValue.indexOf(":");
      authType = "basic";
      authToken = encodeBase64(userValue);
      authUsername = separator >= 0 ? userValue.slice(0, separator) : userValue;
      authPassword = separator >= 0 ? userValue.slice(separator + 1) : "";
    } else if (token === "-G" || token === "--get") {
      forceGet = true;
      method = "GET";
    } else if (token === "--url") {
      rawUrl = takeNext(index);
      index += 1;
    } else if (urlValue !== undefined) {
      rawUrl = urlValue;
    } else if (token === "-A" || token === "--user-agent") {
      headers.push({
        id: createId("header"),
        key: "User-Agent",
        value: takeNext(index),
        enabled: true,
      });
      index += 1;
    } else if (token === "-e" || token === "--referer") {
      headers.push({
        id: createId("header"),
        key: "Referer",
        value: takeNext(index),
        enabled: true,
      });
      index += 1;
    } else if (token === "-b" || token === "--cookie") {
      headers.push({
        id: createId("header"),
        key: "Cookie",
        value: takeNext(index),
        enabled: true,
      });
      index += 1;
    } else if (CURL_OPTIONS_WITH_VALUE.has(token)) {
      index += 1;
    } else if (!token.startsWith("-") && !rawUrl) {
      rawUrl = token;
    }
  }

  if (!rawUrl) return null;
  const split = splitUrl(rawUrl, createId);
  const rawData = dataParts.join("&");
  const encodedData = encodedParts.join("&");
  const queryData = [rawData, encodedData].filter(Boolean).join("&");
  const params = forceGet && queryData ? [...split.params, ...formRows(queryData, createId)] : split.params;
  let body = forceGet ? "" : rawData;
  let bodyType: HttpBodyType = "none";
  let normalizedFormBody = formBody;

  if (formBody.length) {
    bodyType = "form-data";
  } else if (binaryFilePath) {
    bodyType = "binary";
  } else if (!forceGet && encodedData) {
    bodyType = "x-www-form-urlencoded";
    normalizedFormBody = formRows(encodedData, createId).map((row) => ({
      ...row,
      fieldType: "text" as const,
    }));
    body = "";
  } else if (body) {
    bodyType =
      hasHeader(headers, "Content-Type", /application\/json/i) ||
      body.trimStart().startsWith("{") ||
      body.trimStart().startsWith("[")
        ? "json"
        : "raw";
  }
  if (bodyType !== "none" && method === "GET" && !forceGet) method = "POST";

  return {
    method,
    url: split.url,
    params,
    headers,
    body,
    bodyType,
    formBody: normalizedFormBody,
    binaryFilePath,
    binaryFileName: binaryFilePath.split(/[\\/]/).pop() || "",
    authType,
    authToken,
    authUsername,
    authPassword,
  };
}

export function serializeCurlCommand(request: HttpRequestDraft) {
  const parts = ["curl"];
  const method = request.method.trim().toUpperCase() || "GET";
  const bodyType = resolveHttpRequestBodyType(request);
  if (method !== "GET") parts.push(`-X ${method}`);
  parts.push(shellQuote(buildHttpRequestUrl(request)));
  const useBasicUser =
    request.authType === "basic" && Boolean(request.authUsername || request.authPassword);
  if (useBasicUser) {
    parts.push(`-u ${shellQuote(`${request.authUsername ?? ""}:${request.authPassword ?? ""}`)}`);
  }
  for (const [key, value] of buildHttpRequestHeaders(request)) {
    if (useBasicUser && key.toLowerCase() === "authorization") continue;
    parts.push(`-H ${shellQuote(`${key}: ${value}`)}`);
  }
  if (bodyType === "form-data") {
    for (const row of activeRows(request.formBody)) {
      const value =
        row.fieldType === "file"
          ? `@${row.value}${row.contentType?.trim() ? `;type=${row.contentType.trim()}` : ""}`
          : row.value;
      parts.push(`-F ${shellQuote(`${row.key.trim()}=${value}`)}`);
    }
  } else if (bodyType === "x-www-form-urlencoded") {
    for (const row of activeRows(request.formBody)) {
      parts.push(`--data-urlencode ${shellQuote(`${row.key.trim()}=${row.value}`)}`);
    }
  } else if (bodyType === "binary" && request.binaryFilePath) {
    parts.push(`--data-binary ${shellQuote(`@${request.binaryFilePath}`)}`);
  } else {
    const body = buildHttpRequestBody(request);
    if (body !== undefined) parts.push(`--data-raw ${shellQuote(body)}`);
  }
  return parts.join(" \\\n  ");
}

export function decodeBasicAuthorization(token: string) {
  return decodeBase64(token);
}
