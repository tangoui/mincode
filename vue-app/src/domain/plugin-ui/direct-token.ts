/** Direct binary channel ticket for one view session (backend plugin/ui_direct.go). */
export interface PluginUIDirectToken {
  token: string;
  url: string;
}

const DIRECT_PATH = "/__mincode/direct";

/**
 * Accepts only a ws:// URL on the loopback asset server's direct path that carries exactly the
 * returned token, so a confused or compromised backend answer cannot point a view elsewhere.
 */
export function parsePluginUIDirectToken(value: unknown): PluginUIDirectToken {
  const raw = value && typeof value === "object" ? value as Record<string, unknown> : undefined;
  const token = typeof raw?.token === "string" ? raw.token : "";
  const rawURL = typeof raw?.url === "string" ? raw.url : "";
  let parsed: URL | undefined;
  try {
    parsed = new URL(rawURL);
  } catch {
    parsed = undefined;
  }
  if (!/^[0-9a-f]{64}$/.test(token) || !parsed || parsed.protocol !== "ws:" || parsed.hostname !== "127.0.0.1"
    || parsed.pathname !== DIRECT_PATH || parsed.search !== `?token=${token}` || parsed.hash) {
    throw new Error("Invalid direct channel token");
  }
  return { token, url: rawURL };
}
