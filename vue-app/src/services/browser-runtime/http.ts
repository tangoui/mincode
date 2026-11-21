/**
 * 浏览器本地模式的 `http_send_request`：用 fetch 代替 backend/http/http.go。
 *
 * 输入输出与 Go 命令一致（`{ payload: NativeHttpRequestPayload }` → `BrowserHttpResponse`）：
 * 超时取 `payload.timeoutMs`（缺省 30 秒、上限 5 分钟），响应体按文本返回，超过 10 MB 截断并加
 * 与 Go 相同的标注，`size_bytes` 为实际读取的字节数。与桌面端不同的地方都要让用户看到：
 * - 浏览器禁止脚本设置的请求头不发送，名称写进 `browser_notes`；
 * - GET / HEAD 不能带请求体，请求体被忽略并写进 `browser_notes`；
 * - Go 只允许同源、不降级的重定向（redirect.go），fetch 跨域跳转只剥离 Authorization：请求带非
 *   CORS 安全头或请求体时改用 `redirect: "manual"`，遇到重定向报 `http.browser.redirectBlocked`；
 *   其余请求照常跟随，跟随后若从 https 降级到 http 则写进 `browser_notes`；
 * - URL 中的 `user:pass@` 与 Go net/http 一样转成 `Authorization: Basic`（fetch 不接受带凭据的 URL）；
 * - 目标不允许跨域或网络不可达时 fetch 抛 TypeError，转成 `http.browser.corsError`；
 * - 只能读到目标通过 CORS 公开的响应头。
 *
 * 抛出的 `Error.message` 若是 `http.browser.*` 文案键，由视图翻译后展示。
 */
import type { NativeHttpFormField, NativeHttpRequestPayload } from "@/domain/http/native-request";
import { useVueI18n } from "@/i18n/messages";
import { browserFile } from "./files";
import { registerBrowserHandler, registerBrowserLocalFeature } from "./registry";

/** 与 Go 的 `http_send_request` 返回一致，另加浏览器专有的提示。 */
export interface BrowserHttpResponse {
  status: number;
  status_text: string;
  headers: [string, string][];
  body: string;
  elapsed_ms: number;
  size_bytes: number;
  /** 浏览器与桌面端行为不同之处的说明（已按当前语言翻译），视图在响应区提示。 */
  browser_notes: string[];
}

/** Fetch 规范的 forbidden request header 名单（小写）；另有 `proxy-`、`sec-` 前缀。 */
export const BROWSER_FORBIDDEN_HEADERS: ReadonlySet<string> = new Set([
  "accept-charset",
  "accept-encoding",
  "access-control-request-headers",
  "access-control-request-method",
  "connection",
  "content-length",
  "cookie",
  "cookie2",
  "date",
  "dnt",
  "expect",
  "host",
  "keep-alive",
  "origin",
  "referer",
  "set-cookie",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "via",
]);
const FORBIDDEN_HEADER_PREFIXES = ["proxy-", "sec-"];

/** 与 Go `httpMaxBody` 一致。 */
export const BROWSER_HTTP_MAX_BODY = 10 * 1024 * 1024;
/** 与 Go `httpMaxUploadBody` 一致：单个上传文件与 multipart 合计的上限。 */
const MAX_UPLOAD_BODY = 25 * 1024 * 1024;
const TRUNCATION_MARKER = "\n... (response truncated)";
const DEFAULT_TIMEOUT_MS = 30_000;
/** CORS 安全请求头：只带这些头且无请求体时，跟随重定向不会转发用户的敏感头。 */
const CORS_SAFELISTED_HEADERS: ReadonlySet<string> = new Set(["accept", "accept-language", "content-language", "content-type"]);

/** 与 Go `http.StatusText` 一致；HTTP/2 没有原因短语，fetch 的 statusText 为空时用它补齐。 */
const STATUS_TEXT: Readonly<Record<number, string>> = {
  100: "Continue", 101: "Switching Protocols", 102: "Processing", 103: "Early Hints",
  200: "OK", 201: "Created", 202: "Accepted", 203: "Non-Authoritative Information", 204: "No Content",
  205: "Reset Content", 206: "Partial Content", 207: "Multi-Status", 208: "Already Reported", 226: "IM Used",
  300: "Multiple Choices", 301: "Moved Permanently", 302: "Found", 303: "See Other", 304: "Not Modified",
  305: "Use Proxy", 307: "Temporary Redirect", 308: "Permanent Redirect",
  400: "Bad Request", 401: "Unauthorized", 402: "Payment Required", 403: "Forbidden", 404: "Not Found",
  405: "Method Not Allowed", 406: "Not Acceptable", 407: "Proxy Authentication Required", 408: "Request Timeout",
  409: "Conflict", 410: "Gone", 411: "Length Required", 412: "Precondition Failed", 413: "Request Entity Too Large",
  414: "Request URI Too Long", 415: "Unsupported Media Type", 416: "Requested Range Not Satisfiable",
  417: "Expectation Failed", 418: "I'm a teapot", 421: "Misdirected Request", 422: "Unprocessable Entity",
  423: "Locked", 424: "Failed Dependency", 425: "Too Early", 426: "Upgrade Required", 428: "Precondition Required",
  429: "Too Many Requests", 431: "Request Header Fields Too Large", 451: "Unavailable For Legal Reasons",
  500: "Internal Server Error", 501: "Not Implemented", 502: "Bad Gateway", 503: "Service Unavailable",
  504: "Gateway Timeout", 505: "HTTP Version Not Supported", 506: "Variant Also Negotiates",
  507: "Insufficient Storage", 508: "Loop Detected", 510: "Not Extended", 511: "Network Authentication Required",
};
const MAX_TIMEOUT_MS = 5 * 60_000;

