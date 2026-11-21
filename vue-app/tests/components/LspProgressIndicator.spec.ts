// The project editor's run bar shows language-server readiness (starting,
// scanning the project, indexing with a percentage) and hides it once idle.
import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";

import LspProgressIndicator from "@/components/projects/LspProgressIndicator.vue";
import { applyLspProgress, setLspRuntimeState, setLspServerName, setLspWatchScanning } from "@/services/lsp-runtime-state";

let wrapper: VueWrapper | undefined;
const root = "/repo";
const server = "intelephense";

async function mountIndicator() {
  wrapper = mount(LspProgressIndicator, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  // Module state is shared between tests: stopping clears the server's progress.
  setLspRuntimeState(root, server, { phase: "stopped" });
});

describe("LspProgressIndicator", () => {
  it("walks through starting, scanning and indexing, then disappears", async () => {
    setLspServerName(root, server, "PHP / Intelephense");
    setLspRuntimeState(root, server, { phase: "starting" });
    const indicator = await mountIndicator();
    expect(indicator.get(".lsp-progress-indicator").text()).toContain("PHP / Intelephense");
    expect(indicator.find(".lsp-progress-indicator .is-loading").exists()).toBe(true);

    setLspRuntimeState(root, server, { phase: "ready" });
    setLspWatchScanning(root, server, true);
    await nextTick();
    expect(indicator.get(".lsp-progress-indicator").attributes("aria-label")).toMatch(/PHP \/ Intelephense/);
    expect(indicator.find(".lsp-progress-indicator .is-loading").exists()).toBe(true);

    setLspWatchScanning(root, server, false);
    applyLspProgress(root, server, { token: "t", value: { kind: "begin", title: "Intelephense indexing", percentage: 0 } });
    applyLspProgress(root, server, { token: "t", value: { kind: "report", percentage: 45, message: "5305 of 11633 files" } });
    await nextTick();
    const item = indicator.get(".lsp-progress-indicator");
    expect(item.text()).toContain("Intelephense indexing 45%");
    expect(item.text()).toContain("5305 of 11633 files");
    expect(item.find(".el-progress").exists()).toBe(true);
    expect(item.attributes("role")).toBe("status");

    applyLspProgress(root, server, { token: "t", value: { kind: "end" } });
    await nextTick();
    expect(indicator.find(".lsp-progress-indicator").exists()).toBe(false);
  });

  it("drops a stopped server's progress so the indicator cannot hang", async () => {
    setLspRuntimeState(root, server, { phase: "ready" });
    applyLspProgress(root, server, { token: "t", value: { kind: "report", percentage: 10 } });
    const indicator = await mountIndicator();
    expect(indicator.find(".lsp-progress-indicator").exists()).toBe(true);
    setLspRuntimeState(root, server, { phase: "stopped" });
    await nextTick();
    expect(indicator.find(".lsp-progress-indicator").exists()).toBe(false);
  });
});
