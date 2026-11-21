import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import SftpWorkspaceView from "@/views/SftpWorkspaceView.vue";
import SftpFileList from "@/components/sftp/SftpFileList.vue";
import SshSftpPanel from "@/components/ssh/SshSftpPanel.vue";
import type { SshConnectionProfile } from "@/domain/ssh/types";

const sftp = vi.hoisted(() => ({
  connectSftp: vi.fn(),
  disconnectSftp: vi.fn(),
  listSftpDirectory: vi.fn(),
  readSftpTextFile: vi.fn(),
  writeSftpTextFile: vi.fn(),
}));
const dialogs = vi.hoisted(() => ({ confirm: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn() }));
const route = vi.hoisted(() => ({ query: { profile: "p1" } as Record<string, string> }));

vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { success: dialogs.success, error: dialogs.error, warning: dialogs.warning },
  ElMessageBox: { confirm: dialogs.confirm },
}));
vi.mock("vue-router", () => ({ useRoute: () => route }));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  chooseNativeFile: vi.fn(),
  copyText: vi.fn(),
}));
vi.mock("@/services/settings-service", () => ({ getUserHomeDirectory: vi.fn().mockResolvedValue("") }));
vi.mock("@/composables/use-workspace-state-scope", () => ({
  useWorkspaceStateScope: () => ({ capture: () => ({ scopeId: "scope", workspaceId: "workspace" }) }),
}));
vi.mock("@/services/sftp-service", () => ({
  ...sftp,
  cancelSftpTransfer: vi.fn(),
  chooseLocalSftpDirectory: vi.fn(),
  createSftpDirectory: vi.fn(),
  deleteLocalFileEntry: vi.fn(),
  deleteSftpEntry: vi.fn(),
  downloadSftpFile: vi.fn(),
  isSftpImageFile: () => false,
  listLocalFiles: vi.fn().mockResolvedValue([]),
  moveLocalFileEntry: vi.fn(),
  readSftpImagePreview: vi.fn(),
  renameLocalFileEntry: vi.fn(),
  renameSftpEntry: vi.fn(),
  resolveSftpInitialPath: (home: string) => home,
  runSftpTransfer: vi.fn(),
  uploadSftpFile: vi.fn(),
}));

const profiles = vi.hoisted(() =>
  ["p1", "p2"].map((id): SshConnectionProfile => ({
    id,
    name: `Server ${id}`,
    host: `${id}.example`,
    port: 22,
    username: "dev",
    authType: "password",
    tags: [],
    favorite: false,
    saveCredentials: false,
    portForwards: [],
    createdAt: 0,
    updatedAt: 0,
  }))
);
vi.mock("@/stores/ssh-connections", () => ({
  useSshConnectionsStore: () => ({
    hydrated: true,
    hydrate: vi.fn(),
    sortedProfiles: profiles,
    findProfile: (id: string) => profiles.find((item) => item.id === id),
    credentialsFor: () => ({ password: "test-only" }),
  }),
}));

vi.mock("@/components/ui/MonacoTextEditor.vue", async () => {
  const { defineComponent, h } = await import("vue");
  // __esModule lets the view's defineAsyncComponent unwrap the default export like a real SFC import.
  return {
    __esModule: true,
    default: defineComponent({
      name: "MonacoTextEditorStub",
      props: { modelValue: { type: String, default: "" } },
      emits: ["update:modelValue", "save"],
      setup: () => () => h("div", { class: "monaco-stub" }),
    }),
  };
});
const MonacoStub = { name: "MonacoTextEditorStub" };

const remoteFile = { name: "app.conf", path: "/home/app.conf", isDir: false, size: 5, permissions: "-rw-r--r--" };

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

let wrapper: VueWrapper | undefined;
beforeEach(() => {
  route.query = { profile: "p1" };
  sftp.connectSftp.mockResolvedValue("/home");
  sftp.disconnectSftp.mockResolvedValue(undefined);
  sftp.listSftpDirectory.mockResolvedValue([remoteFile]);
  sftp.readSftpTextFile.mockResolvedValue("hello");
  sftp.writeSftpTextFile.mockResolvedValue(undefined);
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.clearAllMocks();
});

async function openDirtyEditor() {
  wrapper = mount(SftpWorkspaceView, { attachTo: document.body });
  await flushPromises();
  expect(sftp.connectSftp).toHaveBeenCalledTimes(1);
  wrapper.findAllComponents(SftpFileList).at(-1)!.vm.$emit("open", remoteFile);
  await flushPromises();
  const editor = wrapper.findComponent(MonacoStub);
  editor.vm.$emit("update:modelValue", "first edit");
  await flushPromises();
  return editor;
}

