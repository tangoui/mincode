import { afterEach, describe, expect, it, vi } from "vitest";
import { reactive } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import DatabaseConnectionDialog from "@/components/database/DatabaseConnectionDialog.vue";
import type { DatabaseConnection, DbType } from "@/domain/database/types";
import { messagesZh } from "@/i18n/locales/zh";

const supportedTypes = vi.hoisted(() => ({ value: null as Set<string> | null }));
vi.mock("@/services/database-capabilities", () => ({ loadSupportedDatabaseTypes: () => Promise.resolve(supportedTypes.value) }));

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  supportedTypes.value = null;
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

describe("connection form layout", () => {

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

  it("type row says the driver is unavailable when the built-in driver is missing", async () => {
    supportedTypes.value = new Set(["sqlite"]);
    await openForm(connection("mysql"));
    const hint = document.querySelector(".db-type-row__hint");
    expect(hint?.textContent).toContain(messagesZh["database.driverUnavailable"]);
    expect(hint?.textContent).not.toContain(messagesZh["database.driverHint"].split("{driver}")[0]);
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

  it("requires explicit confirmation before a legacy binding can use a built-in driver", async () => {
    supportedTypes.value = new Set(["mysql"]);
    const original = connection("mysql", {
      driver: { pluginId: "enterprise-driver", driverId: "gone" },
      options: { selector: "old" },
      database: "orders",
    });
    await openForm(original);
    const button = (key: "database.testConnection" | "database.confirmBuiltinDriver" | "common.save") =>
      [...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === messagesZh[key]);

    button("database.testConnection")?.click();
    await flushPromises();
    expect(wrapper!.emitted("test")).toBeUndefined();
    expect(button("database.confirmBuiltinDriver")).toBeDefined();

    button("database.confirmBuiltinDriver")!.click();
    await flushPromises();
    button("common.save")!.click();
    await flushPromises();
    expect(wrapper!.emitted("save")?.at(-1)?.[0]).toMatchObject({
      id: original.id,
      dbType: "mysql",
      database: "orders",
      driver: undefined,
      options: undefined,
    });
    expect(original.driver?.driverId).toBe("gone");
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
