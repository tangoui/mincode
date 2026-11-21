import { afterEach, describe, expect, it, vi } from "vitest";
import { reactive } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import DatabaseConnectionDialog from "@/components/database/DatabaseConnectionDialog.vue";
import { useDatabaseDriversStore } from "@/stores/database-drivers";
import type { PluginDatabaseDriver } from "@/domain/plugins/database-drivers";
import type { DatabaseConnection, DbType } from "@/domain/database/types";
import { messagesZh } from "@/i18n/locales/zh";

const router = vi.hoisted(() => ({ push: vi.fn(() => Promise.resolve()) }));
vi.mock("vue-router", () => ({ useRouter: () => router }));
const installedPlugins = vi.hoisted(() => ({ ids: [] as string[] }));
vi.mock("@/services/plugin-service", () => ({
  listPlugins: () => Promise.resolve(installedPlugins.ids.map((id) => ({ id }))),
}));
const supportedTypes = vi.hoisted(() => ({ value: null as Set<string> | null }));
vi.mock("@/services/database-capabilities", () => ({ loadSupportedDatabaseTypes: () => Promise.resolve(supportedTypes.value) }));

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  installedPlugins.ids = [];
  supportedTypes.value = null;
});

const provider = "企业数据库驱动 / Enterprise Database Driver ".repeat(4);
function driver(id: string, title: string): PluginDatabaseDriver {
  return {
    key: `plugin:enterprise-driver/${id}`, pluginId: "enterprise-driver", pluginName: provider,
    pluginVersion: "0.1.0", driverId: id, title, category: "relational", sqlDialect: "generic",
    dbType: "plugin", dialectType: "mysql", fields: [], required: [], fileExtensions: [], features: [],
  };
}

describe("database driver picker", () => {
  it("lists plugin drivers as compact cards and opens the form on click", async () => {
    const store = useDatabaseDriversStore();
    store.drivers = [driver("exasol", "Exasol"), driver("hana", "SAP HANA"), driver("vertica", "Vertica")];
    wrapper = mount(DatabaseConnectionDialog, { props: { modelValue: false }, attachTo: document.body });
    await wrapper.setProps({ modelValue: true });
    await flushPromises();
    const category = [...document.querySelectorAll<HTMLButtonElement>(".db-type-picker__categories button")].at(-1);
    category?.click();
    await flushPromises();
    const cards = [...document.querySelectorAll<HTMLButtonElement>(".db-type-card.is-plugin")];
    expect(cards).toHaveLength(3);
    for (const card of cards) {
      // Compact card: logo, a two-line text block, and a hover arrow; the long provider is one truncated line.
      expect(card.querySelector(":scope > .db-type-card__logo")).toBeTruthy();
      const text = card.querySelector(".db-type-card__content small");
      expect(text?.textContent).toContain(provider);
      expect(card.querySelector(".el-tag")).toBeNull();
      expect(card.querySelector(":scope > .db-type-card__arrow")).toBeTruthy();
    }
    cards[1].click();
    await flushPromises();
    expect(document.querySelector(".db-connection-form-shell")).toBeTruthy();
    expect(document.body.textContent).toContain("SAP HANA");
  });
});

describe("built-in type picker search", () => {
  async function search(query: string) {
    const input = document.querySelector<HTMLInputElement>(".db-type-picker__search input");
    expect(input).toBeTruthy();
    input!.value = query;
    input!.dispatchEvent(new Event("input"));
    await flushPromises();
    return [...document.querySelectorAll<HTMLButtonElement>(".db-type-card:not(.is-plugin)")].map((card) =>
      card.querySelector(".db-type-card__content b")?.textContent?.trim(),
    );
  }

  it("finds compatible databases by name and the generic entries by custom-type searches", async () => {
    useDatabaseDriversStore().drivers = [];
    wrapper = mount(DatabaseConnectionDialog, { props: { modelValue: false }, attachTo: document.body });
    await wrapper.setProps({ modelValue: true });
    await flushPromises();
    expect(await search("dolt")).toEqual(["Dolt"]);
    expect(await search("GreatSQL")).toEqual(["GreatSQL"]);
    expect(await search("opentenbase")).toEqual(["OpenTenBase"]);
    expect(await search("Custom MySQL")).toEqual(["MySQL"]);
    expect(await search("自定义 PostgreSQL")).toEqual(["PostgreSQL"]);
    // The generic PostgreSQL card says it also covers compatible databases.
    const subtitle = document.querySelector(".db-type-card:not(.is-plugin) .db-type-card__content small");
    expect(subtitle?.textContent).toMatch(/PostgreSQL/);
  });
});

