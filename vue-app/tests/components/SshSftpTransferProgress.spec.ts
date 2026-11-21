import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import SshSftpPanel from "@/components/ssh/SshSftpPanel.vue";
import type { FileTransferProtocol, SshConnectionProfile } from "@/domain/ssh/types";
import type { SftpTransferSnapshot, SftpTransferStartRequest } from "@/services/sftp-service";
import { useVueI18n } from "@/i18n/messages";

const bridge = vi.hoisted(() => ({
  invoke: vi.fn(),
  chooseFile: vi.fn(),
  chooseSaveFile: vi.fn(),
  chooseDirectory: vi.fn(),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: (...args: unknown[]) => bridge.invoke(...args),
  chooseNativeFile: (...args: unknown[]) => bridge.chooseFile(...args),
  chooseNativeSaveFile: (...args: unknown[]) => bridge.chooseSaveFile(...args),
  chooseNativeDirectory: (...args: unknown[]) => bridge.chooseDirectory(...args),
  copyText: vi.fn(),
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    const handlers = bridge.listeners.get(name) ?? new Set();
    handlers.add(handler);
    bridge.listeners.set(name, handlers);
    return () => handlers.delete(handler);
  },
}));
// The editor and plugin menu integrations do not participate in file transfers.
vi.mock("@/components/projects/MonacoTextEditor.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/components/plugins/PluginMenuItems.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/components/plugins/PluginServiceMenuItems.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/stores/ssh-connections", () => ({
  useSshConnectionsStore: () => ({ findProfile: () => undefined }),
}));

let wrapper: VueWrapper | undefined;
let request: SftpTransferStartRequest | undefined;

function snapshot(overrides: Partial<SftpTransferSnapshot> = {}): SftpTransferSnapshot {
  if (!request) throw new Error("No active transfer");
  return {
    jobId: request.jobId,
    sessionId: request.sessionId,
    direction: request.direction,
    status: "running",
    bytesTransferred: 0,
    totalBytes: 1024 * 1024,
    speedBytesPerSecond: 0,
    resumedFrom: 0,
    ...overrides,
  };
}

function emit(name: string, payload: unknown) {
  for (const handler of bridge.listeners.get(name) ?? []) handler(payload);
}

function profile(protocol: FileTransferProtocol): SshConnectionProfile {
  return {
    id: "files", name: "Files", host: "ssh.example.test", port: 22, username: "user",
    authType: "password", tags: [], favorite: false, saveCredentials: false, portForwards: [],
    createdAt: 0, updatedAt: 0,
    fileTransfer: { enabled: true, protocol, host: "files.example.test", port: 21, username: "user", viaCurrentSsh: false },
  };
}

async function openPanel(protocol: FileTransferProtocol = "ftp") {
  wrapper = mount(SshSftpPanel, {
    props: { visible: true, sshSessionId: "ssh-test", profile: profile(protocol) },
  });
  await flushPromises();
  return wrapper;
}

async function upload() {
  const { t } = useVueI18n();
  const button = wrapper!.findAll(".ssh-sftp-panel__actions button").find((item) => item.text() === t("sftp.upload"));
  if (!button) throw new Error("Upload action not found");
  await button.trigger("click");
  await flushPromises();
}

async function download(name = "remote.zip") {
  const { t } = useVueI18n();
  const row = wrapper!.findAll(".ssh-sftp-panel__row").find((item) => item.text().includes(name));
  if (!row) throw new Error("Remote entry not found");
  await row.trigger("click");
  const button = wrapper!.findAll(".ssh-sftp-panel__actions button").find((item) => item.text() === t("sftp.download"));
  if (!button) throw new Error("Download action not found");
  await button.trigger("click");
  await flushPromises();
}

