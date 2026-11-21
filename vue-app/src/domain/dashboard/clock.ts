export type DashboardGreetingKey =
  | "dashboard.greetingMorning"
  | "dashboard.greetingAfternoon"
  | "dashboard.greetingEvening";

/** Morning until noon, afternoon until 18:00, evening otherwise (including after midnight). */
export function greetingKeyForHour(hour: number): DashboardGreetingKey {
  if (hour >= 5 && hour < 12) return "dashboard.greetingMorning";
  if (hour >= 12 && hour < 18) return "dashboard.greetingAfternoon";
  return "dashboard.greetingEvening";
}

/** Delay to the next whole second, so the seconds change together with the system clock. */
export function msUntilNextSecond(now: Date) {
  return 1000 - now.getMilliseconds();
}

const formatters = new Map<string, { time: Intl.DateTimeFormat; date: Intl.DateTimeFormat }>();

function formattersFor(locale: string) {
  let entry = formatters.get(locale);
  if (!entry) {
    entry = {
      time: new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }),
      date: new Intl.DateTimeFormat(locale, { year: "numeric", month: "long", day: "numeric", weekday: "long" }),
    };
    formatters.set(locale, entry);
  }
  return entry;
}

const pad = (value: number) => String(value).padStart(2, "0");

/** Local time (24-hour, with seconds) and long date for the dashboard header. */
export function formatDashboardClock(now: Date, locale: string) {
  const { time, date } = formattersFor(locale);
  return {
    time: time.format(now),
    date: date.format(now),
    iso: `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}T${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`,
  };
}