type BrowserHttpPayload = Partial<NativeHttpRequestPayload> & { timeout_ms?: number };

interface PreparedBody {
  body?: BodyInit;
  /** Go 在缺省或 multipart 时补的 Content-Type；multipart 交给浏览器生成 boundary。 */
  contentType?: "multipart" | "application/octet-stream";
}

type BrowserHttpErrorKey = "corsError" | "timeout" | "invalidUrl" | "fileUnavailable" | "uploadTooLarge" | "redirectBlocked";

function browserHttpError(key: BrowserHttpErrorKey, cause?: unknown) {
  // 保留原始错误便于排查；tsconfig 的 lib 不含 ES2022 的 Error 构造选项，故手动挂 cause。
  const error = new Error(`http.browser.${key}`);
  return cause === undefined ? error : Object.assign(error, { cause });
}

function isForbiddenHeader(name: string) {
  const lower = name.toLowerCase();
  return BROWSER_FORBIDDEN_HEADERS.has(lower) || FORBIDDEN_HEADER_PREFIXES.some((prefix) => lower.startsWith(prefix));
}

function timeoutMs(payload: BrowserHttpPayload) {
  const value = payload.timeout_ms ?? payload.timeoutMs;
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return DEFAULT_TIMEOUT_MS;
  return Math.min(value, MAX_TIMEOUT_MS);
}

interface ValidatedUrl {
  url: string;
  /** URL 中 `user:pass@` 转成的 Basic 凭据（已 base64），无凭据时为 undefined。 */
  basicAuth?: string;
}

function base64Utf8(text: string) {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function validatedUrl(value: unknown): ValidatedUrl {
  if (typeof value !== "string") throw browserHttpError("invalidUrl");
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw browserHttpError("invalidUrl");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw browserHttpError("invalidUrl");
  if (!url.username && !url.password) return { url: value };
  // 与 Go net/http 相同：URL 中的凭据改为 Basic 认证头，并从地址中去掉（fetch 遇到会抛 TypeError）。
  // 非法的 % 转义（如 `%zz`）在 Go 的 url.Parse 中同样是无效地址，不能把原始 URIError 抛给界面。
  let credentials: string;
  try {
    credentials = `${decodeURIComponent(url.username)}:${decodeURIComponent(url.password)}`;
  } catch {
    throw browserHttpError("invalidUrl");
  }
  const basicAuth = base64Utf8(credentials);
  url.username = "";
  url.password = "";
  return { url: url.href, basicAuth };
}

function pickedFile(path: unknown): File {
  const file = typeof path === "string" ? browserFile(path.trim()) : undefined;
  if (!file) throw browserHttpError("fileUnavailable");
  if (file.size > MAX_UPLOAD_BODY) throw browserHttpError("uploadTooLarge");
  return file;
}

function formDataBody(fields: NativeHttpFormField[] = []): FormData {
  const form = new FormData();
  let size = 0;
  for (const field of fields) {
    const name = field.name.trim();
    if (!name) continue;
    if (field.fieldType === "file") {
      const file = pickedFile(field.value);
      const contentType = field.contentType?.trim();
      const filename = field.fileName?.trim() || file.name;
      form.append(name, contentType ? new Blob([file], { type: contentType }) : file, filename);
      size += file.size;
    } else {
      form.append(name, field.value);
      size += field.value.length;
    }
    if (size > MAX_UPLOAD_BODY) throw browserHttpError("uploadTooLarge");
  }
  return form;
}

/** 请求是否真有请求体内容（GET / HEAD 丢弃时只在有内容时提示）。 */
function hasBodyContent(payload: BrowserHttpPayload) {
  switch ((payload.bodyType ?? "").trim().toLowerCase()) {
    case "form-data":
      return (payload.formFields ?? []).some((field) => field.name.trim());
    case "binary":
      return Boolean(payload.binaryFilePath?.trim());
    default:
      return Boolean(payload.body);
  }
}

function prepareBody(payload: BrowserHttpPayload): PreparedBody {
  switch ((payload.bodyType ?? "").trim().toLowerCase()) {
    case "form-data":
      return { body: formDataBody(payload.formFields), contentType: "multipart" };
    case "binary":
      return { body: pickedFile(payload.binaryFilePath), contentType: "application/octet-stream" };
    default:
      return payload.body ? { body: payload.body } : {};
  }
}

async function readCappedBody(response: Response): Promise<Uint8Array> {
  if (!response.body) return new Uint8Array(await response.arrayBuffer());
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (total <= BROWSER_HTTP_MAX_BODY) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
    total += value.byteLength;
  }
  if (total > BROWSER_HTTP_MAX_BODY) await reader.cancel().catch(() => undefined);
  const raw = new Uint8Array(Math.min(total, BROWSER_HTTP_MAX_BODY + 1));
  let offset = 0;
  for (const chunk of chunks) {
    if (offset >= raw.length) break;
    const part = chunk.subarray(0, raw.length - offset);
    raw.set(part, offset);
    offset += part.length;
  }
  return raw;
}

