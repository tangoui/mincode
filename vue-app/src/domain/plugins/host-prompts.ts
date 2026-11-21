// Host prompts plugins and service calls wait on (host_ui.go
// host.quickPick / host.inputBox, the host.plugins.invoke, host.database and
// host.ssh.files approvals, and the serviceProvider / serviceConfirm prompts of
// host_services.go and service_commands.go). Kept out of host-ui.ts so the
// entry bundle does not carry them: services/plugin-host-ui.ts loads this
// module when the first prompt arrives. For service calls the host makes
// itself the origin is HOST_PROMPT_ORIGIN instead of a plugin.

import { serviceNameError } from "@/domain/plugins/service-name";
import {
  HOST_PROMPT_ORIGIN,
  label,
  origin,
  PLUGIN_ID,
  PROMPT_ID,
  record,
  text,
  utf8Length,
  type Origin,
} from "@/domain/plugins/host-ui";

export { HOST_PROMPT_ORIGIN };

const ITEM_ID = /^[A-Za-z0-9._:/@#+-]{1,128}$/;
/** Reply id that makes the picked provider the service's default (serviceProviderRememberID). */
export const SERVICE_PROVIDER_REMEMBER_ID = "remember";

export interface PluginQuickPickItem {
  id: string;
  label: string;
  description?: string;
  detail?: string;
}

interface PromptBase extends Origin {
  id: string;
  title: string;
  placeholder?: string;
}

export interface PluginQuickPickPrompt extends PromptBase {
  kind: "quickPick";
  canPickMany: boolean;
  items: PluginQuickPickItem[];
}

export interface PluginInputBoxPrompt extends PromptBase {
  kind: "inputBox";
  prompt?: string;
  value: string;
  password: boolean;
}

/** Approval for host.plugins.invoke: one plugin calling another plugin's action. */
export interface PluginCallPrompt extends PromptBase {
  kind: "pluginCall";
  canRemember: boolean;
  call: {
    targetPluginId: string;
    targetName: string;
    action: string;
    description?: string;
    risk: "read" | "network" | "write" | "dangerous";
    payload: string;
  };
}

/** Approval for host.database.*: read-only schema access to one saved connection. */
export interface PluginDatabaseSchemaPrompt extends PromptBase {
  kind: "databaseSchema";
  canRemember: boolean;
  database: { connectionId: string; name: string; kind: string; database?: string };
}

/** Approval for host.ssh.files.authorize: backup files in one directory of one saved SSH connection. */
export interface PluginSSHFilesPrompt extends PromptBase {
  kind: "sshFiles";
  canRemember: boolean;
  sshFiles: { connectionId: string; connectionName: string; host: string; port: number; username: string; directory: string };
}

/**
 * Choice of the provider of a service call (several providers, no default):
 * items are the providers (label = plugin name, description = entry title).
 * fromHost marks a call the host makes from one of its surfaces.
 */
export interface PluginServiceProviderPrompt extends PromptBase {
  kind: "serviceProvider";
  fromHost: boolean;
  service: string;
  items: PluginQuickPickItem[];
}

/** Confirmation of a dangerous service call the host makes (command palette rule). */
export interface PluginServiceConfirmPrompt extends PromptBase {
  kind: "serviceConfirm";
  fromHost: true;
  call: {
    targetPluginId: string;
    targetName: string;
    service: string;
    serviceTitle: string;
    description?: string;
    risk: PluginCallPrompt["call"]["risk"];
    payload: string;
  };
}

export type PluginPrompt =
  | PluginQuickPickPrompt
  | PluginInputBoxPrompt
  | PluginCallPrompt
  | PluginDatabaseSchemaPrompt
  | PluginSSHFilesPrompt
  | PluginServiceProviderPrompt
  | PluginServiceConfirmPrompt;

const RISKS = new Set(["read", "network", "write", "dangerous"]);
const SERVICE_PROMPT_KINDS = new Set(["serviceProvider", "serviceConfirm"]);

function quickPickItems(value: unknown): PluginQuickPickItem[] | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > 200) return undefined;
  const seen = new Set<string>();
  const items: PluginQuickPickItem[] = [];
  for (const entry of value) {
    const item = record(entry);
    const itemId = item && text(item.id, 128);
    const itemLabel = item && label(item.label, 200)?.trim();
    if (!item || !itemId || !ITEM_ID.test(itemId) || seen.has(itemId) || !itemLabel) return undefined;
    seen.add(itemId);
    const out: PluginQuickPickItem = { id: itemId, label: itemLabel };
    const description = label(item.description, 200)?.trim();
    const detail = label(item.detail, 600)?.trim();
    if (description) out.description = description;
    if (detail) out.detail = detail;
    items.push(out);
  }
  return items;
}

