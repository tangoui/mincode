import type { HttpKeyValueRow } from "../request-collection";
import { isSensitiveHttpKey, looksLikeHttpCredential } from "../plugin-collections";

const PLACEHOLDER = /^\s*\{\{\s*[^{}\s]+\s*\}\}\s*$/;

/** 导出文件会被分享：敏感值只保留 {{变量}} 引用，字面量清空。 */
export function exportAuthValue(value: string | undefined) {
  return value && PLACEHOLDER.test(value) ? value : "";
}

export function exportRows(rows: readonly HttpKeyValueRow[], header: boolean) {
  return rows.map((row) => {
    const sensitive = isSensitiveHttpKey(row.key, header) || looksLikeHttpCredential(row.value);
    return sensitive ? { ...row, value: exportAuthValue(row.value) } : row;
  });
}

/** 拆分 URL 里的查询串；查询参数单独成行，避免发送时与 params 重复拼接。 */
export function splitUrlQuery(raw: string): { base: string; query: Array<[string, string]> } {
  const hashless = raw.split("#")[0] ?? "";
  const index = hashless.indexOf("?");
  if (index < 0) return { base: hashless, query: [] };
  const query: Array<[string, string]> = [];
  for (const part of hashless.slice(index + 1).split("&")) {
    if (!part) continue;
    const [key = "", ...rest] = part.split("=");
    const decode = (value: string) => {
      try {
        return decodeURIComponent(value.replace(/\+/g, " "));
      } catch {
        return value;
      }
    };
    if (key) query.push([decode(key), decode(rest.join("="))]);
  }
  return { base: hashless.slice(0, index), query };
}
