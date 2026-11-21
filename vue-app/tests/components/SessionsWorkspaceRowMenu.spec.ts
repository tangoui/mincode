import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

// Session rows are plain markup; one shared actions menu opens at the clicked row. A full
// el-dropdown per row (1000 rows with ten projects expanded) froze expanding for ~0.8 s.
const bridge = vi.hoisted(() => ({ calls: [] as Array<[string, Record<string, unknown>]> }));
const sessionCount = vi.hoisted(() => ({ value: 50 }));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
  chooseNativeDirectory: () => Promise.resolve(""),
  invokeBackend: (command: string, args: Record<string, unknown>) => {
    bridge.calls.push([command, args]);
    if (command === "list_claude_projects") {
      return Promise.resolve([{ encodedDir: "-work-app", decodedPath: "/work/app", sessionCount: sessionCount.value, lastAt: 1 }]);
    }
    if (command === "get_project_sessions") {
      return Promise.resolve(Array.from({ length: sessionCount.value }, (_, i) => ({
        uuid: `00000000-0000-0000-0000-${String(i).padStart(12, "0")}`, encodedDir: "-work-app", decodedPath: "/work/app",
        title: `Session ${i}`, lastAt: 1000 + i, messageCount: 4,
      })));
    }
    if (command === "get_session_messages") return Promise.resolve([{ role: "user", content: "hello" }]);
    return Promise.resolve(command.startsWith("list_") ? [] : {});
  },
}));
vi.mock("vue-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-router")>()),
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("@/composables/use-active-view-commands", () => ({ useToggleSidebarCommand: () => undefined }));
vi.mock("@/services/plugin-contributions", () => ({ registerContributionConsumer: () => () => undefined }));
vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ setDraft: () => undefined, show: () => undefined }) }));
vi.mock("@/composables/use-workspace-state-scope", async () => {
  const { ref } = await import("vue");
  return {
    useWorkspaceStateScope: () => ({ hydrate: () => Promise.resolve() }),
    useWorkspaceScopedPersistence: () => ({
      scopeId: ref("test"),
      capture: () => ({}),
      isCurrent: () => true,
      load: () => Promise.resolve(undefined),
      save: () => Promise.resolve(),
    }),
  };
});
vi.mock("@/services/session-tool-preferences", async () => {
  const { createDefaultSessionToolPrefs } = await import("@/domain/ai/session-tools");
  return {
    SESSION_TOOLS_EVENT: "test:session-tools",
    applySessionToolRoots: () => Promise.resolve({}),
    loadSessionToolPrefs: () => Promise.resolve(createDefaultSessionToolPrefs()),
  };
});
const { ElDropdown } = await import("element-plus");
const { default: SessionsWorkspaceView } = await import("@/views/SessionsWorkspaceView.vue");

const settle = async () => {
  for (let round = 0; round < 4; round += 1) {
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  bridge.calls = [];
  sessionCount.value = 50;
});

async function expandClaudeProject() {
  wrapper = mount(SessionsWorkspaceView, { attachTo: document.body });
  await settle();
  const claude = wrapper.findAll(".sessions-provider").find((section) => section.text().includes("Claude"));
  if (!claude) throw new Error("Claude group missing");
  await claude.get(".sessions-provider__toggle").trigger("click");
  await settle();
  await wrapper.get(".sessions-project__toggle").trigger("click");
  await settle();
  await vi.waitFor(() => expect(wrapper!.findAll(".sessions-session-row").length).toBeGreaterThan(0));
}

describe("SessionsWorkspaceView session row actions", { timeout: 60_000 }, () => {
  it("renders only the session rows near the viewport for a project with many sessions", async () => {
    sessionCount.value = 600;
    await expandClaudeProject();
    const wrap = wrapper!.get(".sessions-provider-list .el-scrollbar__wrap").element as HTMLElement;
    Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 600 });
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await settle();
    const rows = wrapper!.findAll(".sessions-session-row").length;
    expect(rows).toBeGreaterThan(0);
    expect(rows).toBeLessThan(100);
  });

  it("renders one shared actions menu for all rows", async () => {
    await expandClaudeProject();
    expect(wrapper!.findAll(".sessions-row-menu")).toHaveLength(50);
    expect(wrapper!.findAllComponents(ElDropdown).length).toBeLessThanOrEqual(1);
  });

  it("opens the shared menu for the clicked row and runs its command on that session", async () => {
    await expandClaudeProject();
    const rows = wrapper!.findAll(".sessions-session-row");
    const target = rows.find((row) => row.text().includes("Session 7"))!;
    await target.get(".sessions-row-menu").trigger("click");
    await settle();
    const preview = [...document.body.querySelectorAll<HTMLElement>(".el-dropdown-menu__item")]
      .find((item) => item.dataset.sessionCommand === "open");
    expect(preview).toBeTruthy();
    preview!.click();
    await settle();
    const opened = bridge.calls.filter(([command]) => command === "get_session_messages");
    expect(opened.at(-1)?.[1]).toMatchObject({ encodedDir: "-work-app", uuid: "00000000-0000-0000-0000-000000000007" });
  });
});
