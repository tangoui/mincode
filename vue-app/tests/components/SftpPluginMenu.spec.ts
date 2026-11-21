import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import SftpWorkspaceView from "@/views/SftpWorkspaceView.vue";
import SftpFileList from "@/components/sftp/SftpFileList.vue";
import SshSftpPanel from "@/components/ssh/SshSftpPanel.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import { parseWhenExpression } from "@/domain/commands/when";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import type { SshConnectionProfile } from "@/domain/ssh/types";
import { pluginMenuItems } from "@/services/contributions/menus";

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: [{ pluginId: "demo.menus", id: "inspect" }], contexts: {} }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
});

// Plugin services: the provider list is a test-owned ref, runPluginService a spy.
const services = vi.hoisted(() => ({ run: vi.fn(), entries: undefined as unknown as import("vue").ShallowRef<unknown[]> }));
vi.mock("@/services/plugin-services", async (original) => {
  const actual = await original<typeof import("@/services/plugin-services")>();
  const { shallowRef } = await import("vue");
  services.entries = shallowRef<unknown[]>([]);
  return { ...actual, pluginServices: services.entries, runPluginService: (...args: unknown[]) => services.run(...args) };
});

const sftp = vi.hoisted(() => ({
  connectSftp: vi.fn(),
  disconnectSftp: vi.fn(),
  listSftpDirectory: vi.fn(),
  listLocalFiles: vi.fn(),
}));
const dialogs = vi.hoisted(() => ({ confirm: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn() }));
const route = vi.hoisted(() => ({ query: { profile: "ssh" } as Record<string, string> }));

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
vi.mock("@/services/settings-service", () => ({ getUserHomeDirectory: vi.fn().mockResolvedValue("/Users/me") }));
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
  moveLocalFileEntry: vi.fn(),
  readSftpImagePreview: vi.fn(),
  readSftpTextFile: vi.fn(),
  renameLocalFileEntry: vi.fn(),
  renameSftpEntry: vi.fn(),
  resolveSftpInitialPath: (home: string) => home,
  runSftpTransfer: vi.fn(),
  uploadSftpFile: vi.fn(),
  writeSftpTextFile: vi.fn(),
}));

const profiles = vi.hoisted(() => {
  const base = {
    port: 2222,
    username: "deploy",
    authType: "key" as const,
    keyFile: "/Users/me/.ssh/id_secret",
    postConnectCommands: ["export TOKEN=abc123"],
    tags: [],
    favorite: false,
    saveCredentials: true,
    portForwards: [],
    jumpHost: { host: "bastion.hidden", port: 22, username: "jumper", authType: "key" as const, keyFile: "/jump_key" },
    socksProxy: { host: "socks.hidden", port: 1080, username: "proxyuser" },
    createdAt: 0,
    updatedAt: 0,
  };
  return [
    { ...base, id: "ssh", name: "Web 1", host: "web1.internal" },
    {
      ...base,
      id: "ftp",
      name: "Files",
      host: "web2.internal",
      fileTransfer: { enabled: true, protocol: "ftp" as const, host: "ftp.example", port: 2121, username: "ftpuser", viaCurrentSsh: false },
    },
  ] satisfies SshConnectionProfile[];
});
vi.mock("@/stores/ssh-connections", () => ({
  useSshConnectionsStore: () => ({
    hydrated: true,
    hydrate: vi.fn(),
    sortedProfiles: profiles,
    findProfile: (id: string) => profiles.find((item) => item.id === id),
    credentialsFor: () => ({ password: "ssh-secret-pass", ftpPassword: "ftp-secret-pass", passphrase: "phrase-secret" }),
  }),
}));
vi.mock("@/components/ui/MonacoTextEditor.vue", async () => {
  const { defineComponent: define, h: render } = await import("vue");
  return { __esModule: true, default: define({ name: "MonacoTextEditorStub", setup: () => () => render("div") }) };
});

// AppContextMenu and the Element Plus items need a live popper; these stubs render the menu inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  emits: ["close"],
  setup(_, { slots }) {
    return () => h("ul", { class: "context-menu-stub" }, slots.default?.());
  },
});
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { icon: { type: [Object, Function], default: undefined } },
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});
const stubs = {
  AppContextMenu: ContextMenu,
  ElDropdownItem: DropdownItem,
  "el-dropdown-item": DropdownItem,
};

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "sftp/file/context",
  command: "inspect",
  title: "Inspect file",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: true,
  hidden: false,
};

