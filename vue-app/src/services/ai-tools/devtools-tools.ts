import type { AiCapability } from "@/domain/ai/capability-registry";
import { decodeBase64, encodeBase64 } from "@/domain/security/crypto";
import { requiredString, tool } from "./shared";

const operations = ["base64.encode", "base64.decode", "json.format", "json.minify", "url.encode", "url.decode"] as const;
type DevtoolsOperation = typeof operations[number];

function operation(input: Record<string, unknown>) {
  const value = requiredString(input, "operation", "devtools.transform");
  if (!operations.includes(value as DevtoolsOperation)) throw new Error("devtools.transform has an unsupported operation");
  return value as DevtoolsOperation;
}

export function devtoolsCapabilities(): AiCapability[] {
  return [{
    module: "devtools",
    target: "none",
    discovery: true,
    tool: tool("devtools.transform", "Perform an allowlisted local text or JSON transformation without filesystem or network access.", {
      operation: { type: "string", enum: operations },
      value: { type: "string" },
    }, ["operation", "value"]),
    mode: "read",
    risk: "read",
    execute: ({ input }) => {
      const value = requiredString(input, "value", "devtools.transform");
      switch (operation(input)) {
        case "base64.encode": return encodeBase64(value);
        case "base64.decode": return decodeBase64(value);
        case "json.format": {
          try { return JSON.stringify(JSON.parse(value), null, 2); } catch { throw new Error("Invalid JSON"); }
        }
        case "json.minify": {
          try { return JSON.stringify(JSON.parse(value)); } catch { throw new Error("Invalid JSON"); }
        }
        case "url.encode": return encodeURIComponent(value);
        case "url.decode": return decodeURIComponent(value);
      }
    },
  }];
}
