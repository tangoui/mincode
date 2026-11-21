import { flushPromises, shallowMount } from "@vue/test-utils";
import { nextTick } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import AiAssistantDrawer from "@/components/ai/AiAssistantDrawer.vue";
import AiConversationSidebar from "@/components/ai/AiConversationSidebar.vue";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import AiPermissionCard from "@/components/ai/AiPermissionCard.vue";
import AiPromptComposer from "@/components/ai/AiPromptComposer.vue";
import AiQueuedMessages from "@/components/ai/AiQueuedMessages.vue";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";

const host = vi.hoisted(() => ({
  invoke: vi.fn<(command: string, args?: Record<string, unknown>) => Promise<unknown>>(),
  events: new Map<string, (payload: unknown) => void>(),
}));
vi.mock("vue-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-router")>()),
  useRoute: () => ({ name: "ai" }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  invokeBackend: host.invoke,
  listenAppEvent: (event: string, handler: (payload: unknown) => void) => {
    host.events.set(event, handler);
    return () => host.events.delete(event);
  },
}));
vi.mock("@/services/desktop-stream", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-stream")>()),
  isStreamAvailable: () => false,
}));

const project = "/test/projects/original";
const replacementProject = "/test/projects/replacement";
const defaultPath = "/test/app-data/MinCode/agent-workspace";
const originalId = "original-recovery-id";
const temporaryId = "temporary-agent-id";
const replacementId = "replacement-agent-id";
let unmount: (() => void) | undefined;

async function mountAssistant(boundRoot = "") {
  let projectAvailable = false;
  let finishPrompt: (() => void) | undefined;
  host.invoke.mockImplementation(async (command, args = {}) => {
    switch (command) {
      case "acp_check_local_directory":
        return { available: args.path === project ? projectAvailable : true };
      case "acp_default_local_directory":
        return { path: defaultPath };
      case "local_fs_file_info":
        if (args.projectRoot === project && !projectAvailable) throw new Error("missing project");
        return { isDir: true };
      case "acp_new_session":
        return { sessionId: args.cwd === replacementProject ? replacementId : temporaryId };
      case "acp_set_config_option":
      case "acp_load_session":
        return { configOptions: [] };
      case "acp_prompt":
        return new Promise<void>((resolve) => { finishPrompt = resolve; });
      case "acp_cancel":
        finishPrompt?.();
        return undefined;
      case "agent_run_start":
        return {
          ...args,
          runId: "run-1",
          revision: 1,
          progress: { phase: "thinking", summary: "", createdAt: 1, updatedAt: 1 },
        };
      case "agent_run_update":
        return {
          ...args,
          sessionId: temporaryId,
          workspaceId: "default",
          workspaceScopeId: "default",
          source: "acp",
          title: "test run",
          revision: 2,
          navigation: { type: "ai-conversation", conversationId: "test-conversation" },
          progress: { phase: args.phase, summary: "", createdAt: 1, updatedAt: 2 },
        };
      default:
        return [];
    }
  });
  const assistant = useAiAssistantStore();
  const workspaceId = useWorkbenchStore().activeWorkspaceId;
  const conversation = assistant.createConversation("agent");
  assistant.setConversationWorkspace(conversation.id, { workspaceId, scopeId: workspaceId, projectRoot: boundRoot });
  if (boundRoot) {
    assistant.setConversationAgentState(conversation.id, { acpSessionId: originalId, acpAdapterId: "codex" });
    assistant.addMessageToConversation(conversation.id, { role: "assistant", content: "Existing transcript" });
  }
  assistant.show("agent");
  const wrapper = shallowMount(AiAssistantDrawer, {
    global: {
      stubs: {
        AppSplitPane: { template: '<section><slot name="first" /><slot name="second" /></section>' },
        AiMessageThread: false,
        ElButton: { template: "<button><slot /></button>" },
        ElScrollbar: { template: "<div><slot /></div>" },
      },
    },
  });
  unmount = () => wrapper.unmount();
  await flushPromises();
  const thread = () => wrapper.getComponent(AiMessageThread);
  const composer = () => wrapper.getComponent(AiPromptComposer);
  return {
    assistant, conversation, thread, composer,
    queue: () => wrapper.getComponent(AiQueuedMessages),
    permission: () => wrapper.getComponent(AiPermissionCard),
    finishPrompt: () => finishPrompt?.(),
    restoreProject: () => { projectAvailable = true; },
    removeProject: () => { projectAvailable = false; },
    async start() {
      thread().vm.$emit("start");
      await flushPromises();
    },
    async send(text: string) {
      composer().vm.$emit("update:modelValue", text);
      await nextTick();
      composer().vm.$emit("send");
      await flushPromises();
    },
    async relink(projectRoot: string) {
      useLocalProjectStore().projectRoot = projectRoot;
      const active = assistant.conversationById(conversation.id)!;
      wrapper.getComponent(AiConversationSidebar).vm.$emit("relink-workspace", active);
      await flushPromises();
    },
  };
}

