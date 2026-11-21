export interface HttpHistoryEntry {
  id: string;
  method: string;
  url: string;
  /** 发送时请求标签的名字；旧数据没有，展示时从地址推导。 */
  name?: string;
  status: number;
  elapsed: number;
  createdAt: number;
}

export type HttpHistoryDay = "today" | "yesterday" | "earlier";

export interface HttpHistoryGroup<T extends HttpHistoryEntry> {
  key: string;
  day: HttpHistoryDay;
  /** 当天 0 点的时间戳，用于“更早”分组显示日期。 */
  date: number;
  items: T[];
}

/** 地址里去掉协议和查询串后的“主机 + 路径”，变量原样保留。 */
export function historyUrlSummary(url: string) {
  const trimmed = url.trim();
  try {
    const parsed = new URL(trimmed);
    const path = parsed.pathname === "/" ? "" : parsed.pathname;
    return `${parsed.host}${path}` || trimmed;
  } catch {
    return trimmed.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "").split(/[?#]/)[0] || trimmed;
  }
}

/** 名字优先；没有名字或名字就是地址时，用路径最后一段，再退回主机。 */
export function historyDisplayName(entry: Pick<HttpHistoryEntry, "name" | "url">, fallback: string) {
  const name = entry.name?.trim();
  if (name && name !== entry.url.trim()) return name;
  const summary = historyUrlSummary(entry.url);
  const segments = summary.split("/").filter(Boolean);
  return segments.length > 1 ? segments[segments.length - 1]! : segments[0] || fallback;
}

export function historyStatusTone(status: number): "ok" | "redirect" | "client-error" | "server-error" {
  if (status >= 500 || status <= 0) return "server-error";
  if (status >= 400) return "client-error";
  if (status >= 300) return "redirect";
  return "ok";
}

function startOfDay(timestamp: number) {
  const date = new Date(timestamp);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

/** 按本地日期分组（今天 / 昨天 / 具体日期），保持原有的新到旧顺序。 */
export function groupHttpHistory<T extends HttpHistoryEntry>(items: readonly T[], now = Date.now()): HttpHistoryGroup<T>[] {
  const today = startOfDay(now);
  const yesterday = startOfDay(today - 1);
  const groups: HttpHistoryGroup<T>[] = [];
  for (const item of items) {
    const date = startOfDay(item.createdAt);
    const day: HttpHistoryDay = date === today ? "today" : date === yesterday ? "yesterday" : "earlier";
    const key = String(date);
    let group = groups[groups.length - 1];
    if (!group || group.key !== key) {
      group = { key, day, date, items: [] };
      groups.push(group);
    }
    group.items.push(item);
  }
  return groups;
}

/** 名字、地址、方法、状态码都参与搜索。 */
export function filterHttpHistory<T extends HttpHistoryEntry>(items: readonly T[], query: string, fallbackName: string) {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...items];
  return items.filter((item) =>
    [historyDisplayName(item, fallbackName), item.url, item.method, String(item.status)].some((value) =>
      value.toLowerCase().includes(needle)
    )
  );
}
