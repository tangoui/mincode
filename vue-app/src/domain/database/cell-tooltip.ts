const TOOLTIP_MINIMUM_LENGTH = 32;
export const DATABASE_CELL_TOOLTIP_MAX_LENGTH = 2400;

function safelyFormatCell(value: unknown, formatCell: (value: unknown) => string) {
  try {
    return formatCell(value);
  } catch {
    return String(value);
  }
}

function prettifyJson(value: unknown): string | undefined {
  try {
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return undefined;
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    }

    if (typeof value === "object" && value !== null) {
      return JSON.stringify(value, null, 2);
    }
  } catch {
    // Database values are untrusted; retain their display formatting when JSON parsing fails.
  }

  return undefined;
}

function truncateTooltipContent(content: string) {
  const characters = Array.from(content);
  if (characters.length <= DATABASE_CELL_TOOLTIP_MAX_LENGTH) return content;
  return `${characters.slice(0, DATABASE_CELL_TOOLTIP_MAX_LENGTH - 1).join("")}…`;
}

/**
 * Produces the readable, bounded detail shown when a result-grid cell is hovered.
 * JSON values are formatted only for the tooltip, leaving the compact grid cell unchanged.
 */
export function formatDatabaseCellTooltip(
  value: unknown,
  formatCell: (value: unknown) => string
): string | undefined {
  if (value == null) return undefined;

  const formatted = safelyFormatCell(value, formatCell);
  const prettyJson = prettifyJson(value);
  const content = prettyJson ?? formatted;

  if (!content || (content.length < TOOLTIP_MINIMUM_LENGTH && !content.includes("\n"))) {
    return undefined;
  }

  return truncateTooltipContent(content);
}