function serviceName(value: unknown): string | undefined {
  return typeof value === "string" && !serviceNameError(value) ? value : undefined;
}

export function parsePluginPrompt(value: unknown): PluginPrompt | undefined {
  const raw = record(value);
  const from = raw && origin(raw, SERVICE_PROMPT_KINDS.has(raw.kind as string));
  const id = raw && text(raw.id, 64);
  if (!raw || !from || !id || !PROMPT_ID.test(id)) return undefined;
  const base: PromptBase = { ...from, id, title: label(raw.title, 200)?.trim() ?? "" };
  const placeholder = label(raw.placeholder, 200)?.trim();
  if (placeholder) base.placeholder = placeholder;
  const fromHost = from.pluginId === HOST_PROMPT_ORIGIN;
  if (raw.kind === "quickPick") {
    const items = quickPickItems(raw.items);
    if (!items) return undefined;
    return { ...base, kind: "quickPick", canPickMany: raw.canPickMany === true, items };
  }
  if (raw.kind === "serviceProvider") {
    const service = serviceName(raw.service);
    const items = quickPickItems(raw.items);
    if (!service || !items || items.some((item) => item.id === SERVICE_PROVIDER_REMEMBER_ID)) return undefined;
    return { ...base, kind: "serviceProvider", fromHost, service, items };
  }
  if (raw.kind === "serviceConfirm") {
    const call = record(raw.call);
    const targetPluginId = call && text(call.targetPluginId, 128);
    const service = call && serviceName(call.service);
    const risk = call && text(call.risk, 16);
    const ids = Array.isArray(raw.items) ? raw.items.map((item) => record(item)?.id) : [];
    if (!fromHost || !call || !targetPluginId || !PLUGIN_ID.test(targetPluginId) || !service || !risk || !RISKS.has(risk) || !ids.includes("once")) return undefined;
    const details: PluginServiceConfirmPrompt["call"] = {
      targetPluginId,
      targetName: label(call.targetName, 200)?.trim() || targetPluginId,
      service,
      serviceTitle: label(call.serviceTitle, 200)?.trim() || service,
      risk: risk as PluginServiceConfirmPrompt["call"]["risk"],
      payload: text(call.payload, 4096) ?? "",
    };
    const description = label(call.description, 600)?.trim();
    if (description) details.description = description;
    return { ...base, kind: "serviceConfirm", fromHost: true, call: details };
  }
  if (raw.kind === "pluginCall") {
    const call = record(raw.call);
    const targetPluginId = call && text(call.targetPluginId, 128);
    const action = call && text(call.action, 128);
    const risk = call && text(call.risk, 16);
    const ids = Array.isArray(raw.items) ? raw.items.map((item) => record(item)?.id) : [];
    if (!call || !targetPluginId || !PLUGIN_ID.test(targetPluginId) || !action || !risk || !RISKS.has(risk) || !ids.includes("once")) return undefined;
    const details: PluginCallPrompt["call"] = {
      targetPluginId,
      targetName: label(call.targetName, 200)?.trim() || targetPluginId,
      action,
      risk: risk as PluginCallPrompt["call"]["risk"],
      payload: text(call.payload, 4096) ?? "",
    };
    const description = label(call.description, 600)?.trim();
    if (description) details.description = description;
    return { ...base, kind: "pluginCall", canRemember: ids.includes("always"), call: details };
  }
  if (raw.kind === "databaseSchema") {
    const database = record(raw.database);
    const connectionId = database && text(database.connectionId, 256);
    const kind = database && text(database.kind, 32);
    const ids = Array.isArray(raw.items) ? raw.items.map((item) => record(item)?.id) : [];
    if (!database || !connectionId || !kind || !ids.includes("once")) return undefined;
    const details: PluginDatabaseSchemaPrompt["database"] = { connectionId, name: label(database.name, 200)?.trim() || connectionId, kind };
    const name = label(database.database, 200)?.trim();
    if (name) details.database = name;
    return { ...base, kind: "databaseSchema", canRemember: ids.includes("always"), database: details };
  }
  if (raw.kind === "sshFiles") {
    const details = record(raw.sshFiles);
    const connectionId = details && text(details.connectionId, 256);
    const host = details && label(details.host, 200)?.trim();
    const username = details && label(details.username, 200)?.trim();
    const directory = details && text(details.directory, 1024);
    const port = details?.port;
    const ids = Array.isArray(raw.items) ? raw.items.map((item) => record(item)?.id) : [];
    if (!details || !connectionId || !host || !username || !directory || !ids.includes("once")) return undefined;
    if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535) return undefined;
    if (!(directory === "~" || directory.startsWith("~/") || directory.startsWith("/")) || [...directory].some((char) => char.charCodeAt(0) < 0x20 || char.charCodeAt(0) === 0x7f)) return undefined;
    return {
      ...base,
      kind: "sshFiles",
      canRemember: ids.includes("always"),
      sshFiles: { connectionId, connectionName: label(details.connectionName, 200)?.trim() || connectionId, host, port, username, directory },
    };
  }
  if (raw.kind === "inputBox") {
    if (!base.title) return undefined;
    const prompt: PluginInputBoxPrompt = { ...base, kind: "inputBox", value: text(raw.value, 4096) ?? "", password: raw.password === true };
    const hint = label(raw.prompt, 600)?.trim();
    if (hint) prompt.prompt = hint;
    return prompt;
  }
  return undefined;
}

