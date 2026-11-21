import type { Terminal } from "@xterm/xterm";
import { createTerminalTextContext } from "@/domain/terminal/context-menu";

export function resolveTerminalPointerContext(
  terminal: Terminal | null,
  host: HTMLElement | undefined,
  clientX: number,
  clientY: number
) {
  if (!terminal || !host) return createTerminalTextContext("");
  const selection = terminal.getSelection();
  if (selection) return createTerminalTextContext(selection);

  const rect = host.getBoundingClientRect();
  const relativeX = Math.max(0, Math.min(clientX - rect.left, rect.width));
  const relativeY = Math.max(0, Math.min(clientY - rect.top, rect.height));
  const row = Math.min(terminal.rows - 1, Math.floor(relativeY / Math.max(1, rect.height / terminal.rows)));
  const column = Math.min(
    terminal.cols - 1,
    Math.floor(relativeX / Math.max(1, rect.width / terminal.cols))
  );
  const line = terminal.buffer.active
    .getLine(terminal.buffer.active.viewportY + row)
    ?.translateToString(true) ?? "";
  return createTerminalTextContext("", line, column);
}
