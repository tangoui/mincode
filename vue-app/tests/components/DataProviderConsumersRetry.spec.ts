import { afterEach, describe, expect, it, vi } from "vitest";
import type { PointContribution } from "@/domain/plugins/contributions";

// The contribution sync: consumers are captured and applied directly.
const sync = vi.hoisted(() => ({
  consumers: new Map<string, (contributions: PointContribution[]) => (() => void) | void>(),
}));
vi.mock("@/services/plugin-contributions", () => ({
  registerContributionConsumer: (consumer: { point: string; apply: (contributions: PointContribution[]) => (() => void) | void }) => {
    sync.consumers.set(consumer.point, consumer.apply);
    return () => sync.consumers.delete(consumer.point);
  },
}));
// The lazy runtime chunk fails to load once (for example a stale chunk after
// an update), then loads.
const runtime = vi.hoisted(() => ({ loads: 0, applied: [] as unknown[] }));
vi.mock("@/services/contributions/data-providers-runtime", () => {
  runtime.loads += 1;
  if (runtime.loads === 1) throw new Error("chunk failed to load");
  return { dataProviderAppliers: { sessionSources: (contributions: unknown) => runtime.applied.push(contributions) } };
});

const { registerDataProviderContributionConsumers } = await import("@/services/contributions/data-providers");

const entry = {
  pluginId: "acme.alpha",
  pluginName: "Alpha",
  version: "1.0.0",
  source: "local",
  runtime: "process",
  activeInWorkspace: true,
  point: { fingerprint: "f", entries: [{ id: "chat", title: "Alpha", action: "sessions.read" }], issues: [] },
} as PointContribution;

const settle = async () => {
  for (let round = 0; round < 4; round += 1) await new Promise((resolve) => setTimeout(resolve, 0));
};

afterEach(() => {
  vi.restoreAllMocks();
});

describe("data provider consumers", () => {
  it("retries the runtime import after a failed load and warns once", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const stop = registerDataProviderContributionConsumers();
    const apply = sync.consumers.get("sessionSources");
    if (!apply) throw new Error("sessionSources consumer not registered");

    apply([entry]);
    await settle();
    expect(runtime.applied).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(1);

    apply([entry]);
    await settle();
    expect(runtime.loads).toBe(2);
    expect(runtime.applied).toEqual([[entry]]);
    stop();
  });
});