beforeEach(() => {
  request = undefined;
  bridge.listeners.clear();
  bridge.chooseFile.mockReset().mockResolvedValue("/local/archive.zip");
  bridge.chooseSaveFile.mockReset().mockResolvedValue("/downloads/remote.zip");
  bridge.chooseDirectory.mockReset().mockResolvedValue("/downloads");
  bridge.invoke.mockReset().mockImplementation(async (command: string, args: Record<string, unknown>) => {
    if (command === "sftp_get_home") return "/tmp";
    if (command === "sftp_list_dir") return [
      { name: "remote.zip", path: "/tmp/remote.zip", isDir: false, size: 1024 * 1024, permissions: "-rw-------" },
      { name: "logs", path: "/tmp/logs", isDir: true, size: 0, permissions: "drwx------" },
    ];
    if (command === "sftp_transfer_start") {
      request = args as unknown as SftpTransferStartRequest;
      return snapshot();
    }
    if (command === "sftp_transfer_cancel") {
      emit("sftp-transfer-ended", snapshot({ status: "cancelled" }));
    }
    return undefined;
  });
});

afterEach(async () => {
  wrapper?.unmount();
  wrapper = undefined;
  await flushPromises();
  bridge.listeners.clear();
});

describe("SSH file panel download progress", () => {
  it.each<FileTransferProtocol>(["sftp", "ftp", "ftps-explicit"])("selects a save path and shows real download progress for %s", async (protocol) => {
    const panel = await openPanel(protocol);
    await download();
    expect(panel.find(".sftp-transfer-row").exists()).toBe(true);
    expect(request).toMatchObject({
      sessionId: `ssh-test-files-${protocol}`, direction: "download", localPath: "/downloads/remote.zip",
      remotePath: "/tmp/remote.zip", resume: false,
    });
    emit("sftp-transfer-progress", snapshot({ bytesTransferred: 512 * 1024, speedBytesPerSecond: 256 * 1024 }));
    await vi.waitFor(() => expect(panel.get(".el-progress").attributes("aria-valuenow")).toBe("50"));
    expect(panel.get(".sftp-transfer-row").text()).toContain("512 KiB");
    expect(panel.get(".sftp-transfer-row").text()).toContain("256 KiB/s");
    expect(bridge.invoke.mock.calls.some(([command]) => command === "sftp_download")).toBe(false);
  });

  it("queues a remote directory under the chosen local parent", async () => {
    const panel = await openPanel();
    await download("logs");
    expect(request).toMatchObject({ direction: "download", remotePath: "/tmp/logs", localPath: "/downloads/logs" });
    expect(panel.get(".sftp-transfer-row").text()).toContain("logs");
    expect(bridge.chooseSaveFile).not.toHaveBeenCalled();
  });

  it("does nothing if either destination picker is cancelled", async () => {
    const panel = await openPanel();
    bridge.chooseSaveFile.mockResolvedValue(null);
    bridge.chooseDirectory.mockResolvedValue(null);
    await download();
    await download("logs");
    expect(panel.find(".sftp-transfer-panel").exists()).toBe(false);
    expect(request).toBeUndefined();
  });

  it("finishes a download without refreshing or reporting an upload", async () => {
    const panel = await openPanel();
    await download();
    const before = bridge.invoke.mock.calls.filter(([command]) => command === "sftp_list_dir").length;
    emit("sftp-transfer-ended", snapshot({ status: "completed", bytesTransferred: 1024 * 1024 }));
    await flushPromises();
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-completed");
    expect(bridge.invoke.mock.calls.filter(([command]) => command === "sftp_list_dir")).toHaveLength(before);
    expect(document.body.querySelector(".el-message--success")?.textContent).toContain("/downloads/remote.zip");
  });

  it("cancels a download and retries it with a fresh job", async () => {
    const panel = await openPanel();
    await download();
    const firstJob = request?.jobId;
    await panel.get(".sftp-transfer-row button").trigger("click");
    await flushPromises();
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-cancelled");
    await panel.get(".sftp-transfer-row button").trigger("click");
    await flushPromises();
    expect(request?.jobId).not.toBe(firstJob);
    expect(request).toMatchObject({ direction: "download", remotePath: "/tmp/remote.zip", localPath: "/downloads/remote.zip", resume: false });
  });

  it("keeps the chosen source fixed while the save dialog is open", async () => {
    const panel = await openPanel();
    let save: (path: string) => void = () => { throw new Error("Save dialog not open"); };
    bridge.chooseSaveFile.mockImplementationOnce(() => new Promise<string>((resolve) => { save = resolve; }));
    const pendingDownload = download();
    await flushPromises();
    await panel.findAll(".ssh-sftp-panel__row")[1]!.trigger("click");
    save("/downloads/renamed.zip");
    await pendingDownload;
    expect(request).toMatchObject({ remotePath: "/tmp/remote.zip", localPath: "/downloads/renamed.zip" });
  });

  it("shows a download failure without a success notification", async () => {
    const panel = await openPanel();
    await download();
    emit("sftp-transfer-ended", snapshot({ status: "failed", error: "Connection lost" }));
    await flushPromises();
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-failed");
    expect(panel.get(".sftp-transfer-row button").attributes("title")).toBe("Connection lost");
    expect(document.body.querySelector(".el-message--success")).toBeNull();
  });

  it("does not start on another session after the save dialog returns", async () => {
    const panel = await openPanel();
    let save: (path: string) => void = () => { throw new Error("Save dialog not open"); };
    bridge.chooseSaveFile.mockImplementationOnce(() => new Promise<string>((resolve) => { save = resolve; }));
    const pendingDownload = download();
    await flushPromises();
    await panel.setProps({ sshSessionId: "another-session" });
    save("/downloads/remote.zip");
    await pendingDownload;
    expect(request).toBeUndefined();
    expect(panel.find(".sftp-transfer-panel").exists()).toBe(false);
  });

  it("shares the upload queue instead of running a download in parallel", async () => {
    const panel = await openPanel();
    await upload();
    await download();
    expect(panel.findAll(".sftp-transfer-row")).toHaveLength(2);
    expect(request?.direction).toBe("upload");
    emit("sftp-transfer-ended", snapshot({ status: "completed", bytesTransferred: 1024 * 1024 }));
    await flushPromises();
    expect(request).toMatchObject({ direction: "download", remotePath: "/tmp/remote.zip" });
  });
});

