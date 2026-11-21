import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, KeepAlive, nextTick, ref } from "vue";

const fetchUsage = vi.hoisted(() => vi.fn());
vi.mock("@/services/terminal-resource-usage", () => ({ fetchLocalShellUsage: fetchUsage }));

import { useTerminalResourceUsage } from "@/composables/use-terminal-resource-usage";

const ids = ref(["p1"]);
let usages: ReturnType<typeof useTerminalResourceUsage>["usages"] | undefined;

const Probe = defineComponent({
  name: "Probe",
  setup() {
    usages = useTerminalResourceUsage(() => ids.value).usages;
    return () => h("div");
  },
});
const Other = defineComponent({ name: "Other", setup: () => () => h("span") });

beforeEach(() => {
  vi.useFakeTimers();
  fetchUsage.mockReset();
  fetchUsage.mockImplementation(async (sessionIds: string[]) =>
    sessionIds.map((sessionId) => ({ sessionId, shellPid: 1, bytes: 1, processCount: 1, processes: [] }))
  );
});
afterEach(() => vi.useRealTimers());

describe("useTerminalResourceUsage", () => {
  it("polls while shown and stops while its view is kept alive in the background", async () => {
    const shown = ref(true);
    const wrapper = mount(defineComponent({
      setup: () => () => h(KeepAlive, null, [shown.value ? h(Probe) : h(Other)]),
    }));
    await flushPromises();
    expect(fetchUsage).toHaveBeenCalledTimes(1);
    expect(fetchUsage).toHaveBeenLastCalledWith(["p1"]);
    expect(usages!.value.get("p1")?.bytes).toBe(1);

    await vi.advanceTimersByTimeAsync(5000);
    expect(fetchUsage).toHaveBeenCalledTimes(2);

    shown.value = false;
    await nextTick();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchUsage).toHaveBeenCalledTimes(2);

    shown.value = true;
    await nextTick();
    await flushPromises();
    expect(fetchUsage).toHaveBeenCalledTimes(3);
    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchUsage).toHaveBeenCalledTimes(3);
  });

  it("does not ask without sessions", async () => {
    ids.value = [];
    const wrapper = mount(Probe);
    await flushPromises();
    expect(fetchUsage).not.toHaveBeenCalled();
    wrapper.unmount();
    ids.value = ["p1"];
  });
});