const SECRETS = ["ssh-secret-pass", "ftp-secret-pass", "phrase-secret", "id_secret", "jump_key", "bastion", "jumper", "socks", "proxyuser", "TOKEN"];

const remoteFile = { name: "app.conf", path: "/home/deploy/app.conf", isDir: false, size: 5, permissions: "-rw-r--r--" };
const remoteDir = { name: "logs", path: "/home/deploy/logs", isDir: true, size: 4096, permissions: "drwxr-xr-x" };
// A symbolic link as sftp_list reports it: formatPermissions marks os.ModeSymlink with "l"
// (Go: backend/sftp/sftp.go, TestFormatPermissionsMarksEntryType); isDir is false (not followed).
const remoteLink = { name: "link.conf", path: "/home/deploy/link.conf", isDir: false, size: 11, permissions: "lrwxrwxrwx" };
const longFile = { name: "long.txt", path: `/${"a".repeat(4096)}`, isDir: false, size: 1, permissions: "-rw-r--r--" };

let wrapper: VueWrapper | undefined;

function contextEvent() {
  return new MouseEvent("contextmenu", { clientX: 10, clientY: 10 });
}

function pluginMenus() {
  return wrapper!.findAllComponents(PluginMenuItems);
}

async function runLastItem() {
  const items = wrapper!.findComponent(ContextMenu).findAll("[role=menuitem]");
  const last = items.at(-1)!;
  expect(last.text()).toBe("Inspect file");
  expect(last.attributes("divided")).toBeDefined();
  await last.trigger("click");
  await flushPromises();
}

function lastInvocation() {
  const [, , invocation] = plugin.execute.mock.calls.at(-1)! as [string, string, { menu: string; context: Record<string, unknown> }];
  expect(invocation.menu).toBe("sftp/file/context");
  const serialized = JSON.stringify(invocation.context);
  for (const secret of SECRETS) expect(serialized).not.toContain(secret);
  return invocation.context;
}

beforeEach(() => {
  plugin.execute.mockReset();
  route.query = { profile: "ssh" };
  sftp.connectSftp.mockResolvedValue("/home/deploy");
  sftp.disconnectSftp.mockResolvedValue(undefined);
  sftp.listSftpDirectory.mockResolvedValue([remoteDir, remoteFile, remoteLink, longFile]);
  sftp.listLocalFiles.mockResolvedValue([{ name: "local.txt", path: "local.txt", isDir: false, size: 3 }]);
  pluginMenuItems.value = [MENU_ITEM];
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  pluginMenuItems.value = [];
  vi.clearAllMocks();
});