describe("plugin driver credential notice", () => {
  it("is a short line beside the provider, with the full consent text in a tooltip", async () => {
    const source = await import("node:fs/promises").then((fs) => fs.readFile("src/components/database/DatabaseConnectionDialog.vue", "utf8"));
    const start = source.indexOf('class="db-driver-section__provider"');
    const block = source.slice(start, source.indexOf("</template>", source.indexOf("<el-tooltip", start)) + 40);
    // One line: the provider text plus a short warning label, no separate banner.
    expect(source).not.toContain('class="db-driver-section__release"');
    expect(block).toContain("db-driver-section__release-label");
    expect(block).toContain("releaseTitle");
    // The full consent sentences are still reachable on hover/focus.
    expect(block).toMatch(/<el-tooltip[^>]*:content="pluginReleaseNotice"/);
    expect(block).toMatch(/tabindex="0"/);
    const notice = source.slice(source.indexOf("const pluginReleaseNotice"), source.indexOf("const pluginSecretFieldsLabel"));
    for (const key of ["releaseSecretBody", "releaseBody", "releaseOnSave", "releaseNeedsConfirm"]) expect(notice).toContain(key);
    // When the binding was not chosen here, the reconfirm requirement stays visible, not hover-only.
    expect(source).toMatch(/v-else-if="pluginDriver && !pluginReleaseConfirmed"[^>]*>[\s\S]{0,120}releaseNeedsConfirm/);
  });
});

describe("plugin driver credential notice (rendered)", () => {
  it("shows one provider line with a warning label whose tooltip carries the consent text", async () => {
    const store = useDatabaseDriversStore();
    store.drivers = [driver("dynamo", "Amazon DynamoDB")];
    wrapper = mount(DatabaseConnectionDialog, { props: { modelValue: false }, attachTo: document.body });
    await wrapper.setProps({ modelValue: true });
    await flushPromises();
    [...document.querySelectorAll<HTMLButtonElement>(".db-type-picker__categories button")].at(-1)?.click();
    await flushPromises();
    document.querySelector<HTMLButtonElement>(".db-type-card.is-plugin")?.click();
    await flushPromises();
    const line = document.querySelector(".db-driver-section__provider");
    expect(line?.textContent).toContain("enterprise-driver");
    const label = line?.querySelector<HTMLElement>(".db-driver-section__release-label");
    expect(label).toBeTruthy();
    // No banner below the line any more.
    expect(document.querySelector(".db-connection-form .el-alert--warning")).toBeNull();
    // Keyboard and hover users get the full text: as the accessible name and in the tooltip.
    expect(label?.getAttribute("tabindex")).toBe("0");
    expect(label?.getAttribute("aria-label")).toContain(provider.trim());
  });
});