describe("SSH file panel upload progress", () => {
  it.each<FileTransferProtocol>(["sftp", "ftp", "ftps-explicit"])("shows real upload bytes and speed for %s without resuming an unrelated remote file", async (protocol) => {
    const panel = await openPanel(protocol);
    await upload();
    expect(panel.find(".sftp-transfer-panel").exists()).toBe(true);
    expect(panel.find(".sftp-transfer-row").exists()).toBe(true); // expanded automatically
    expect(request).toMatchObject({
      sessionId: `ssh-test-files-${protocol}`, direction: "upload", localPath: "/local/archive.zip",
      remotePath: "/tmp/archive.zip", resume: false,
    });
    emit("sftp-transfer-progress", snapshot({ bytesTransferred: 256 * 1024, speedBytesPerSecond: 128 * 1024 }));
    await vi.waitFor(() => expect(panel.get(".el-progress").attributes("aria-valuenow")).toBe("25"));
    const row = panel.get(".sftp-transfer-row");
    expect(row.text()).toContain("archive.zip");
    expect(row.text()).toContain("256 KiB");
    expect(row.text()).toContain("1.0 MiB");
    expect(row.text()).toContain("128 KiB/s");
    expect(bridge.invoke.mock.calls.some(([command]) => command === "sftp_upload_file")).toBe(false);
  });

  it("marks completion and refreshes the directory only after the transfer ends", async () => {
    const panel = await openPanel();
    await upload();
    const before = bridge.invoke.mock.calls.filter(([command]) => command === "sftp_list_dir").length;
    emit("sftp-transfer-ended", snapshot({ status: "completed", bytesTransferred: 1024 * 1024 }));
    await flushPromises();
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-completed");
    expect(bridge.invoke.mock.calls.filter(([command]) => command === "sftp_list_dir")).toHaveLength(before + 1);
    expect(bridge.listeners.get("sftp-transfer-progress")?.size).toBe(0);
    expect(bridge.listeners.get("sftp-transfer-ended")?.size).toBe(0);
  });

  it("cancels an in-flight upload from the visible transfer row", async () => {
    const panel = await openPanel();
    await upload();
    const { t } = useVueI18n();
    await panel.get(`.sftp-transfer-row button[aria-label="${t("sftp.cancelRunning")}"]`).trigger("click");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("sftp_transfer_cancel", { jobId: request!.jobId, sessionId: "ssh-test-files-ftp" });
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-cancelled");
  });

  it("shows failures instead of claiming the upload finished and offers retry", async () => {
    const panel = await openPanel();
    await upload();
    emit("sftp-transfer-ended", snapshot({ status: "failed", error: "Connection lost" }));
    await flushPromises();
    expect(panel.get(".sftp-transfer-row__status").classes()).toContain("is-failed");
    expect(panel.get(".sftp-transfer-row button").attributes("title")).toBe("Connection lost");
  });

  it("cancels the running transfer and unsubscribes when the panel is destroyed", async () => {
    await openPanel();
    await upload();
    wrapper!.unmount();
    wrapper = undefined;
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("sftp_transfer_cancel", { jobId: request!.jobId, sessionId: "ssh-test-files-ftp" });
    expect(bridge.listeners.get("sftp-transfer-progress")?.size).toBe(0);
    expect(bridge.listeners.get("sftp-transfer-ended")?.size).toBe(0);
  });

  it("queues another upload without hiding the active transfer", async () => {
    const panel = await openPanel();
    await upload();
    bridge.chooseFile.mockResolvedValue("/local/second.zip");
    await upload();
    expect(panel.findAll(".sftp-transfer-row")).toHaveLength(2);
    expect(bridge.invoke.mock.calls.filter(([command]) => command === "sftp_transfer_start")).toHaveLength(1);
    emit("sftp-transfer-ended", snapshot({ status: "completed", bytesTransferred: 1024 * 1024 }));
    await flushPromises();
    expect(request?.localPath).toBe("/local/second.zip");
    expect(panel.findAll(".sftp-transfer-row__status.is-running")).toHaveLength(1);
  });

  it("keeps uploading while hidden and shows updated progress when reopened", async () => {
    const panel = await openPanel();
    await upload();
    await panel.setProps({ visible: false });
    emit("sftp-transfer-progress", snapshot({ bytesTransferred: 512 * 1024 }));
    await panel.setProps({ visible: true });
    await vi.waitFor(() => expect(panel.get(".el-progress").attributes("aria-valuenow")).toBe("50"));
    expect(bridge.invoke.mock.calls.some(([command]) => command === "sftp_transfer_cancel")).toBe(false);
  });

  it("keeps the destination selected before the native file picker opens", async () => {
    const panel = await openPanel();
    let choose: (path: string) => void = () => { throw new Error("Picker not open"); };
    bridge.chooseFile.mockImplementationOnce(() => new Promise<string>((resolve) => { choose = resolve; }));
    const pendingUpload = upload();
    await flushPromises();
    await panel.get(".ssh-sftp-panel__pathbar input").setValue("/another-directory");
    choose("/local/archive.zip");
    await pendingUpload;
    expect(request?.remotePath).toBe("/tmp/archive.zip");
  });

  it("does not start an upload when the file picker is cancelled", async () => {
    const panel = await openPanel();
    bridge.chooseFile.mockResolvedValue(null);
    await upload();
    expect(panel.find(".sftp-transfer-panel").exists()).toBe(false);
    expect(request).toBeUndefined();
  });
});