describe("SftpWorkspaceView remote file plugin menus", () => {
  async function mountView() {
    wrapper = mount(SftpWorkspaceView, { attachTo: document.body, global: { stubs } });
    await flushPromises();
    expect(sftp.connectSftp).toHaveBeenCalledTimes(1);
  }

  function remoteList() {
    return wrapper!.findAllComponents(SftpFileList).at(-1)!;
  }

  it("adds plugin items to a remote row's menu with the SSH endpoint and no credentials", async () => {
    await mountView();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), remoteFile);
    await flushPromises();
    expect(pluginMenus()).toHaveLength(1);
    expect(pluginMenus()[0]!.props("host")).toEqual({ "sftp.kind": "file", "sftp.extname": ".conf", "sftp.protocol": "sftp" });
    await runLastItem();
    expect(lastInvocation()).toEqual({
      connection: { id: "ssh", name: "Web 1", protocol: "sftp", host: "web1.internal", port: 2222, username: "deploy" },
      path: "/home/deploy/app.conf",
      name: "app.conf",
      kind: "file",
      size: 5,
    });
  });

  it("evaluates sftp.kind for directories", async () => {
    pluginMenuItems.value = [{ ...MENU_ITEM, whenClauses: parseWhenExpression('sftp.kind == "directory"') }];
    await mountView();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), remoteFile);
    await flushPromises();
    expect(wrapper!.findAll(".app-plugin-menu-item")).toHaveLength(0);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), remoteDir);
    await flushPromises();
    await runLastItem();
    expect(lastInvocation()).toMatchObject({ path: "/home/deploy/logs", name: "logs", kind: "directory" });
  });

  it("keeps plugin items out of the blank-area and local-pane menus", async () => {
    await mountView();
    await wrapper!.find(".sftp-file-pane--remote").trigger("contextmenu");
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(pluginMenus()).toHaveLength(0);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();

    wrapper!.findAllComponents(SftpFileList).at(0)!.vm.$emit("row-contextmenu", contextEvent(), {
      name: "local.txt",
      path: "local.txt",
      isDir: false,
      size: 3,
    });
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(pluginMenus()).toHaveLength(0);
  });

  it("uses the FTP transfer endpoint when the profile has one", async () => {
    route.query = { profile: "ftp" };
    await mountView();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), remoteFile);
    await flushPromises();
    expect(pluginMenus()[0]!.props("host")).toMatchObject({ "sftp.protocol": "ftp" });
    await runLastItem();
    expect(lastInvocation()).toMatchObject({
      connection: { id: "ftp", name: "Files", protocol: "ftp", host: "ftp.example", port: 2121, username: "ftpuser" },
    });
  });

  it("refuses to send a path over remotePathMaxBytes", async () => {
    await mountView();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), longFile);
    await flushPromises();
    await runLastItem();
    expect(plugin.execute).not.toHaveBeenCalled();
    expect(dialogs.error).toHaveBeenCalledWith(expect.stringMatching(/所选对象过长|too long/));
  });

  it("closes the context menu after a plugin item runs", async () => {
    await mountView();
    remoteList().vm.$emit("row-contextmenu", contextEvent(), remoteFile);
    await flushPromises();
    await runLastItem();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(false);
  });
});

describe("SshSftpPanel remote file plugin menus", () => {
  async function mountPanel(profileIndex = 0) {
    wrapper = mount(SshSftpPanel, {
      props: { visible: true, sshSessionId: "ssh-1", profile: profiles[profileIndex]!, credentials: { password: "ssh-secret-pass" } },
      attachTo: document.body,
      global: { stubs },
    });
    await flushPromises();
  }

  function row(name: string) {
    return wrapper!.findAll(".ssh-sftp-panel__row").find((item) => item.text().includes(name))!;
  }

  it("adds plugin items to a row's menu with whitelisted context", async () => {
    await mountPanel();
    await row("app.conf").trigger("contextmenu");
    await flushPromises();
    expect(pluginMenus()).toHaveLength(1);
    await runLastItem();
    expect(lastInvocation()).toEqual({
      connection: { id: "ssh", name: "Web 1", protocol: "sftp", host: "web1.internal", port: 2222, username: "deploy" },
      path: "/home/deploy/app.conf",
      name: "app.conf",
      kind: "file",
      size: 5,
    });
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(false);
  });

  it("keeps plugin items out of the blank-area menu", async () => {
    await mountPanel();
    await wrapper!.find(".ssh-sftp-panel__list").trigger("contextmenu");
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(pluginMenus()).toHaveLength(0);
  });

  it("uses the FTP endpoint and refuses an over-long path", async () => {
    await mountPanel(1);
    await row("app.conf").trigger("contextmenu");
    await flushPromises();
    await runLastItem();
    expect(lastInvocation()).toMatchObject({
      connection: { id: "ftp", protocol: "ftp", host: "ftp.example", port: 2121, username: "ftpuser" },
    });

    await row("long.txt").trigger("contextmenu");
    await flushPromises();
    plugin.execute.mockClear();
    await runLastItem();
    expect(plugin.execute).not.toHaveBeenCalled();
    expect(dialogs.error).toHaveBeenCalledWith(expect.stringMatching(/所选对象过长|too long/));
  });
});

// ---------------------------------------------------------------------------
// "Process with plugin" (contributes.services surface sftpFile) on remote rows.

const { t } = useVueI18n();
const SERVICE_MENU = t("plugins.services.menu");
const CREDENTIAL_KEYS = ["password", "privateKey", "passphrase", "ftpPassword", "keyFile"];

