export type DatabaseTemporalEditorKind = "text" | "date" | "time" | "datetime";

const dateOnlyPattern = /^\d{4}-\d{2}-\d{2}$/;
const timeOnlyPattern = /^\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?$/;
const dateTimePattern = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?)(Z|[+-]\d{2}:?\d{2})?$/i;

/**
 * Chooses a native picker only for columns that are declared as temporal or
 * whose current scalar value has an unambiguous ISO-like temporal shape.
 */
export function databaseTemporalEditorKind(
  dataType: string | undefined,
  value: unknown,
): DatabaseTemporalEditorKind {
  const type = dataType?.trim().toLowerCase() ?? "";
  if (/\b(timestamp|datetime|date_time)\b|date\s*time/.test(type)) {
    return "datetime";
  }
  if (/\bdate\b/.test(type)) return "date";
  if (/\btime\b/.test(type)) return "time";

  if (typeof value !== "string") return "text";
  if (dateTimePattern.test(value.trim())) return "datetime";
  if (dateOnlyPattern.test(value.trim())) return "date";
  if (timeOnlyPattern.test(value.trim())) return "time";
  return "text";
}

/** Converts a stored temporal scalar to the value expected by a native picker. */
export function toDatabaseTemporalEditorValue(
  value: string,
  kind: DatabaseTemporalEditorKind,
): string {
  const trimmed = value.trim();
  if (kind === "datetime") {
    const match = trimmed.match(dateTimePattern);
    if (!match) return value;
    const time = normalizeTime(match[2]);
    return `${match[1]}T${time}`;
  }
  if (kind === "date") return dateOnlyPattern.test(trimmed) ? trimmed : value;
  if (kind === "time") return timeOnlyPattern.test(trimmed) ? normalizeTime(trimmed) : value;
  return value;
}

/**
 * Restores the original storage convention after the picker changes a value.
 * In particular, ISO UTC values keep their `Z` suffix instead of silently
 * changing into a browser-local timestamp.
 */
export function fromDatabaseTemporalEditorValue(
  editorValue: string,
  kind: DatabaseTemporalEditorKind,
  originalValue: string,
): string {
  const trimmed = editorValue.trim();
  if (kind === "datetime") {
    const match = trimmed.match(dateTimePattern);
    if (!match) return editorValue;
    const original = originalValue.trim().match(dateTimePattern);
    const separator = originalValue.includes("T") ? "T" : " ";
    const offset = original?.[3] ?? "";
    return `${match[1]}${separator}${normalizeTime(match[2])}${offset}`;
  }
  if (kind === "date") return dateOnlyPattern.test(trimmed) ? trimmed : editorValue;
  if (kind === "time") return timeOnlyPattern.test(trimmed) ? normalizeTime(trimmed) : editorValue;
  return editorValue;
}

function normalizeTime(value: string) {
  const [whole, fraction] = value.split(".", 2);
  const seconds = whole.length === 5 ? `${whole}:00` : whole;
  return fraction ? `${seconds}.${fraction.padEnd(3, "0")}` : seconds;
}
