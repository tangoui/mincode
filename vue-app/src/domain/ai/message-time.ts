export function normalizeAiMessageTimestamp(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

export function formatAiMessageShortTime(
  timestamp: number | undefined,
  now = new Date(),
  locale?: Intl.LocalesArgument,
) {
  if (!timestamp) return "";
  const date = new Date(timestamp);
  const sameDay = date.getFullYear() === now.getFullYear()
    && date.getMonth() === now.getMonth()
    && date.getDate() === now.getDate();
  return new Intl.DateTimeFormat(locale, sameDay
    ? { hour: "2-digit", minute: "2-digit" }
    : { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date);
}

export function formatAiMessageFullTime(timestamp: number | undefined, locale?: Intl.LocalesArgument) {
  if (!timestamp) return "";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(timestamp));
}
