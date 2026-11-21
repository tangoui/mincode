import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useSourceControlBadgeStore } from "@/stores/source-control-badge";

describe("source control badge store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps a count per workspace scope", () => {
    const store = useSourceControlBadgeStore();
    store.report("a", 25);
    store.report("b", 2);
    expect(store.countFor("a")).toBe(25);
    expect(store.countFor("b")).toBe(2);
    expect(store.countFor("c")).toBe(0);
  });

  it("drops a scope when its count reaches zero or it is cleared", () => {
    const store = useSourceControlBadgeStore();
    store.report("a", 3);
    store.report("a", 0);
    expect(store.counts).toEqual({});
    store.report("b", 1);
    store.clear("b");
    expect(store.countFor("b")).toBe(0);
  });
});
