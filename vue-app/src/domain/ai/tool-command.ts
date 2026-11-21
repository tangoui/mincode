export type AiPromptSuggestionKind = "tool" | "file" | "prompt" | "context";

export interface AiPromptSuggestion {
  id: string;
  name: string;
  description: string;
  kind: AiPromptSuggestionKind;
  insertText: string;
  /** Display name of the plugin that contributed this suggestion (plugin prompts). */
  source?: string;
  /** Plugin prompt key (`<pluginId>.<id>`); selecting it expands the template into the draft. */
  promptKey?: string;
  /** Plugin context provider key (`<pluginId>/<id>`); selecting it opens the preview dialog, never sends. */
  contextKey?: string;
}

export interface AiPromptSuggestionTool {
  name: string;
  description: string;
}

/** A plugin prompt template offered in the composer; it only ever expands into the draft. */
export interface AiPromptSuggestionPrompt {
  key: string;
  name: string;
  description: string;
  source: string;
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
  const typedCommand = /^(tool|prompt)(?:\s+(.*))?$/i.exec(body);
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
  prompts: AiPromptSuggestionPrompt[] = []
): AiPromptSuggestion[] {
  const parsed = parseAiPromptSuggestionQuery(input);
  if (!parsed) return [];

  const suggestions: AiPromptSuggestion[] = [
    ...prompts.map((prompt) => ({
      id: `prompt:${prompt.key}`,
      name: prompt.name,
      description: prompt.description,
      kind: "prompt" as const,
      insertText: "",
      source: prompt.source,
      promptKey: prompt.key,
    })),
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
    return [suggestion.name, suggestion.description, suggestion.source ?? ""].some((value) =>
      value.toLowerCase().includes(parsed.query)
    );
  });
}
