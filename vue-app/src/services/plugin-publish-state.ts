import { isPluginPublishTerminal, normalizePluginPublishRecord, type PluginPublishRecord } from "@/domain/plugins/publish-summary";
import { refreshPluginPublish } from "@/services/plugin-publish-service";

const maxRecords = 8;
const retentionMs = 10 * 60_000;
/** Lazy state machine; both records and deduplication history follow the bounded backend lifecycle. */
export function createPluginPublishState(update: (records: PluginPublishRecord[]) => void) {
  const records = new Map<string, PluginPublishRecord>();
  const seen = new Map<string, number>();
  const dismissed = new Map<string, number>();
  const refreshed = new Map<string, number>();
  let active = true, signedIn = false, loginEpoch = 0;
  const expiry = (record?: PluginPublishRecord) => {
    const time = Date.parse(record?.expiresAt ?? "");
    return Number.isFinite(time) && time > 0 ? time : Date.now() + retentionMs;
  };
  function bound<T>(map: Map<string, T>) {
    while (map.size > maxRecords) {
      const first = map.keys().next().value;
      if (first === undefined) break;
      map.delete(first);
    }
  }
  function prune() {
    const now = Date.now();
    for (const map of [seen, dismissed]) for (const [id, end] of map) if (end <= now) map.delete(id);
    for (const [id, record] of records) {
      if (isPluginPublishTerminal(record.status) && expiry(record) <= now) records.delete(id);
    }
    while (records.size > maxRecords) {
      const first = [...records].find(([, record]) => isPluginPublishTerminal(record.status))?.[0] ?? records.keys().next().value;
      if (first === undefined) break;
      records.delete(first);
    }
    for (const id of refreshed.keys()) if (!records.has(id)) refreshed.delete(id);
    bound(seen); bound(dismissed); bound(refreshed);
  }
  function publish() { prune(); update([...records.values()]); }
  function refreshLoginWaits() {
    if (!signedIn || !active) return;
    for (const record of records.values()) {
      if (record.status !== "awaiting_login" || refreshed.get(record.id) === loginEpoch) continue;
      refreshed.set(record.id, loginEpoch);
      void refreshPluginPublish(record.id).catch(() => undefined);
    }
  }
  function receive(value: unknown) {
    if (!active) return;
    prune();
    const record = normalizePluginPublishRecord(value);
    if (!record || dismissed.has(record.id)) { publish(); return; }
    seen.set(record.id, expiry(record)); records.set(record.id, record); publish(); refreshLoginWaits();
  }
  const timer = setInterval(() => { if (active) publish(); }, 1000);
  return {
    receive,
    hydrate(values: unknown) {
      if (!Array.isArray(values)) return;
      prune();
      for (const raw of values) {
        const record = normalizePluginPublishRecord(raw);
        if (record && !seen.has(record.id)) receive(record);
      }
    },
    cloud(value: unknown) {
      if (!value || typeof value !== "object" || !("signedIn" in value)) return;
      const next = value.signedIn === true;
      if (next && !signedIn) loginEpoch++;
      signedIn = next; prune(); refreshLoginWaits();
    },
    dismiss(id: string) { prune(); dismissed.set(id, expiry(records.get(id))); records.delete(id); publish(); },
    dispose() { active = false; clearInterval(timer); records.clear(); seen.clear(); dismissed.clear(); refreshed.clear(); },
  };
}