export async function sendBrowserHttpRequest(args: Record<string, unknown>, signal?: AbortSignal): Promise<BrowserHttpResponse> {
  signal?.throwIfAborted();
  const payload = (args.payload ?? {}) as BrowserHttpPayload;
  const { url, basicAuth } = validatedUrl(payload.url);
  const method = (payload.method ?? "").trim().toUpperCase() || "GET";
  const { t } = useVueI18n();
  const notes: string[] = [];

  // 浏览器不允许 GET / HEAD 带请求体：不构建（也就不读取文件），有内容时提示已忽略。
  const bodyless = method === "GET" || method === "HEAD";
  if (bodyless && hasBodyContent(payload)) notes.push(t("http.browser.bodyDropped", { method }));
  const { body, contentType } = bodyless ? {} : prepareBody(payload);

  const headers = new Headers();
  const dropped = new Map<string, string>();
  for (const [name, value] of payload.headers ?? []) {
    const trimmed = name.trim();
    if (!trimmed) continue;
    if (isForbiddenHeader(trimmed)) {
      if (!dropped.has(trimmed.toLowerCase())) dropped.set(trimmed.toLowerCase(), trimmed);
      continue;
    }
    headers.set(trimmed, value);
  }
  if (dropped.size) notes.push(t("http.browser.headersDropped", { headers: [...dropped.values()].join(", ") }));
  if (contentType === "multipart") headers.delete("content-type");
  else if (contentType && !headers.has("content-type")) headers.set("content-type", contentType);
  if (basicAuth && !headers.has("authorization")) headers.set("authorization", `Basic ${basicAuth}`);

  // 带敏感头或请求体时不让浏览器自动跟随（跨域跳转会转发自定义头、307/308 会重放请求体）。
  let sensitive = body !== undefined;
  headers.forEach((_value, name) => {
    if (!CORS_SAFELISTED_HEADERS.has(name)) sensitive = true;
  });
  const redirect: RequestRedirect = sensitive ? "manual" : "follow";

  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs(payload));
  const forwardAbort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", forwardAbort, { once: true });

  const failure = (error: unknown, phase: "fetch" | "body") => {
    if (signal?.aborted) return signal.reason ?? error;
    if (timedOut) return browserHttpError("timeout", error);
    if (phase === "fetch" && error instanceof TypeError) return browserHttpError("corsError", error);
    return error;
  };

  const start = performance.now();
  try {
    let response: Response;
    try {
      response = await fetch(url, { method, headers, body, redirect, signal: controller.signal });
    } catch (error) {
      throw failure(error, "fetch");
    }
    if (response.type === "opaqueredirect") throw browserHttpError("redirectBlocked");
    if (response.redirected && new URL(url).protocol === "https:" && /^http:/i.test(response.url)) {
      notes.push(t("http.browser.redirectDowngraded", { url: response.url }));
    }
    const elapsedMs = Math.round(performance.now() - start);
    const responseHeaders: [string, string][] = [];
    response.headers.forEach((value, name) => responseHeaders.push([name, value]));

    let raw: Uint8Array;
    try {
      raw = await readCappedBody(response);
    } catch (error) {
      throw failure(error, "body");
    }
    const truncated = raw.length > BROWSER_HTTP_MAX_BODY;
    const text = new TextDecoder().decode(truncated ? raw.subarray(0, BROWSER_HTTP_MAX_BODY) : raw);
    return {
      status: response.status,
      status_text: response.statusText || STATUS_TEXT[response.status] || "",
      headers: responseHeaders,
      body: truncated ? text + TRUNCATION_MARKER : text,
      elapsed_ms: elapsedMs,
      size_bytes: raw.length,
      browser_notes: notes,
    };
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", forwardAbort);
  }
}

registerBrowserHandler("http_send_request", sendBrowserHttpRequest);
registerBrowserLocalFeature({
  id: "http",
  titleKey: "browserLocal.feature.http.title",
  limitKey: "browserLocal.feature.http.limit",
});
