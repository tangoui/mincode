import { computed, ref, type ComputedRef } from "vue";
import {
  evaluateWhenClauses,
  parseWhenExpression,
  type PluginContextValues,
  type WhenClause,
} from "@/domain/commands/when";

export type CommandSource = "builtin" | "workspace";

export interface RegisteredCommand {
  id: string;
  source: CommandSource;
  title: string | (() => string);
  subtitle?: string | (() => string);
  keywords?: readonly string[];
  priority?: number;
  /**
   * Keybinding-context expression (domain/commands/when.ts dialect) that must
   * hold for the command to be listed in the palette or run from a key.
   */
  when?: string;
  /** List the command in the palette (default true); key-only handlers set false. */
  showInPalette?: boolean;
  execute: () => void | Promise<void>;
}

export interface ResolvedRegisteredCommand extends Omit<RegisteredCommand, "title" | "subtitle"> {
  title: string;
  subtitle?: string;
}

interface RegistryEntry {
  token: symbol;
  command: RegisteredCommand;
  clauses: WhenClause[];
}

const entries = new Map<string, RegistryEntry>();
const revision = ref(0);

function resolveText(value: string | (() => string) | undefined) {
  return typeof value === "function" ? value() : value;
}

function normalizedCommand(command: RegisteredCommand): RegisteredCommand {
  const id = command.id.trim();
  if (!id) throw new Error("Command id must not be empty");
  return {
    ...command,
    id,
    keywords: command.keywords?.filter((keyword) => keyword.trim()),
  };
}

export function registerCommand(command: RegisteredCommand): () => void {
  const normalized = normalizedCommand(command);
  // Throws on an invalid expression so a typo fails at registration, not silently at runtime.
  const clauses = normalized.when?.trim() ? parseWhenExpression(normalized.when) : [];
  const token = Symbol(normalized.id);
  entries.set(normalized.id, { token, command: normalized, clauses });
  revision.value += 1;

  return () => {
    if (entries.get(normalized.id)?.token !== token) return;
    entries.delete(normalized.id);
    revision.value += 1;
  };
}

/** Whether a command is registered and its `when` holds in `context` (omitted: when is not checked). */
export function isRegisteredCommandEnabled(id: string, context?: PluginContextValues): boolean {
  const entry = entries.get(id);
  if (!entry) return false;
  return !context || evaluateWhenClauses(entry.clauses, context);
}

/**
 * Palette-visible commands. `context` supplies the keybinding context captured
 * when the palette opened; without it `when` is not evaluated.
 */
export function useRegisteredCommands(
  context?: () => PluginContextValues | undefined,
): ComputedRef<ResolvedRegisteredCommand[]> {
  return computed(() => {
    void revision.value;
    const values = context?.();
    return [...entries.values()]
      .filter(({ command, clauses }) =>
        command.showInPalette !== false && (!values || evaluateWhenClauses(clauses, values)))
      .map(({ command }) => ({
      ...command,
      get title() {
        return resolveText(command.title) ?? command.id;
      },
      get subtitle() {
        return resolveText(command.subtitle);
      },
    }));
  });
}

export async function executeRegisteredCommand(id: string): Promise<void> {
  const entry = entries.get(id);
  if (!entry) throw new Error(`Command ${id} is not registered`);
  await entry.command.execute();
}
