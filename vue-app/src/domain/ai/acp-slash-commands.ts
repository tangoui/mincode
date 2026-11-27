import { toolSuggestionText, type AiPromptSuggestion } from "./tool-command";

export interface AcpSlashCommand {
  name: string;
  description: string;
  hint: string;
}

export function parseAcpSlashCommands(value: unknown): AcpSlashCommand[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  return value.flatMap((item: unknown) => {
    if (!item || typeof item !== "object") return [];
    const { name, description, input } = item as Record<string, unknown>;
    if (typeof name !== "string" || !/^[^/\s]+$/.test(name) || seen.has(name)) return [];
    seen.add(name);
    const hint = input && typeof input === "object" && "hint" in input ? input.hint : undefined;
    return [{
      name,
      description: typeof description === "string" ? description.trim() : "",
      hint: typeof hint === "string" ? hint.trim() : "",
    }];
  });
}

export function acpSlashSuggestions(input: string, commands: AcpSlashCommand[]): AiPromptSuggestion[] {
  const match = /^\s*\/([^/\s]*)$/.exec(input);
  if (!match) return [];
  const query = match[1].toLowerCase();
  return commands.filter((command) => command.name.toLowerCase().startsWith(query)).map((command) => ({
    id: "acp-command:" + command.name,
    kind: "acp-command",
    name: "/" + command.name,
    description: command.description,
    inputHint: command.hint,
    insertText: toolSuggestionText(command.name),
  }));
}

export function isAcpSlashCommand(input: string, commands: AcpSlashCommand[]) {
  const name = /^\s*\/([^/\s]+)(?=\s|$)/.exec(input)?.[1];
  return name !== undefined && commands.some((command) => command.name === name);
}
