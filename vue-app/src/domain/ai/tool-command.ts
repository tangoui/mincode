export type AiPromptSuggestionKind = "tool";

export interface AiPromptSuggestion {
  id: string;
  name: string;
  description: string;
  kind: AiPromptSuggestionKind;
  insertText: string;
}

export interface AiPromptSuggestionTool {
  name: string;
  description: string;
}


interface AiPromptSuggestionQuery {
  kind?: AiPromptSuggestionKind;
  query: string;
}

export function toolSuggestionText(toolName: string): string {
  return `/${toolName} `;
}

export function parseAiPromptSuggestionQuery(input: string): AiPromptSuggestionQuery | null {
  const trimmed = input.trimStart();
  if (!trimmed.startsWith("/") || trimmed.includes("{")) return null;

  const body = trimmed.slice(1).trim();
  const typedCommand = /^(tool)(?:\s+(.*))?$/i.exec(body);
  if (typedCommand) {
    return {
      kind: typedCommand[1]!.toLowerCase() as AiPromptSuggestionKind,
      query: (typedCommand[2] ?? "").trim().toLowerCase(),
    };
  }

  return { query: body.toLowerCase() };
}

export function buildAiPromptSuggestions(
  input: string,
  tools: AiPromptSuggestionTool[],
): AiPromptSuggestion[] {
  const parsed = parseAiPromptSuggestionQuery(input);
  if (!parsed) return [];

  const suggestions: AiPromptSuggestion[] = [
    ...tools.map((tool) => ({
      id: `tool:${tool.name}`,
      name: tool.name,
      description: tool.description,
      kind: "tool" as const,
      insertText: toolSuggestionText(tool.name),
    })),
  ];

  return suggestions.filter((suggestion) => {
    if (parsed.kind && suggestion.kind !== parsed.kind) return false;
    if (!parsed.query) return true;
    return [suggestion.name, suggestion.description].some((value) =>
      value.toLowerCase().includes(parsed.query)
    );
  });
}
