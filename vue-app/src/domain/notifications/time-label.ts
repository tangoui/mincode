// Notification timestamps, formatted like Date#toLocaleString. toLocaleString builds a new
// formatter on every call; the notification list formats up to 200 rows, so keep one per locale.
const formats = new Map<string, Intl.DateTimeFormat>();

export function notificationTimeLabel(timestamp: number, locale: string): string {
  let format = formats.get(locale);
  if (!format) {
    format = new Intl.DateTimeFormat(locale, {
      year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", second: "numeric",
    });
    formats.set(locale, format);
  }
  return format.format(timestamp);
}
