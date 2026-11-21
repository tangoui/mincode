import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { ElButton, ElInput } from "element-plus";
import { defineComponent } from "vue";
import MapInspector from "../../../official-plugins/mind-map/ui-src/MapInspector.vue";
import ProbePanel from "../../../official-plugins/http-load-tester/ui-src/ProbePanel.vue";
import NodesPanel from "../../../official-plugins/consul-manager/ui-src/NodesPanel.vue";
import ClusterPanel from "../../../official-plugins/etcd-manager/ui-src/ClusterPanel.vue";
import MetadataPanel from "../../../official-plugins/object-storage/ui-src/MetadataPanel.vue";
import ServerStatsPanel from "../../../official-plugins/zookeeper-manager/ui-src/ServerStatsPanel.vue";
import SystemPanel from "../../../official-plugins/docker-manager/ui-src/SystemPanel.vue";
import ToolOutput from "../../../official-plugins/devtools/ui-src/ToolOutput.vue";

const t = (key: string) => key;

describe("extracted plugin panels", () => {
  it("updates the mind-map draft before committing without mutating the input object", async () => {
    const original = { id: "n", text: "Original", note: "", link: "" };
    let current = original;
    let committed = "";
    const host = defineComponent({
      components: { MapInspector },
      data: () => ({ draft: original }),
      methods: {
        update(value: typeof original) { current = value; this.draft = value; },
        commit() { committed = current.text; },
      },
      setup: () => ({ t }),
      template: '<MapInspector :draft="draft" :t="t" :stats="{topics:1,branches:0,depth:0,notes:0}" :selected-node="{id:\'n\',text:\'Original\'}" :selected-depth="0" @update:draft="update" @commit="commit" />',
    });
    const wrapper = mount(host);
    await wrapper.find("input").setValue("Changed");
    expect(current.text).toBe("Changed");
    expect(committed).toBe("Changed");
    expect(original.text).toBe("Original");
    const inspector = wrapper.findComponent(MapInspector);
    await inspector.findAll("button").find(button => button.text() === "collapseAll")?.trigger("click");
    expect(inspector.emitted("collapse")?.[0]).toEqual([true]);
    wrapper.unmount();
  });

  it("retains probe loading, failure, response filtering and copy behavior", async () => {
    const wrapper = mount(ProbePanel, {
      props: { probeState: { busy: true, result: null, errorKey: "", error: "", label: "Probe" }, t, kindLabel: t },
      global: { stubs: { TimingWaterfall: true } },
    });
    expect(wrapper.text()).toContain("probing");
    await wrapper.setProps({ probeState: { busy: false, result: null, errorKey: "errProbe", error: "Failure detail", label: "Probe" } });
    expect(wrapper.text()).toContain("Failure detail");
    const result = { status: 200, statusText: "OK", headers: [{ name: "Content-Type", value: "text/plain" }, { name: "X-Sample", value: "safe" }], timing: { totalMs: 0, ttfbMs: 0 }, phases: [], body: "<untrusted>", bodyBytes: 11 };
    await wrapper.setProps({ probeState: { busy: false, result, errorKey: "", error: "", label: "Probe" }, headerFilter: "x-sample" });
    expect(wrapper.find("textarea").element.value).toBe("<untrusted>");
    expect(wrapper.find("untrusted").exists()).toBe(false);
    expect(wrapper.findComponent({ name: "ElTable" }).props("data")).toEqual([result.headers[1]]);
    await wrapper.findAll("button").find(button => button.text() === "copyJson")?.trigger("click");
    expect(wrapper.emitted("copy")?.[0]).toEqual([result]);
    wrapper.unmount();
  });

  it("keeps node refresh gated and filters parent-owned rows through model updates", async () => {
    const wrapper = mount(NodesPanel, { props: { t, canRead: false, connected: true, busy: "", nodesLoaded: false, nodesTruncated: false, nodes: [], nodeRows: [], emptyLabel: "Not loaded", filter: "" } });
    expect(wrapper.text()).toContain("Not loaded");
    expect(wrapper.findComponent(ElButton).props("disabled")).toBe(true);
    await wrapper.setProps({ canRead: true });
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    await wrapper.findComponent(ElInput).find("input").setValue("alpha");
    expect(wrapper.emitted("update:filter")?.[0]).toEqual(["alpha"]);
    wrapper.unmount();
  });

  it("shows partial etcd cluster errors instead of replacing them with empty success", async () => {
    const wrapper = mount(ClusterPanel, { props: { t, canRead: true, busy: "", clusterLoaded: true, clusterHeader: null, clusterErrors: { members: { key: "errProfileName", detail: "Members denied" } }, members: [], statuses: [], alarms: [], statusHealthy: 0, statusTotal: 0, emptyLabel: "Not read" } });
    expect(wrapper.text()).toContain("Members denied");
    expect(wrapper.text()).toContain("noAlarms");
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    wrapper.unmount();
  });

  it("keeps metadata warnings and refresh requests in the object detail context", async () => {
    const wrapper = mount(MetadataPanel, { props: { t, language: "en", stat: { size: 5, lastModified: "2026-01-01T00:00:00Z", tagsSupported: false, tagsError: "Tags denied", userMetadata: { sample: "plain" } }, detailBusy: "", canWrite: false } });
    expect(wrapper.text()).toContain("Tags denied");
    // Read-only profiles may reload metadata but not open the editor.
    expect(wrapper.findAllComponents(ElButton)[1]?.props("disabled")).toBe(true);
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    await wrapper.setProps({ detailBusy: "stat" });
    expect(wrapper.findComponent(ElButton).props("disabled")).toBe(true);
    wrapper.unmount();
  });

  it("keeps ZooKeeper metrics refresh disabled when the parent connection is invalid", async () => {
    const wrapper = mount(ServerStatsPanel, { props: { t, ready: true, disabled: true, statsLoading: false, statsLoaded: false, stats: [], filter: "" } });
    expect(wrapper.findComponent(ElButton).props("disabled")).toBe(true);
    await wrapper.setProps({ disabled: false });
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    wrapper.unmount();
  });

  it("keeps Docker system loading and refresh in the parent engine context", async () => {
    const wrapper = mount(SystemPanel, {
      props: { t, info: null, usable: false, loading: false, contextKey: 1, active: true },
      global: { stubs: { DiskPanel: true } },
    });
    expect(wrapper.text()).toContain("selectEndpoint");
    expect(wrapper.findComponent(ElButton).props("disabled")).toBe(true);
    await wrapper.setProps({ usable: true, loading: true });
    expect(wrapper.text()).toContain("loading");
    await wrapper.setProps({ loading: false });
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
    wrapper.unmount();
  });

  it("renders untrusted diff text as text and emits copy rather than accessing the clipboard", async () => {
    const wrapper = mount(ToolOutput, { props: { active: "diff", output: "result", qrPreview: "", rows: [{ type: "add", text: "<script>unsafe</script>" }], t } });
    expect(wrapper.text()).toContain("<script>unsafe</script>");
    expect(wrapper.find("script").exists()).toBe(false);
    await wrapper.find("button").trigger("click");
    expect(wrapper.emitted("copy")).toHaveLength(1);
    wrapper.unmount();
  });
});
