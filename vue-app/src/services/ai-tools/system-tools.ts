import type { AiCapability } from "@/domain/ai/capability-registry";
import { calcSubnet, ipInCidr } from "@/domain/network/subnet";
import { requiredString, sanitizeAiResult, tool, type AiToolDependencies } from "./shared";

function port(value: unknown, capability: string) {
  if (!Number.isInteger(value) || (value as number) < 1 || (value as number) > 65_535) {
    throw new Error(`${capability} requires a valid port`);
  }
  return value as number;
}

function ports(value: unknown) {
  if (!Array.isArray(value) || value.length < 1 || value.length > 101) {
    throw new Error("system.list_port_processes requires 1 to 101 ports");
  }
  return [...new Set(value.map((item) => port(item, "system.list_port_processes")))];
}

function pid(value: unknown) {
  if (!Number.isSafeInteger(value) || (value as number) < 1 || (value as number) > 4_294_967_295) {
    throw new Error("system.kill_process requires a valid PID");
  }
  return value as number;
}

function networkResponse(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid network response");
  const response = value as Record<string, unknown>;
  if (typeof response.status !== "number" || typeof response.body !== "string") {
    throw new Error("Invalid network response");
  }
  if (response.status < 200 || response.status >= 300) throw new Error(`HTTP ${response.status}`);
  return sanitizeAiResult(JSON.parse(response.body));
}

export function systemCapabilities(dependencies: AiToolDependencies): AiCapability[] {
  return [
    {
      module: "system",
      target: "none",
      discovery: true,
      tool: tool("system.get_platform", "Read the local operating system platform name."),
      mode: "read",
      risk: "read",
      execute: () => dependencies.invoke("get_platform_info", {}),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.check_tcp_port", "Check TCP connectivity to a host and port.", {
        host: { type: "string" },
        port: { type: "integer", minimum: 1, maximum: 65_535 },
      }, ["host", "port"]),
      mode: "read",
      risk: "network",
      approvalPreview: ({ input }) =>
        `${requiredString(input, "host", "system.check_tcp_port")}:${port(input.port, "system.check_tcp_port")}`,
      execute: ({ input }) => dependencies.invoke("check_tcp_port", {
        host: requiredString(input, "host", "system.check_tcp_port"),
        port: port(input.port, "system.check_tcp_port"),
      }),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.dns_lookup", "Query public DNS-over-HTTPS for one hostname and record type.", {
        host: { type: "string" },
        recordType: { type: "string", enum: ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SOA"] },
      }, ["host", "recordType"]),
      mode: "read",
      risk: "network",
      approvalPreview: ({ input }) =>
        `DNS ${requiredString(input, "recordType", "system.dns_lookup")} ${requiredString(input, "host", "system.dns_lookup")}`,
      execute: async ({ input }) => networkResponse(await dependencies.invoke("http_send_request", {
        payload: {
          method: "GET",
          url: `https://cloudflare-dns.com/dns-query?name=${encodeURIComponent(requiredString(input, "host", "system.dns_lookup"))}&type=${encodeURIComponent(requiredString(input, "recordType", "system.dns_lookup"))}`,
          headers: [["Accept", "application/dns-json"]],
          timeoutMs: 10_000,
        },
      })),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.ip_lookup", "Query public IP metadata for an optional IP address.", {
        ip: { type: "string" },
      }),
      mode: "read",
      risk: "network",
      approvalPreview: ({ input }) => `IP lookup ${typeof input.ip === "string" && input.ip.trim() ? input.ip.trim() : "current public IP"}`,
      execute: async ({ input }) => {
        const ip = typeof input.ip === "string" ? input.ip.trim() : "";
        return networkResponse(await dependencies.invoke("http_send_request", {
          payload: {
            method: "GET",
            url: ip ? `https://ipapi.co/${encodeURIComponent(ip)}/json/` : "https://ipapi.co/json/",
            headers: [],
            timeoutMs: 10_000,
          },
        }));
      },
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.calculate_subnet", "Calculate IPv4 subnet details locally without network access.", {
        cidr: { type: "string" },
        checkIp: { type: "string" },
      }, ["cidr"]),
      mode: "read",
      risk: "read",
      execute: ({ input }) => {
        const cidr = requiredString(input, "cidr", "system.calculate_subnet");
        const info = calcSubnet(cidr);
        const checkIp = typeof input.checkIp === "string" ? input.checkIp.trim() : "";
        return { ...info, ...(checkIp ? { checkIp, contains: ipInCidr(checkIp, cidr) } : {}) };
      },
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.list_port_processes", "List local processes bound to an explicit bounded set of ports.", {
        ports: {
          type: "array",
          minItems: 1,
          maxItems: 101,
          uniqueItems: true,
          items: { type: "integer", minimum: 1, maximum: 65_535 },
        },
      }, ["ports"]),
      mode: "read",
      risk: "read",
      execute: ({ input }) => dependencies.invoke("list_port_processes", { ports: ports(input.ports) }),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.kill_process", "Force-terminate one local process by exact PID.", {
        pid: { type: "integer", minimum: 1, maximum: 4_294_967_295 },
      }, ["pid"]),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) => `Force-terminate process PID ${pid(input.pid)}`,
      execute: async ({ input }) => ({
        pid: pid(input.pid),
        killed: await dependencies.invoke("kill_process_by_pid", { pid: pid(input.pid) }),
      }),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.list_cli_tools", "List the developer and AI command-line tools this app probes for, with the version of each installed one.", {
        force: { type: "boolean", description: "Re-probe instead of using the cached result." },
      }),
      mode: "read",
      risk: "read",
      execute: async ({ input }) => sanitizeAiResult(await dependencies.invoke("detect_cli_tools", { force: input.force === true })),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.check_for_update", "Check whether a newer version of this app is published."),
      mode: "read",
      risk: "network",
      approvalPreview: () => "Check GitHub Releases for a newer app version",
      execute: async () => sanitizeAiResult(await dependencies.invoke("check_update", {})),
    },
    {
      module: "system",
      target: "none",
      /**
       * 剪贴板是全局的，里面常年躺着刚复制的密码或 token。
       * 读一次就等于把它交给模型，所以按危险操作走审批。
       */
      tool: tool("system.read_clipboard", "Read the current text on the system clipboard. It may hold something the user copied for another purpose."),
      mode: "read",
      risk: "dangerous",
      approvalPreview: () => "Read the current system clipboard text",
      execute: async () => sanitizeAiResult(await dependencies.invoke("clipboard_read_text", {})),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.write_clipboard", "Replace the text on the system clipboard. Whatever the user had copied is lost.", {
        text: { type: "string" },
      }, ["text"]),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) => {
        const text = typeof input.text === "string" ? input.text : "";
        return `Replace the system clipboard with ${text.length} character(s)`;
      },
      execute: ({ input }) => dependencies.invoke("clipboard_write_text", {
        text: typeof input.text === "string" ? input.text : "",
      }),
    },
    {
      module: "system",
      target: "none",
      tool: tool("system.capture_screenshot", "Open the operating system's interactive screenshot picker and put the captured image on the clipboard. The user chooses the region."),
      mode: "write",
      risk: "dangerous",
      approvalPreview: () => "Open the system screenshot picker and copy the result to the clipboard",
      execute: () => dependencies.invoke("capture_screenshot", {}),
    },
  ];
}