describe("JDBC connection entry and plugin driver suggestions", () => {
  const jdbcGeneric: PluginDatabaseDriver = {
    ...driver("generic", "JDBC Connection"),
    key: "plugin:jdbc-bridge/generic",
    pluginId: "jdbc-bridge",
    pluginName: "JDBC 桥接 / JDBC Bridge",
    titleLocalized: { zh: "JDBC 连接" },
  };

  async function openPicker() {
    wrapper = mount(DatabaseConnectionDialog, { props: { modelValue: false }, attachTo: document.body });
    await wrapper.setProps({ modelValue: true });
    await flushPromises();
  }

  async function search(query: string) {
    const input = document.querySelector<HTMLInputElement>(".db-type-picker__search input");
    expect(input).toBeTruthy();
    input!.value = query;
    input!.dispatchEvent(new Event("input"));
    await flushPromises();
  }

  it("JDBC button opens generic driver or install", async () => {
    router.push.mockClear();
    // Not installed: the button says so and opens the plugin manager searching jdbc-bridge.
    await openPicker();
    const button = document.querySelector<HTMLButtonElement>(".db-type-picker__jdbc");
    expect(button).toBeTruthy();
    expect(button?.getAttribute("aria-description") ?? button?.title).toBeTruthy();
    button!.click();
    await flushPromises();
    expect(router.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "jdbc-bridge" } });
    expect(wrapper!.emitted("update:modelValue")?.at(-1)).toEqual([false]);
    wrapper!.unmount();
    wrapper = undefined;
    document.body.innerHTML = "";

    // Installed and enabled: the button binds the generic JDBC driver and opens the form.
    router.push.mockClear();
    useDatabaseDriversStore().drivers = [jdbcGeneric];
    await openPicker();
    document.querySelector<HTMLButtonElement>(".db-type-picker__jdbc")!.click();
    await flushPromises();
    expect(router.push).not.toHaveBeenCalled();
    expect(document.querySelector(".db-connection-form-shell")).toBeTruthy();
    expect(document.querySelector(".db-driver-section__provider")?.textContent).toContain("jdbc-bridge");
  });

  it("search offers an install card for databases from plugins that are not installed", async () => {
    router.push.mockClear();
    await openPicker();
    await search("db2");
    const card = document.querySelector<HTMLButtonElement>(".db-type-card.is-suggestion");
    expect(card?.textContent).toContain("IBM Db2");
    expect(card?.textContent).toContain("IBM Db2 Driver");
    card!.click();
    await flushPromises();
    expect(router.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "db2-driver" } });
  });

  it("finds jdbc-bridge's own Caché / SUNDB / 神通 drivers once it is installed", async () => {
    router.push.mockClear();
    const oscar: PluginDatabaseDriver = {
      ...jdbcGeneric, key: "plugin:jdbc-bridge/oscar", driverId: "oscar", title: "ShenTong OSCAR", titleLocalized: { zh: "神通 OSCAR" },
    };
    useDatabaseDriversStore().drivers = [oscar, jdbcGeneric];
    await openPicker();
    await search("神通");
    // The installed driver card, not an install suggestion.
    expect(document.querySelector(".db-type-card.is-suggestion")).toBeNull();
    const cards = [...document.querySelectorAll<HTMLButtonElement>(".db-type-card.is-plugin:not(.is-suggestion)")];
    expect(cards.map((card) => card.querySelector("b")?.textContent)).toEqual(["神通 OSCAR"]);
    // Installed plugin drivers are not offered again as install cards.
    await search("teradata");
    expect(document.querySelector(".db-type-card.is-suggestion")).toBeNull();
    await search("ShenTong");
    document.querySelector<HTMLButtonElement>(".db-type-card.is-plugin:not(.is-suggestion)")!.click();
    await flushPromises();
    expect(router.push).not.toHaveBeenCalled();
    expect(document.querySelector(".db-connection-form-shell")).toBeTruthy();
    expect(document.querySelector(".db-driver-section__provider")?.textContent).toContain("jdbc-bridge");
  });

  it("says enable instead of install when the plugin is installed but disabled", async () => {
    router.push.mockClear();
    installedPlugins.ids = ["jdbc-bridge", "db2-driver"];
    await openPicker();
    const button = document.querySelector<HTMLButtonElement>(".db-type-picker__jdbc")!;
    expect(button.textContent).toContain(messagesZh["database.jdbcConnection.needsEnable"]);
    expect(button.getAttribute("aria-description")).toBe(messagesZh["database.jdbcConnection.enableHint"]);
    await search("db2");
    const card = document.querySelector<HTMLButtonElement>(".db-type-card.is-suggestion")!;
    expect(card.textContent).toContain(messagesZh["database.pluginSuggestion.enable"].replace("{plugin}", "IBM Db2 驱动 / IBM Db2 Driver"));
    // A vendor-jar driver of a disabled jdbc-bridge: enable copy plus the jar hint in one sentence.
    await search("SUNDB");
    const sundb = document.querySelector<HTMLButtonElement>(".db-type-card.is-suggestion small")!;
    const enable = messagesZh["database.pluginSuggestion.enable"].replace("{plugin}", "JDBC 桥接 / JDBC Bridge");
    expect(sundb.textContent).toBe(messagesZh["database.pluginSuggestion.registerJarHint"].replace("{provider}", enable));
    // Navigation is unchanged: plugin manager searching the plugin.
    document.querySelector<HTMLButtonElement>(".db-type-card.is-suggestion")!.click();
    await flushPromises();
    expect(router.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "jdbc-bridge" } });
  });

  it("finds installed driver cards by suggestion keywords", async () => {
    const xugu: PluginDatabaseDriver = {
      ...driver("xugu", "XuguDB"), key: "plugin:xugudb-driver/xugu", pluginId: "xugudb-driver", pluginName: "XuguDB Driver",
    };
    const informix: PluginDatabaseDriver = {
      ...driver("informix", "IBM Informix"), key: "plugin:jdbc-bridge/informix", pluginId: "jdbc-bridge", pluginName: "JDBC 桥接 / JDBC Bridge",
    };
    useDatabaseDriversStore().drivers = [xugu, informix, jdbcGeneric];
    await openPicker();
    // The English title has no 虚谷; the suggestion keywords find the installed card.
    await search("虚谷");
    expect([...document.querySelectorAll(".db-type-card.is-plugin:not(.is-suggestion) b")].map((node) => node.textContent)).toEqual(["XuguDB"]);
    expect(document.querySelector(".db-type-card.is-suggestion")).toBeNull();
    await search("GBase 8s");
    expect([...document.querySelectorAll(".db-type-card.is-plugin:not(.is-suggestion) b")].map((node) => node.textContent)).toEqual(["IBM Informix"]);
    // jdbc-bridge is installed: its vendor-jar databases are not offered as install cards.
    for (const query of ["Caché", "SUNDB", "OSCAR"]) {
      await search(query);
      expect(document.querySelectorAll(".db-type-card.is-suggestion"), query).toHaveLength(0);
    }
  });

  it("says a vendor jar is needed on the install card of Caché / SUNDB / 神通", async () => {
    await openPicker();
    for (const [query, title] of [["Cache", "InterSystems Caché"], ["科蓝", "科蓝 SUNDB"], ["神通", "神通 OSCAR"]]) {
      await search(query);
      const cards = document.querySelectorAll(".db-type-card.is-suggestion");
      expect(cards, query).toHaveLength(1);
      expect(cards[0]?.querySelector("b")?.textContent).toBe(title);
      const install = messagesZh["database.pluginSuggestion.install"].replace("{plugin}", "JDBC 桥接 / JDBC Bridge");
      expect(cards[0]?.querySelector("small")?.textContent).toBe(messagesZh["database.pluginSuggestion.registerJarHint"].replace("{provider}", install));
    }
  });
});