function calls(command: string) {
  return host.invoke.mock.calls.filter(([name]) => name === command).map(([, args]) => args);
}

afterEach(() => {
  unmount?.();
  unmount = undefined;
  host.invoke.mockClear();
  host.invoke.mockResolvedValue([]);
  host.events.clear();
});

describe("local agent directory in the assistant", () => {
  it("starts an unbound conversation from the composer without adding a project binding", async () => {
    const { assistant, conversation, composer } = await mountAssistant();
    composer().vm.$emit("update:modelValue", "First message");
    await nextTick();
    composer().vm.$emit("send");
    await vi.waitFor(() => expect(calls("acp_prompt")).toHaveLength(1));
    expect(calls("acp_check_local_directory")).toEqual([]);
    expect(calls("acp_default_local_directory")).toHaveLength(1);
    expect(calls("acp_launch")).toEqual([expect.objectContaining({ cwd: defaultPath })]);
    expect(calls("acp_new_session")).toEqual([expect.objectContaining({ cwd: defaultPath })]);
    expect(calls("acp_prompt")).toEqual([expect.objectContaining({ sessionId: temporaryId, text: "First message" })]);
    expect(assistant.activeConversation?.projectRoot || "").toBe("");
    expect(assistant.activeConversation?.id).toBe(conversation.id);
  });

  it("requires one-time consent and routes the live session while preserving the original recovery ID", async () => {
    const session = await mountAssistant(project);
    const { assistant, conversation, thread, composer } = session;
    const originalServer = assistant.activeConversation!.acpServerId;
    await session.start();
    expect(calls("acp_launch")).toEqual([]);
    expect(calls("acp_load_session")).toEqual([]);
    expect(calls("acp_default_local_directory")).toEqual([]);
    expect(thread().props("defaultDirectoryActionLabel")).toBeTruthy();

    session.restoreProject();
    const fallbackButton = thread().findAll("button")
      .find((button) => button.text() === thread().props("defaultDirectoryActionLabel"));
    expect(fallbackButton).toBeDefined();
    await fallbackButton!.trigger("click");
    await flushPromises();
    expect(thread().props("effectiveDirectory")).toBe(defaultPath);
    expect(calls("acp_load_session")).toEqual([]);
    const launch = calls("acp_launch")[0];
    const serverId = launch?.serverId;
    expect(serverId).toEqual(expect.any(String));
    expect(launch?.cwd).toBe(defaultPath);
    expect(serverId).not.toBe(originalServer);
    expect(calls("acp_new_session")).toEqual([expect.objectContaining({ cwd: defaultPath, serverId })]);
    expect(assistant.persistedSnapshot()).toMatchObject({ acpSessionId: originalId, acpServerId: originalServer, acpAdapterId: "codex" });
    expect(assistant.activeConversation).toMatchObject({ projectRoot: project });

    composer().vm.$emit("change-agent-config", { configId: "mode", value: "normal" });
    composer().vm.$emit("update:modelValue", "Run with the default directory");
    await nextTick();
    composer().vm.$emit("send");
    await vi.waitFor(() => expect(calls("acp_prompt")).toHaveLength(1));
    const target = { serverId, sessionId: temporaryId };
    expect(calls("acp_set_config_option")).toEqual([expect.objectContaining(target)]);
    expect(calls("agent_run_start")).toEqual([expect.objectContaining({ sessionId: temporaryId })]);
    expect(calls("acp_prompt")).toEqual([expect.objectContaining(target)]);
    expect(calls("local_fs_file_info")).toContainEqual({ projectRoot: defaultPath, filePath: "." });

    host.events.get("acp:session-update")?.({
      ...target,
      update: { sessionUpdate: "current_mode_update", currentModeId: "normal" },
    });
    await flushPromises();
    expect(assistant.activeConversation?.agentEvents).toContainEqual(expect.objectContaining({ kind: "mode", detail: "normal" }));

    composer().vm.$emit("cancel");
    await flushPromises();
    expect(calls("acp_cancel")).toContainEqual(expect.objectContaining(target));
    expect(thread().props("effectiveDirectory")).toBe(defaultPath);

    const other = assistant.createConversation("agent");
    assistant.setConversationWorkspace(other.id, { workspaceId: conversation.workspaceId!, scopeId: conversation.workspaceScopeId!, projectRoot: project });
    await nextTick();
    expect(thread().props("effectiveDirectory")).toBeFalsy();
    expect(thread().props("defaultDirectoryActionLabel")).toBeFalsy();
    await assistant.switchConversation(conversation.id);
    await nextTick();
    expect(thread().props("effectiveDirectory")).toBe(defaultPath);

    thread().vm.$emit("stopAgent");
    await flushPromises();
    expect(calls("acp_disconnect")).toContainEqual({ serverId });
    expect(thread().props("effectiveDirectory")).toBeFalsy();
    await session.start();
    expect(calls("acp_load_session")).toEqual([expect.objectContaining({
      cwd: project, serverId: originalServer, sessionId: originalId,
    })]);
    expect(assistant.snapshot().acpSessionId).toBe(originalId);
    expect(assistant.messages).toContainEqual(expect.objectContaining({ content: "Existing transcript" }));
  });

  it("uses the existing project check for a relayed conversation", async () => {
    const { assistant, conversation, start } = await mountAssistant(project);
    const active = assistant.conversationById(conversation.id)!;
    active.external = { kind: "plugin", pluginId: "mock-relay", pluginName: "Mock Relay", key: "remote-thread" };
    await start();
    expect(calls("acp_check_local_directory")).toEqual([]);
    expect(calls("acp_default_local_directory")).toEqual([]);
    expect(calls("acp_launch")).toEqual([]);
  });

  it("checks the original project again after the host disconnects the temporary agent", async () => {
    const session = await mountAssistant(project);
    await session.start();
    session.thread().vm.$emit("startInDefaultDirectory");
    await flushPromises();
    const serverId = calls("acp_launch")[0]?.serverId;
    expect(session.thread().props("effectiveDirectory")).toBe(defaultPath);
    host.events.get("acp:disconnected")?.({ serverIds: [serverId] });
    await flushPromises();
    expect(session.thread().props("effectiveDirectory")).toBeFalsy();
    await session.start();
    expect(calls("acp_check_local_directory")).toEqual([{ path: project }, { path: project }]);
    expect(calls("acp_default_local_directory")).toHaveLength(1);
    expect(session.thread().props("defaultDirectoryActionLabel")).toBeTruthy();
  });

  it("disconnects a temporary agent on page reload without changing the recovery record", async () => {
    const session = await mountAssistant(project);
    await session.start();
    session.thread().vm.$emit("startInDefaultDirectory");
    await flushPromises();
    const serverId = calls("acp_launch")[0]?.serverId;
    expect(session.thread().props("effectiveDirectory")).toBe(defaultPath);
    window.dispatchEvent(new Event("pagehide"));
    await flushPromises();
    expect(calls("acp_disconnect")).toEqual([{ serverId }]);
    expect(session.assistant.persistedSnapshot().acpSessionId).toBe(originalId);
  });

  it("requires consent if the running project's directory disappears before the next send", async () => {
    const session = await mountAssistant(project);
    session.restoreProject();
    await session.start();
    const serverId = session.assistant.snapshot().acpServerId;
    session.removeProject();
    session.composer().vm.$emit("update:modelValue", "Keep this draft");
    await nextTick();
    session.composer().vm.$emit("send");
    await flushPromises();
    expect(calls("acp_prompt")).toEqual([]);
    expect(calls("acp_default_local_directory")).toEqual([]);
    expect(session.thread().props("defaultDirectoryActionLabel")).toBeTruthy();

    session.thread().vm.$emit("startInDefaultDirectory");
    await flushPromises();
    expect(calls("acp_disconnect")).toContainEqual({ serverId });
    expect(calls("acp_load_session")).toHaveLength(1);
    expect(session.thread().props("effectiveDirectory")).toBe(defaultPath);
    expect(session.assistant.persistedSnapshot().acpSessionId).toBe(originalId);
  });

  it("keeps a live project session when a delayed disconnect arrives for its reused server ID", async () => {
    const session = await mountAssistant(project);
    session.restoreProject();
    await session.start();
    const serverId = session.assistant.snapshot().acpServerId;
    const invoke = host.invoke.getMockImplementation()!;
    host.invoke.mockImplementation((command, args) => command === "acp_list_status"
      ? Promise.resolve([{ id: serverId, state: "connected" }])
      : invoke(command, args));
    host.events.get("acp:disconnected")?.({ serverIds: [serverId] });
    await flushPromises();
    expect(session.assistant.activeConversation?.agentReady).toBe(true);
    await session.start();
    expect(calls("acp_load_session")).toHaveLength(1);
  });

  it.each(["project", "temporary"] as const)("starts in the rebound project after a live %s agent", async (kind) => {
    const session = await mountAssistant(project);
    const workbench = useWorkbenchStore();
    const currentWorkspaceId = workbench.activeWorkspaceId;
    const originalWorkspace = workbench.createWorkspace("Original workspace")!;
    workbench.switchWorkspace(currentWorkspaceId);
    session.assistant.setConversationWorkspace(session.conversation.id, {
      workspaceId: originalWorkspace.id, scopeId: session.assistant.activeWorkspaceScopeId, projectRoot: project,
    });
    if (kind === "project") session.restoreProject();
    await session.start();
    if (kind === "temporary") {
      session.thread().vm.$emit("startInDefaultDirectory");
      await flushPromises();
    }
    session.restoreProject();
    const previousTarget = calls("acp_launch")[0]!;
    const previousAdapterId = session.assistant.persistedSnapshot().acpAdapterId;

    expect(workbench.deleteWorkspace(originalWorkspace.id)).toBe(true);
    await session.relink(replacementProject);
    expect(session.assistant.activeConversation?.projectRoot).toBe(replacementProject);
    await session.send("Run in the new project");
    expect(calls("acp_disconnect")).toContainEqual({ serverId: previousTarget.serverId });
    expect(calls("acp_launch").map((args) => args?.cwd)).toEqual([previousTarget.cwd, replacementProject]);
    const replacementTarget = { serverId: calls("acp_launch")[1]?.serverId, sessionId: replacementId };
    expect(calls("acp_new_session")).toContainEqual(expect.objectContaining({
      cwd: replacementProject, serverId: replacementTarget.serverId,
    }));
    expect(calls("acp_load_session").map((args) => args?.cwd)).not.toContain(replacementProject);
    await vi.waitFor(() => expect(calls("acp_prompt")).toEqual([expect.objectContaining(replacementTarget)]));
    expect(calls("local_fs_file_info")).toContainEqual({ projectRoot: replacementProject, filePath: "." });
    expect(session.assistant.activeConversation?.acpProjectSessions).toContainEqual({
      projectRoot: project, sessionId: originalId, adapterId: previousAdapterId,
    });
    session.finishPrompt();
    await flushPromises();

    await session.relink(project);
    await session.start();
    expect(calls("acp_load_session").at(-1)).toEqual(expect.objectContaining({
      cwd: project, sessionId: originalId,
    }));
    expect(session.assistant.messages).toContainEqual(expect.objectContaining({ content: "Existing transcript" }));
  });


  it("does not publish a project session that disconnects before initialization replies", async () => {
    const session = await mountAssistant(project);
    session.restoreProject();
    const serverId = session.assistant.persistedSnapshot().acpServerId;
    const invoke = host.invoke.getMockImplementation()!;
    let finishInitialization: (() => void) | undefined;
    host.invoke.mockImplementation((command, args) => {
      if (command === "acp_load_session" && !finishInitialization) {
        return Promise.reject(new Error("Mock agent cannot restore the session"));
      }
      if (command === "acp_new_session" && !finishInitialization) {
        return new Promise((resolve) => {
          finishInitialization = () => resolve({ sessionId: temporaryId });
        });
      }
      return invoke(command, args);
    });

    await session.send("Retryable draft");
    expect(calls("acp_new_session")).toHaveLength(1);
    host.events.get("acp:disconnected")?.({ serverIds: [serverId] });
    await flushPromises();
    finishInitialization!();
    await flushPromises();

    expect(calls("acp_list_status")).toHaveLength(1);
    expect(session.assistant.activeConversation?.agentReady).toBe(false);
    expect(session.assistant.persistedSnapshot().acpSessionId).toBe(originalId);
    expect(calls("acp_prompt")).toEqual([]);
    session.composer().vm.$emit("send");
    await flushPromises();
    expect(calls("acp_launch")).toHaveLength(2);
    await vi.waitFor(() => expect(calls("acp_prompt")).toEqual([expect.objectContaining({
      serverId, sessionId: originalId, text: "Retryable draft",
    })]));
    session.finishPrompt();
    await flushPromises();
  });
});
