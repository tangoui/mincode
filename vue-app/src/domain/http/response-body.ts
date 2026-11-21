/** 超过该长度（字符）的响应体不自动 JSON 格式化，避免同步 parse/stringify 卡住界面。 */
export const LARGE_RESPONSE_FORMAT_THRESHOLD = 1_500_000;

export function formatJsonResponseBody(body: string) {
  try {
    return JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    return body;
  }
}

export function isLargeResponseBody(body: string, threshold = LARGE_RESPONSE_FORMAT_THRESHOLD) {
  return body.length > threshold;
}

/** 仅按首个非空白字符粗判是否可能是 JSON，用于决定是否展示「格式化」入口。 */
export function looksLikeJsonBody(body: string) {
  const match = /\S/.exec(body.slice(0, 1024));
  return match?.[0] === "{" || match?.[0] === "[";
}

export function formatResponseSizeLabel(length: number) {
  return length >= 1024 * 1024
    ? `${(length / 1024 / 1024).toFixed(1)} MB`
    : `${Math.round(length / 1024)} KB`;
}
