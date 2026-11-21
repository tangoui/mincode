import type { DatabaseConnection, DatabaseTunnelConfig } from "@/domain/database/types";

/** Folded sections of the connection form. */
export type ConnectionFormSection = "tunnel" | "extra";

const TUNNEL_TYPE_LABELS: Record<DatabaseTunnelConfig["type"], string> = {
  ssh: "SSH",
  socks5: "SOCKS5",
  http_connect: "HTTP CONNECT",
};

/** Tunnel summary: null when no tunnel is used; otherwise the type label and host. */
export function tunnelSummary(
  tunnel: DatabaseTunnelConfig | undefined,
): { type: string; host: string } | null {
  if (!tunnel) return null;
  // Imported or old data may not match the type; never let a summary break the form.
  const host = typeof tunnel.host === "string" ? tunnel.host.trim() : "";
  return { type: TUNNEL_TYPE_LABELS[tunnel.type] ?? String(tunnel.type ?? ""), host };
}

/** Additional-info summary: group then tag names, blanks dropped; the description only sets a flag. */
export function extraSummary(
  draft: Pick<DatabaseConnection, "group" | "tags" | "description">,
): { items: string[]; hasDescription: boolean } {
  const items = [draft.group, ...(draft.tags ?? [])]
    .map((value) => value?.trim() ?? "")
    .filter((value) => value.length > 0);
  return { items, hasDescription: Boolean(draft.description?.trim()) };
}

/** Sections open by default when editing: those that already hold content. */
export function initialOpenSections(
  draft: Pick<DatabaseConnection, "tunnel" | "group" | "tags" | "description">,
): ConnectionFormSection[] {
  const sections: ConnectionFormSection[] = [];
  if (draft.tunnel) sections.push("tunnel");
  const extra = extraSummary(draft);
  if (extra.items.length > 0 || extra.hasDescription) sections.push("extra");
  return sections;
}