function tabLabel() {
  return wrapper!.find(".sftp-editor-tab__select").text();
}

describe("SftpWorkspaceView unsaved remote edits", () => {
  it("keeps the tab dirty when the user types while a save is in flight and blocks re-entrant saves", async () => {
    const editor = await openDirtyEditor();
    const write = deferred();
    sftp.writeSftpTextFile.mockReturnValueOnce(write.promise);

    editor.vm.$emit("save");
    await flushPromises();
    editor.vm.$emit("update:modelValue", "second edit");
    editor.vm.$emit("save");
    await flushPromises();
    expect(sftp.writeSftpTextFile).toHaveBeenCalledTimes(1);
    expect(sftp.writeSftpTextFile).toHaveBeenLastCalledWith(expect.any(String), remoteFile.path, "first edit");

    write.resolve();
    await flushPromises();
    expect(tabLabel()).toContain("•");

    editor.vm.$emit("save");
    await flushPromises();
    expect(sftp.writeSftpTextFile).toHaveBeenCalledTimes(2);
    expect(sftp.writeSftpTextFile).toHaveBeenLastCalledWith(expect.any(String), remoteFile.path, "second edit");
    expect(tabLabel()).not.toContain("•");
  });

  it("asks before the disconnect button discards unsaved edits", async () => {
    await openDirtyEditor();
    dialogs.confirm.mockRejectedValueOnce("cancel");
    await wrapper!.find(".sftp-disconnect-button").trigger("click");
    await flushPromises();
    expect(dialogs.confirm).toHaveBeenCalledTimes(1);
    expect(sftp.disconnectSftp).not.toHaveBeenCalled();
    expect(tabLabel()).toContain("•");

    dialogs.confirm.mockResolvedValueOnce(undefined);
    await wrapper!.find(".sftp-disconnect-button").trigger("click");
    await flushPromises();
    expect(sftp.disconnectSftp).toHaveBeenCalledTimes(1);
    expect(wrapper!.find(".sftp-editor-tab").exists()).toBe(false);
  });

  it("keeps the original profile selected when switching profiles is cancelled", async () => {
    await openDirtyEditor();
    const select = wrapper!.findComponent({ name: "ElSelect" });
    dialogs.confirm.mockRejectedValueOnce("cancel");
    select.vm.$emit("update:modelValue", "p2");
    await flushPromises();
    expect(select.props("modelValue")).toBe("p1");
    expect(sftp.disconnectSftp).not.toHaveBeenCalled();
    expect(tabLabel()).toContain("•");

    dialogs.confirm.mockResolvedValueOnce(undefined);
    select.vm.$emit("update:modelValue", "p2");
    await flushPromises();
    expect(select.props("modelValue")).toBe("p2");
    expect(sftp.disconnectSftp).toHaveBeenCalledTimes(1);
  });

  it("warns instead of silently dropping unsaved edits when the view is unmounted", async () => {
    await openDirtyEditor();
    wrapper!.unmount();
    wrapper = undefined;
    expect(dialogs.warning).toHaveBeenCalledWith(expect.stringContaining(remoteFile.name));
  });
});

describe("SshSftpPanel unsaved remote edits", () => {
  it("keeps the editor dirty when the user types while a save is in flight", async () => {
    wrapper = mount(SshSftpPanel, {
      props: { visible: true, sshSessionId: "ssh-1", profile: profiles[0] },
      attachTo: document.body,
    });
    await flushPromises();
    await wrapper.find(".ssh-sftp-panel__row").trigger("dblclick");
    await flushPromises();
    const editor = wrapper.findComponent(MonacoStub);
    editor.vm.$emit("update:modelValue", "first edit");
    const write = deferred();
    sftp.writeSftpTextFile.mockReturnValueOnce(write.promise);

    editor.vm.$emit("save");
    await flushPromises();
    editor.vm.$emit("update:modelValue", "second edit");
    editor.vm.$emit("save");
    await flushPromises();
    expect(sftp.writeSftpTextFile).toHaveBeenCalledTimes(1);

    write.resolve();
    await flushPromises();
    expect(wrapper.find(".ssh-sftp-panel__editor-toolbar strong").text()).toContain("•");
  });
});
