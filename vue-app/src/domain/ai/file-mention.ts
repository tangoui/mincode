export interface AiFileMention {
  /** Text typed after "@", used to search project files. */
  query: string;
  /** Index of the "@" in the prompt. */
  start: number;
}

/**
 * Finds the "@query" the user is typing at the end of the prompt. The "@" must
 * start the prompt or follow whitespace, so e-mail addresses and "a@b" stay text.
 */
export function parseAiFileMention(input: string): AiFileMention | null {
  const match = /(^|\s)@([^\s@]*)$/.exec(input);
  if (!match) return null;
  return { query: match[2] ?? "", start: match.index + (match[1]?.length ?? 0) };
}

/** Replaces the "@query" being typed with the chosen project-relative path. */
export function applyAiFileMention(input: string, mention: AiFileMention, path: string): string {
  const reference = /\s/.test(path) ? `@"${path}"` : `@${path}`;
  return `${input.slice(0, mention.start)}${reference} `;
}
