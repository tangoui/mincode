import type { MessageKey } from "@/i18n/messages";

/** 后端 app_data_usage 返回的单个类别。 */
export interface DataUsageItem {
  id: string;
  group: "data" | "cache";
  path: string;
  bytes: number;
  files: number;
  exists: boolean;
}

export interface DataUsage {
  dataRoot: string;
  cacheRoot: string;
  totalBytes: number;
  items: DataUsageItem[];
}

export interface DataUsageGroup {
  group: DataUsageItem["group"];
  root: string;
  bytes: number;
  items: DataUsageItem[];
}

const ITEM_LABELS: Record<string, MessageKey> = {
  database: "settings.data.usage.item.database",
  notes: "settings.data.usage.item.notes",
  plugins: "settings.data.usage.item.plugins",
  pluginData: "settings.data.usage.item.pluginData",
  terminal: "settings.data.usage.item.terminal",
  configSync: "settings.data.usage.item.configSync",
  externalMcp: "settings.data.usage.item.externalMcp",
  other: "settings.data.usage.item.other",
  updates: "settings.data.usage.item.updates",
  agentWorktrees: "settings.data.usage.item.agentWorktrees",
  browser: "settings.data.usage.item.browser",
  cacheOther: "settings.data.usage.item.cacheOther",
};

/** 类别 id 对应的文案键；后端新增的未知类别归入“其他”。 */
export function dataUsageItemLabel(id: string): MessageKey {
  return ITEM_LABELS[id] ?? "settings.data.usage.item.other";
}

/** 按数据 / 缓存分组；空的缓存组（缓存目录不可用）不显示，组内保持后端给出的顺序。 */
export function groupDataUsage(usage: DataUsage): DataUsageGroup[] {
  const groups: DataUsageGroup[] = [];
  for (const [group, root] of [["data", usage.dataRoot], ["cache", usage.cacheRoot]] as const) {
    const items = usage.items.filter((item) => item.group === group);
    if (!root || items.length === 0) continue;
    groups.push({ group, root, bytes: items.reduce((sum, item) => sum + item.bytes, 0), items });
  }
  return groups;
}

export function formatDataBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return unit === 0 ? `${value} B` : `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}
