// Loads src/domain/ai/settings-integrations.ts for Node tests. The module
// imports the desktop bridge and the SkillHub link helper, which cannot run under
// Node, so those imports are replaced with inert stubs before transpiling.
import { readFile } from "node:fs/promises";
import ts from "typescript";

const integrationSource = await readFile(
  new URL("../src/domain/ai/settings-integrations.ts", import.meta.url),
  "utf8",
);
// The MCP helpers live in their own module (kept out of the entry chunk);
// inline them, without their export keywords, in place of the import and re-export.
const mcpServersSource = (await readFile(
  new URL("../src/domain/ai/mcp-servers.ts", import.meta.url),
  "utf8",
)).replace(/^export /gm, "");
const compiledIntegration = ts.transpileModule(
  integrationSource
    .replace(/import \{[^}]*\} from "@\/domain\/ai\/mcp-servers";/, mcpServersSource)
    .replace(/export \{[^}]*\} from "@\/domain\/ai\/mcp-servers";/, "export { findMcpPathPlaceholder, normalizeMcpServerForRuntime };")
    .replace(
      'import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";',
      'const invokeBackend = async () => undefined; const isDesktopRuntime = () => false;',
    )
    .replace(
      /import \{[\s\S]*?\} from "@\/domain\/ai\/skillhub-repository";/,
      "const normalizeTencentSkillHubDocsUrl = (value) => value;",
    ),
  { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } },
).outputText;

export const settingsIntegrations = await import(
  `data:text/javascript;base64,${Buffer.from(compiledIntegration).toString("base64")}`
);