function sftpProvider(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.logs",
    pluginName: "Demo Logs",
    entryId: "analyze",
    service: "demo.file.analyze",
    title: "Analyze file",
    description: "",
    input: { kind: "file", accepts: [".conf", ".txt"], maxBytes: 4096 },
    output: { kind: "text" },
    surfaces: ["sftpFile"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

function menuLabels() {
  return wrapper!.findComponent(ContextMenu).findAll("[role=menuitem]").map((item) => item.text());
}

function serviceExpander() {
  return wrapper!.findComponent(ContextMenu).findAll("[role=menuitem]").find((item) => item.text() === SERVICE_MENU);
}

/** Expands "Process with plugin", picks the service and returns the request handed to runPluginService. */
async function runService(title = "Analyze file") {
  const expander = serviceExpander();
  expect(expander, "service entry").toBeDefined();
  await expander!.trigger("click");
  const item = wrapper!.findComponent(ContextMenu).findAll("[role=menuitem]").find((entry) => entry.text() === title);
  expect(item, title).toBeDefined();
  await item!.trigger("click");
  await flushPromises();
  expect(services.run).toHaveBeenCalledTimes(1);
  const [request] = services.run.mock.calls[0]! as [Record<string, unknown>];
  const serialized = JSON.stringify(request);
  for (const key of CREDENTIAL_KEYS) expect(serialized).not.toContain(`"${key}"`);
  for (const secret of SECRETS) expect(serialized).not.toContain(secret);
  return request;
}

describe("SftpWorkspaceView 'Process with plugin' on remote files", () => {
  beforeEach(() => {
    services.entries.value = [sftpProvider()];
    services.run.mockReset();
  });
  afterEach(() => {
    services.entries.value = [];
  });

  async function mountView() {
    wrapper = mount(SftpWorkspaceView, { attachTo: document.body, global: { stubs } });
    await flushPromises();
  }

  function remoteList() {
    return wrapper!.findAllComponents(SftpFileList).at(-1)!;
  }

  async function openRow(item: typeof remoteFile) {
    remoteList().vm.$emit("row-contextmenu", contextEvent(), item);
    await flushPromises();
  }

  it("follows the plugin menu items and hands over the session and path, never credentials", async () => {
    await mountView();
    await openRow(remoteFile);
    const labels = menuLabels();
    expect(labels.indexOf(SERVICE_MENU)).toBeGreaterThan(labels.indexOf("Inspect file"));
    expect(labels.at(-1)).toBe(SERVICE_MENU);
    expect(await runService()).toEqual({
      service: "demo.file.analyze",
      surface: "sftpFile",
      remoteFile: { sessionId: "sftp-scope-ssh", path: "/home/deploy/app.conf" },
      surfaceContext: {
        remote: {
          connection: { id: "ssh", name: "Web 1", protocol: "sftp", host: "web1.internal", port: 2222, username: "deploy" },
          path: "/home/deploy/app.conf",
        },
      },
    });
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(false);
  });

  it("does not appear on directories, symbolic links, the blank area or the local pane", async () => {
    await mountView();
    await openRow(remoteDir);
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();

    await openRow(remoteLink);
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();

    await wrapper!.find(".sftp-file-pane--remote").trigger("contextmenu");
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();

    wrapper!.findAllComponents(SftpFileList).at(0)!.vm.$emit("row-contextmenu", contextEvent(), {
      name: "local.txt",
      path: "local.txt",
      isDir: false,
      size: 3,
    });
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(menuLabels()).not.toContain(SERVICE_MENU);
  });

  it("uses the endpoint the session was opened with, not a profile edited afterwards", async () => {
    await mountView();
    const original = { ...profiles[0]! };
    try {
      profiles[0]!.host = "edited.internal";
      profiles[0]!.name = "Edited";
      await openRow(remoteFile);
      const request = await runService();
      expect(request.surfaceContext).toEqual({
        remote: {
          connection: { id: "ssh", name: "Web 1", protocol: "sftp", host: "web1.internal", port: 2222, username: "deploy" },
          path: "/home/deploy/app.conf",
        },
      });
    } finally {
      Object.assign(profiles[0]!, original);
    }
  });

  it("uses the FTP transfer endpoint and session", async () => {
    route.query = { profile: "ftp" };
    await mountView();
    await openRow(remoteFile);
    expect(await runService()).toMatchObject({
      remoteFile: { sessionId: "sftp-scope-ftp", path: "/home/deploy/app.conf" },
      surfaceContext: { remote: { connection: { id: "ftp", protocol: "ftp", host: "ftp.example", port: 2121, username: "ftpuser" } } },
    });
  });

  it("has no target once the menu closes or the view unmounts", async () => {
    await mountView();
    await openRow(remoteFile);
    let target = wrapper!.findComponent(PluginServiceMenuItems).props("target") as () => unknown;
    expect(target()).toMatchObject({ surface: "sftpFile", sessionId: "sftp-scope-ssh" });
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();
    expect(target()).toBeNull();

    await openRow(remoteFile);
    target = wrapper!.findComponent(PluginServiceMenuItems).props("target") as () => unknown;
    expect(target()).not.toBeNull();
    wrapper!.unmount();
    wrapper = undefined;
    expect(target()).toBeNull();
  });
});

describe("SshSftpPanel 'Process with plugin' on remote files", () => {
  beforeEach(() => {
    services.entries.value = [sftpProvider()];
    services.run.mockReset();
  });
  afterEach(() => {
    services.entries.value = [];
  });

  async function mountPanel(profileIndex = 0) {
    wrapper = mount(SshSftpPanel, {
      props: { visible: true, sshSessionId: "ssh-1", profile: profiles[profileIndex]!, credentials: { password: "ssh-secret-pass" } },
      attachTo: document.body,
      global: { stubs },
    });
    await flushPromises();
  }

  function row(name: string) {
    return wrapper!.findAll(".ssh-sftp-panel__row").find((item) => item.text().includes(name))!;
  }

  it("follows the plugin menu items and hands over the panel's session and path", async () => {
    await mountPanel();
    await row("app.conf").trigger("contextmenu");
    await flushPromises();
    const labels = menuLabels();
    expect(labels.indexOf(SERVICE_MENU)).toBeGreaterThan(labels.indexOf("Inspect file"));
    expect(await runService()).toEqual({
      service: "demo.file.analyze",
      surface: "sftpFile",
      remoteFile: { sessionId: "ssh-1-files-sftp", path: "/home/deploy/app.conf" },
      surfaceContext: {
        remote: {
          connection: { id: "ssh", name: "Web 1", protocol: "sftp", host: "web1.internal", port: 2222, username: "deploy" },
          path: "/home/deploy/app.conf",
        },
      },
    });
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(false);
  });

  it("does not appear on directories, symbolic links or the blank area", async () => {
    await mountPanel();
    await row("logs").trigger("contextmenu");
    await flushPromises();
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();
    await row("link.conf").trigger("contextmenu");
    await flushPromises();
    expect(menuLabels()).toContain("Inspect file");
    expect(menuLabels()).not.toContain(SERVICE_MENU);
    wrapper!.findComponent(ContextMenu).vm.$emit("close");
    await flushPromises();
    await wrapper!.find(".ssh-sftp-panel__list").trigger("contextmenu");
    await flushPromises();
    expect(wrapper!.findComponent(ContextMenu).exists()).toBe(true);
    expect(menuLabels()).not.toContain(SERVICE_MENU);
  });

  it("keeps the connected endpoint when the profile prop is edited, and drops the target on unmount", async () => {
    await mountPanel();
    await wrapper!.setProps({ profile: { ...profiles[0]!, host: "edited.internal", name: "Edited" } });
    await row("app.conf").trigger("contextmenu");
    await flushPromises();
    expect(await runService()).toMatchObject({
      remoteFile: { sessionId: "ssh-1-files-sftp" },
      surfaceContext: { remote: { connection: { name: "Web 1", host: "web1.internal" } } },
    });
    await row("app.conf").trigger("contextmenu");
    await flushPromises();
    const target = wrapper!.findComponent(PluginServiceMenuItems).props("target") as () => unknown;
    expect(target()).not.toBeNull();
    wrapper!.unmount();
    wrapper = undefined;
    expect(target()).toBeNull();
  });
});