/** What the user did with a prompt. */
export type PromptAnswer = { cancelled: true } | { ids: string[] } | { value: string };

/** The reply sent back through plugin_host_prompt_reply. */
export function promptReply(prompt: PluginPrompt, answer: PromptAnswer) {
  if ("cancelled" in answer) return { id: prompt.id, reply: { cancelled: true } };
  if ((prompt.kind === "pluginCall" || prompt.kind === "databaseSchema" || prompt.kind === "sshFiles") && "ids" in answer) {
    const [choice] = answer.ids;
    if (answer.ids.length !== 1 || (choice !== "once" && !(choice === "always" && prompt.canRemember))) return undefined;
    return { id: prompt.id, reply: { cancelled: false, ids: [choice] } };
  }
  if (prompt.kind === "serviceConfirm" && "ids" in answer) {
    if (answer.ids.length !== 1 || answer.ids[0] !== "once") return undefined;
    return { id: prompt.id, reply: { cancelled: false, ids: ["once"] } };
  }
  if (prompt.kind === "serviceProvider" && "ids" in answer) {
    const known = new Set(prompt.items.map((item) => item.id));
    const picked = answer.ids.filter((id) => known.has(id));
    const remember = answer.ids.filter((id) => id === SERVICE_PROVIDER_REMEMBER_ID);
    if (picked.length !== 1 || remember.length > 1 || picked.length + remember.length !== answer.ids.length) return undefined;
    return { id: prompt.id, reply: { cancelled: false, ids: remember.length ? [picked[0]!, SERVICE_PROVIDER_REMEMBER_ID] : [picked[0]!] } };
  }
  if (prompt.kind === "quickPick" && "ids" in answer) {
    const known = new Set(prompt.items.map((item) => item.id));
    const ids = [...new Set(answer.ids)].filter((id) => known.has(id));
    if (!ids.length || (!prompt.canPickMany && ids.length !== 1)) return undefined;
    return { id: prompt.id, reply: { cancelled: false, ids } };
  }
  if (prompt.kind === "inputBox" && "value" in answer) {
    const value = answer.value.replace(/[\r\n]/g, "");
    if (utf8Length(value) > 4096) return undefined;
    return { id: prompt.id, reply: { cancelled: false, value } };
  }
  return undefined;
}

/** Filters quick pick items by a case-insensitive substring of label, description or detail. */
export function filterQuickPickItems(items: readonly PluginQuickPickItem[], query: string) {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...items];
  return items.filter((item) => [item.label, item.description, item.detail].some((field) => field?.toLowerCase().includes(needle)));
}
