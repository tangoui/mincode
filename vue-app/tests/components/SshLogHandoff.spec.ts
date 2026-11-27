import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import SshServerManagerPanel from "@/components/ssh/SshServerManagerPanel.vue";

const handoff = vi.hoisted(() => vi.fn());
const ssh = vi.hoisted(() => ({
  loadSshServerLogs: vi.fn().mockResolvedValue("journal entry"),
  loadSshDockerInfo: vi.fn().mockResolvedValue({ available: true }),
  loadSshDockerContainers: vi.fn().mockResolvedValue([{
    id: "container-id", names: "worker", state: "running", image: "worker:latest", status: "Up", ports: "", created: "",
  }]),
  loadSshDockerLogs: vi.fn().mockResolvedValue("container entry"),
  loadSshServerSnapshot: vi.fn().mockRejectedValue(new Error("Snapshot unavailable")),
}));
vi.mock("@/services/ai-handoff", () => ({ sendToAi: handoff }));
vi.mock("@/services/ssh-server-manager-service", async (original) => ({
  ...(await original<typeof import("@/services/ssh-server-manager-service")>()),
  ...ssh,
}));

let dispose: (() => void) | undefined;
afterEach(() => {
  dispose?.();
  handoff.mockClear();
});

async function openTab(tab: string) {
  const wrapper = mount(SshServerManagerPanel, {
    props: { modelValue: true, sessionId: "ssh-1" },
    attachTo: document.body,
    global: {
      stubs: {
        ElDrawer: { template: "<section><slot /></section>" },
        ElDialog: { props: ["modelValue"], template: '<section v-if="modelValue"><slot /><slot name="footer" /></section>' },
      },
    },
  });
  dispose = () => wrapper.unmount();
  await flushPromises();
  wrapper.getComponent({ name: "ElTabs" }).vm.$emit("update:modelValue", tab);
  await flushPromises();
  return wrapper;
}

function button(label: string) {
  const element = [...document.body.querySelectorAll("button")].find((item) => !item.disabled && item.textContent?.trim() === label);
  expect(element, label).toBeDefined();
  return new DOMWrapper(element!);
}

it("passes the loaded service logs to AI", async () => {
  await openTab("logs");
  expect(ssh.loadSshServerLogs).toHaveBeenCalledWith("ssh-1", "", 200);
  await button("让 AI 分析日志").trigger("click");
  expect(handoff).toHaveBeenCalledWith({
    source: "ssh", title: "服务日志", content: "journal entry", prompt: "请分析这些日志，指出异常及下一步排查方法。",
  });
});

it("passes logs from the chosen Docker container to AI", async () => {
  await openTab("docker");
  await button("容器日志").trigger("click");
  await flushPromises();
  expect(ssh.loadSshDockerLogs).toHaveBeenCalledWith("ssh-1", "container-id", 300);
  await button("让 AI 分析日志").trigger("click");
  expect(handoff).toHaveBeenCalledWith({
    source: "docker", title: "容器日志 · worker", content: "container entry", prompt: "请分析这些日志，指出异常及下一步排查方法。",
  });
});
