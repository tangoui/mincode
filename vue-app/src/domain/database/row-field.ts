export type DatabaseRowFieldKind =
  | "text"
  | "textarea"
  | "number"
  | "boolean"
  | "date"
  | "time"
  | "datetime"
  | "json";

export type DatabaseRowFieldValue = string | number | boolean | null;

export class DatabaseRowFieldValueError extends Error {
  readonly reason: "invalidNumber" | "invalidJson";
  readonly cause?: unknown;

  constructor(reason: "invalidNumber" | "invalidJson", cause?: unknown) {
    super(reason);
    this.reason = reason;
    this.cause = cause;
  }
}

/**
 * Returns whether a column name is conventionally used for prose that benefits
 * from multi-line editing even when its database type is VARCHAR.
 */
export function isDatabaseDescriptionField(columnName?: string): boolean {
  const normalized = (columnName ?? "")
    .trim()
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .toLowerCase();
  return /(^|[_\-\s])(description|desc|remark|remarks|comment|comments|note|notes|memo)([_\-\s]|$)/.test(
    normalized,
  );
}

export function databaseRowFieldKind(
  dataType?: string,
  columnName?: string,
): DatabaseRowFieldKind {
  const type = dataType?.trim().toLowerCase() ?? "";
  if (/\b(bool|boolean)\b|\btinyint\s*\(\s*1\s*\)/.test(type)) return "boolean";
  if (/\b(timestamp|datetime|date_time)\b|date\s*time/.test(type)) return "datetime";
  if (/\bdate\b/.test(type)) return "date";
  if (/\btime\b/.test(type)) return "time";
  if (/\b(vector|embedding|embeddings)\b/.test(type)) return "json";
  if (/\b(json|jsonb|object|map|list|set|array|tuple|struct)\b/.test(type)) return "json";
  if (/\b(tinyint|smallint|mediumint|int|integer|bigint|decimal|numeric|number|real|double|float|money|serial|varint|counter)\b/.test(type)) {
    return "number";
  }
  if (/\b(text|clob|blob|binary|bytea|xml)\b/.test(type)) return "textarea";
  if (isDatabaseDescriptionField(columnName)) return "textarea";
  return "text";
}

export function initialDatabaseRowFieldValue(
  kind: DatabaseRowFieldKind,
  defaultValue?: string | null,
): DatabaseRowFieldValue {
  const value = defaultValue?.trim() ?? "";
  if (!value) return kind === "boolean" ? false : "";
  if (kind === "boolean") {
    if (/^(true|1)$/i.test(value)) return true;
    if (/^(false|0)$/i.test(value)) return false;
    return "";
  }
  if (kind === "datetime") {
    const match = value.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?)/);
    return match ? `${match[1]}T${match[2]}` : "";
  }
  if (kind === "date") return /^\d{4}-\d{2}-\d{2}$/.test(value) ? value : "";
  if (kind === "time") return /^\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?$/.test(value) ? value : "";
  if (kind === "number") return Number.isFinite(Number(value)) ? value : "";
  return value;
}

/** Converts a raw database result value into an editable row-form field value. */
export function databaseRowFieldValueFromRaw(
  kind: DatabaseRowFieldKind,
  raw: unknown,
): DatabaseRowFieldValue {
  if (raw == null) return null;
  if (kind === "boolean") return Boolean(raw);
  if (kind === "json") {
    if (typeof raw === "string") return raw;
    try {
      return JSON.stringify(raw, null, 2);
    } catch {
      return String(raw);
    }
  }
  if (typeof raw === "object") return String(raw);
  const text = String(raw);
  if (kind === "number") return Number.isFinite(Number(text)) ? text : "";
  if (kind === "datetime") {
    const match = text.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?)/);
    return match ? `${match[1]}T${match[2]}` : text;
  }
  return text;
}

const declaredTextTypePattern = /char|text|string|clob|uuid|enum|xml|inet|cidr|macaddr/;

/**
 * Chooses how an inline grid edit is parsed. The declared column type wins;
 * only when it is missing or unrecognized (e.g. MongoDB `Int32`/`Document`)
 * does the original cell value's runtime type decide, so a VARCHAR "00123"
 * stays text while a schemaless numeric field stays numeric. Temporal values
 * are submitted verbatim because the inline editor preserves their format.
 */
export function databaseCellEditFieldKind(
  dataType: string | undefined,
  columnName: string | undefined,
  rawValue: unknown,
): DatabaseRowFieldKind {
  const kind = databaseRowFieldKind(dataType, columnName);
  if (kind === "date" || kind === "time" || kind === "datetime") return "text";
  if (kind !== "text" && kind !== "textarea") return kind;
  if (declaredTextTypePattern.test(dataType?.trim().toLowerCase() ?? "")) return kind;
  if (typeof rawValue === "number" || typeof rawValue === "bigint") return "number";
  if (typeof rawValue === "boolean") return "boolean";
  if (rawValue !== null && typeof rawValue === "object") return "json";
  return kind;
}

export function parseDatabaseRowFieldValue(
  kind: DatabaseRowFieldKind,
  value: DatabaseRowFieldValue,
  nullable: boolean,
): unknown {
  if (value === null && nullable) return null;
  if (kind === "boolean" && typeof value !== "string") return Boolean(value);
  const text = String(value ?? "");
  if (nullable && text.trim().toUpperCase() === "NULL") return null;
  if (kind === "boolean") {
    const normalized = text.trim();
    if (/^(true|t|1|yes|y|on)$/i.test(normalized)) return true;
    if (/^(false|f|0|no|n|off)?$/i.test(normalized)) return false;
    return normalized;
  }
  if (kind === "number") {
    const normalized = text.trim();
    const parsed = Number(normalized);
    if (!normalized || !Number.isFinite(parsed)) {
      throw new DatabaseRowFieldValueError("invalidNumber");
    }
    // 超出 double 精度的 BIGINT/DECIMAL 以原文提交，避免被 Number 截断。
    const significantDigits = normalized
      .replace(/^[+-]/, "")
      .replace(/e.*$/i, "")
      .replace(".", "")
      .replace(/^0+/, "");
    if (/^[+-]?\d+(\.\d*)?$/.test(normalized) && significantDigits.length > 15) return normalized;
    return parsed;
  }
  if (kind === "json") {
    try {
      return JSON.parse(text) as unknown;
    } catch (cause) {
      throw new DatabaseRowFieldValueError("invalidJson", cause);
    }
  }
  if (kind === "datetime") return text.replace("T", " ");
  return text;
}