describe("connection form layout", () => {
  const jdbcGeneric: PluginDatabaseDriver = {
    ...driver("generic", "JDBC Connection"),
    key: "plugin:jdbc-bridge/generic",
    pluginId: "jdbc-bridge",
    pluginName: "JDBC 桥接 / JDBC Bridge",
    titleLocalized: { zh: "JDBC 连接" },
    fields: ["username", "password"],
    options: { properties: { jdbcUrl: { type: "string", title: "JDBC URL", scope: "global" } } },
  };

  function connection(dbType: DbType, overrides: Partial<DatabaseConnection> = {}): DatabaseConnection {
    return {
      id: "conn-1", name: "Test", dbType, host: "localhost", port: 3306,
      ssl: false, savePassword: true, createdAt: 1, updatedAt: 1, ...overrides,
    };
  }

  async function openForm(initial?: DatabaseConnection) {
    wrapper = mount(DatabaseConnectionDialog, { props: { modelValue: false, initial }, attachTo: document.body });
    await wrapper.setProps({ modelValue: true });
    await flushPromises();
  }

  async function openJdbcForm() {
    useDatabaseDriversStore().drivers = [jdbcGeneric];
    await openForm();
    document.querySelector<HTMLButtonElement>(".db-type-picker__jdbc")!.click();
    await flushPromises();
  }

  function formItem(label: string) {
    return [...document.querySelectorAll<HTMLElement>(".db-connection-form .el-form-item")].find(
      (item) => item.querySelector(".el-form-item__label")?.textContent?.trim() === label,
    );
  }

  function leafTexts() {
    return [...document.querySelectorAll<HTMLElement>(".db-connection-form-shell *")]
      .filter((node) => node.children.length === 0)
      .map((node) => node.textContent?.trim());
  }

  /** The nearest element (self included) hidden with display: none, e.g. a collapsed panel. */
  function hiddenAncestor(node: Element) {
    for (let current: Element | null = node; current; current = current.parentElement) {
      if (current instanceof HTMLElement && current.style.display === "none") return current;
    }
    return null;
  }

  function foldHeader(titleKey: "database.section.tunnel" | "database.section.extra") {
    return [...document.querySelectorAll<HTMLElement>(".db-connection-form .el-collapse-item__header")].find((header) =>
      header.textContent?.includes(messagesZh[titleKey]),
    );
  }

  const sshTunnel: NonNullable<DatabaseConnection["tunnel"]> = {
    type: "ssh", host: "jump.example.com", port: 22, username: "ops", authType: "key", keyPath: "~/.ssh/id_rsa",
  };

  function follows(first: Element, second: Element) {
    return Boolean(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING);
  }

  it("title reads 新建连接 · <type> without a repeated word", async () => {
    await openJdbcForm();
    expect(document.querySelector(".db-connection-dialog__header strong")?.textContent?.trim()).toBe("新建连接 · JDBC 连接");
  });

  it("merges the basic info and plugin driver blocks into one type row", async () => {
    await openJdbcForm();
    const texts = leafTexts();
    // The old block headings (基本信息 / 驱动); their keys were removed with the blocks.
    expect(texts).not.toContain("基本信息");
    expect(texts).not.toContain("驱动");
    const row = document.querySelector<HTMLElement>(".db-type-row");
    expect(row).toBeTruthy();
    expect(row!.textContent).toContain("JDBC 连接");
    const button = [...row!.querySelectorAll("button")].find(
      (node) => node.textContent?.trim() === messagesZh["database.previousStep"],
    );
    expect(button).toBeTruthy();
    expect(row!.querySelector(".el-select")).toBeTruthy();
    expect(row!.querySelector(".db-driver-section__provider")?.textContent).toContain("jdbc-bridge");
  });

  it("keeps driver alerts visible under the type row", async () => {
    // Plugin driver no longer in the catalog: driverUnavailableReason.
    await openForm(connection("plugin", { driver: { pluginId: "enterprise-driver", driverId: "gone" } }));
    let row = document.querySelector(".db-type-row")!;
    let alert = [...document.querySelectorAll(".el-alert")].find((node) =>
      node.textContent?.includes(messagesZh["pluginContrib.databaseDrivers.unavailable"]),
    );
    expect(alert).toBeTruthy();
    expect(follows(row, alert!)).toBe(true);
    expect(row.contains(alert!)).toBe(false);
    wrapper!.unmount();
    wrapper = undefined;
    document.body.innerHTML = "";

    // Built-in driver left out of this build: builtinDriverMissing.
    supportedTypes.value = new Set(["sqlite"]);
    await openForm(connection("mysql"));
    row = document.querySelector(".db-type-row")!;
    alert = [...document.querySelectorAll(".el-alert")].find((node) =>
      node.textContent?.includes(messagesZh["pluginContrib.databaseDrivers.runtime.builtinMissingTitle"]),
    );
    expect(alert).toBeTruthy();
    expect(follows(row, alert!)).toBe(true);
    expect(row.contains(alert!)).toBe(false);
    wrapper!.unmount();
    wrapper = undefined;
    document.body.innerHTML = "";
    supportedTypes.value = null;

    // Bound to an available driver, but not chosen in this dialog: re-confirmation notice.
    useDatabaseDriversStore().drivers = [jdbcGeneric];
    await openForm(connection("plugin", { driver: { pluginId: "jdbc-bridge", driverId: "generic" } }));
    row = document.querySelector(".db-type-row")!;
    const notice = document.querySelector(".db-driver-section__reconfirm");
    expect(notice?.textContent).toContain(messagesZh["pluginContrib.databaseDrivers.releaseNeedsConfirm"]);
    expect(follows(row, notice!)).toBe(true);
    expect(row.contains(notice!)).toBe(false);
  });

  it("type row says the driver is unavailable when the built-in driver is missing", async () => {
    supportedTypes.value = new Set(["sqlite"]);
    await openForm(connection("mysql"));
    const hint = document.querySelector(".db-type-row__hint");
    expect(hint?.textContent).toContain(messagesZh["database.driverUnavailable"]);
    expect(hint?.textContent).not.toContain(messagesZh["database.driverHint"].split("{driver}")[0]);
  });

  it("edit title names the missing plugin driver by its id", async () => {
    await openForm(connection("plugin", { driver: { pluginId: "enterprise-driver", driverId: "gone" } }));
    expect(document.querySelector(".db-connection-dialog__header strong")?.textContent?.trim()).toBe("编辑连接 · gone");
  });

  it("edits and saves a connection whose tunnel comes from a reactive store", async () => {
    // Connections come from a Pinia store, so initial.tunnel is a reactive proxy.
    const initial = reactive(connection("mysql", { tunnel: { ...sshTunnel } }));
    await openForm(initial as DatabaseConnection);
    const save = [...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === messagesZh["common.save"]);
    save!.click();
    await flushPromises();
    const saved = wrapper!.emitted("save")?.[0]?.[0] as DatabaseConnection | undefined;
    expect(saved?.tunnel).toEqual(sshTunnel);
    expect(saved?.tunnel).not.toBe(initial.tunnel);
  });

  it("edit title keeps the built-in type name when its bound plugin driver is missing", async () => {
    await openForm(connection("mysql", { driver: { pluginId: "enterprise-driver", driverId: "gone" } }));
    expect(document.querySelector(".db-connection-dialog__header strong")?.textContent?.trim()).toBe("编辑连接 · MySQL");
  });

  it("quick connect still fills the fields", async () => {
    await openForm(connection("mysql"));
    const input = document.querySelector<HTMLInputElement>("#database-quick-connect");
    expect(input).toBeTruthy();
    expect(input!.closest(".db-form-main")).toBeTruthy();
    input!.value = "mysql://u:p@h:3307/db";
    input!.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(formItem(messagesZh["database.host"])?.querySelector("input")?.value).toBe("h");
    expect(formItem(messagesZh["database.port"])?.querySelector("input")?.value).toBe("3307");
    expect(formItem(messagesZh["database.username"])?.querySelector("input")?.value).toBe("u");
    expect(formItem(messagesZh["database.defaultDatabase"])?.querySelector("input")?.value).toBe("db");
  });

  it("file database shows the file picker and no tunnel", async () => {
    await openForm(connection("sqlite", { port: 0, filePath: "/tmp/test.db" }));
    const main = document.querySelector(".db-form-main");
    expect(main).toBeTruthy();
    const file = formItem(messagesZh["database.filePath"]);
    expect(file?.closest(".db-form-main")).toBeTruthy();
    expect(file?.querySelector("input")).toBeTruthy();
    expect([...file!.querySelectorAll("button")].some((node) => node.textContent?.trim() === messagesZh["database.chooseFile"])).toBe(true);
    expect(document.querySelector("#database-quick-connect")).toBeNull();
    expect(formItem(messagesZh["database.tunnelType"])).toBeUndefined();
    expect(leafTexts()).not.toContain(messagesZh["database.section.tunnel"]);
  });

  it("plugin options stay expanded after the credentials", async () => {
    await openJdbcForm();
    const options = document.querySelector<HTMLElement>(".db-form-options");
    expect(options).toBeTruthy();
    expect(options!.style.display).not.toBe("none");
    expect(options!.textContent).toContain(messagesZh["pluginContrib.databaseDrivers.optionsSection"]);
    const jdbcUrl = formItem("JDBC URL")?.querySelector<HTMLInputElement>("input");
    expect(jdbcUrl).toBeTruthy();
    expect(options!.contains(jdbcUrl!)).toBe(true);
    expect(hiddenAncestor(jdbcUrl!)).toBeNull();
    expect(jdbcUrl!.closest(".el-collapse-item:not(.is-active)")).toBeNull();
    const password = document.querySelector<HTMLInputElement>(".db-form-main input[type=password]");
    expect(password).toBeTruthy();
    expect(follows(password!, options!)).toBe(true);
  });

  it("folds tunnel and extra for a new connection", async () => {
    await openForm();
    const mysql = [...document.querySelectorAll<HTMLButtonElement>(".db-type-card:not(.is-plugin)")].find(
      (card) => card.querySelector(".db-type-card__content b")?.textContent?.trim() === "MySQL",
    );
    mysql!.click();
    await flushPromises();
    const tunnel = foldHeader("database.section.tunnel");
    const extra = foldHeader("database.section.extra");
    expect(tunnel).toBeTruthy();
    expect(extra).toBeTruthy();
    expect(tunnel!.getAttribute("aria-expanded")).toBe("false");
    expect(extra!.getAttribute("aria-expanded")).toBe("false");
    expect(tunnel!.textContent).toContain(messagesZh["database.tunnelSummaryNone"]);
    expect(extra!.textContent).toContain(messagesZh["database.extraSummaryEmpty"]);
    // The folded content stays mounted but hidden.
    const tunnelType = formItem(messagesZh["database.tunnelType"]);
    expect(tunnelType).toBeTruthy();
    expect(hiddenAncestor(tunnelType!)).not.toBeNull();
  });

  it("expands sections that have content when editing", async () => {
    await openForm(connection("mysql", { tunnel: sshTunnel, group: "生产" }));
    const tunnel = foldHeader("database.section.tunnel")!;
    const extra = foldHeader("database.section.extra")!;
    expect(tunnel.getAttribute("aria-expanded")).toBe("true");
    expect(extra.getAttribute("aria-expanded")).toBe("true");
    const host = formItem(messagesZh["database.proxyHost"])?.querySelector("input");
    expect(host?.value).toBe("jump.example.com");
    expect(hiddenAncestor(host!)).toBeNull();
    expect(tunnel.textContent).toContain("SSH · jump.example.com");
    expect(extra.textContent).toContain("生产");
  });

  it("summary is part of the collapse header's accessible name", async () => {
    await openForm(connection("mysql", { group: "生产", tags: ["a", "b", "c"] }));
    const tunnel = foldHeader("database.section.tunnel")!;
    const extra = foldHeader("database.section.extra")!;
    for (const header of [tunnel, extra]) {
      // Named by its content: a role=button header with no aria-label overriding the text.
      expect(header.getAttribute("role")).toBe("button");
      expect(header.getAttribute("aria-label")).toBeNull();
      expect(header.getAttribute("tabindex")).toBe("0");
    }
    expect(tunnel.textContent).toContain(messagesZh["database.section.tunnel"]);
    expect(tunnel.textContent).toContain(messagesZh["database.tunnelSummaryNone"]);
    // At most three names, the rest as +N.
    expect(extra.textContent).toContain("生产、a、b +1");
    // Keyboard toggles the fold.
    expect(tunnel.getAttribute("aria-expanded")).toBe("false");
    tunnel.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await flushPromises();
    expect(tunnel.getAttribute("aria-expanded")).toBe("true");
  });

  it("folded sections hold no validated fields", async () => {
    await openForm(connection("mysql", { tunnel: sshTunnel, description: "备注" }));
    const rules = wrapper!.findComponent({ name: "ElForm" }).props("rules") as Record<string, unknown>;
    const keys = Object.keys(rules);
    expect(keys.length).toBeGreaterThan(0);
    for (const key of keys) {
      expect(key).not.toMatch(/^(tunnel|description|group|tags)\b/);
    }
    expect(document.querySelectorAll(".db-connection-form .el-collapse .el-form-item.is-required")).toHaveLength(0);
  });
});
