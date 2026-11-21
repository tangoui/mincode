/**
 * Plugin terminal links (contributes.terminalLinks) for one xterm instance.
 * A link provider scans the hovered logical line (the buffer row plus up to
 * 4 wrapped rows, at most 2048 characters) with the rules from the
 * terminal-links store, within the per-line time budget; a rule that exceeds
 * the budget 3 times is disabled for this terminal. Nothing is rendered as
 * HTML: xterm only decorates cells and the hover text is a plain `title`.
 * Opening needs a click: URL targets go through the system-browser boundary
 * (normalizeWebUrl + openSystemBrowser).
 */
import type { IBufferLine, IDisposable, ILink, Terminal } from "@xterm/xterm";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import {
  TERMINAL_LINK_LINE_SCAN_MAX_CHARS,
  TERMINAL_LINK_OVERRUNS_BEFORE_DISABLE,
  TERMINAL_LINK_WRAPPED_ROWS_MAX,
  resolveTerminalLinkUrl,
  scanTerminalLine,
  terminalLinkHoverText,
  type TerminalLinkMatch,
  type TerminalLinkTerminal,
} from "@/domain/terminal/terminal-links";
import { normalizeWebUrl, type TerminalWebLinkFailure } from "@/services/terminal-web-links";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { openSystemBrowser } from "@/services/web-automation-service";
import { useTerminalLinksStore } from "@/stores/terminal-links";

export interface PluginTerminalLinksOptions {
  kind: TerminalLinkTerminal;
  /** Working directory for relative file paths (local terminals). */
  cwd?: () => string | undefined;
  /** Element whose `title` shows the plain-text hover label. */
  host: HTMLElement;
  /** Reuses the terminal's web-link failure notices. */
  onWebLinkFailure: (failure: TerminalWebLinkFailure) => void;
}

interface CellPosition {
  x: number;
  y: number;
  /** Last cell (1-based, inclusive) covered by this character. */
  endX: number;
}

interface LogicalLine {
  text: string;
  positions: CellPosition[];
}

function appendRow(line: IBufferLine, row: number, target: LogicalLine) {
  const cell = line.getCell(0);
  if (!cell) return;
  for (let x = 0; x < line.length && target.text.length < TERMINAL_LINK_LINE_SCAN_MAX_CHARS; x += 1) {
    line.getCell(x, cell);
    const width = cell.getWidth();
    if (width === 0) continue;
    const chars = cell.getChars() || " ";
    for (let index = 0; index < chars.length; index += 1) {
      target.positions.push({ x: x + 1, y: row + 1, endX: x + width });
    }
    target.text += chars;
  }
}

/** Reads the logical line around buffer row `row` (0-based): at most 4 rows and 2048 characters. */
function readLogicalLine(xterm: Terminal, row: number): LogicalLine {
  const buffer = xterm.buffer.active;
  let start = row;
  while (start > 0 && row - start < TERMINAL_LINK_WRAPPED_ROWS_MAX - 1 && buffer.getLine(start)?.isWrapped) start -= 1;
  const result: LogicalLine = { text: "", positions: [] };
  for (let current = start; current < start + TERMINAL_LINK_WRAPPED_ROWS_MAX; current += 1) {
    const line = buffer.getLine(current);
    if (!line || (current > start && !line.isWrapped)) break;
    appendRow(line, current, result);
    if (result.text.length >= TERMINAL_LINK_LINE_SCAN_MAX_CHARS) break;
  }
  return result;
}

/** Registers the plugin link provider on `xterm`; dispose it on unmount. */
export function attachPluginTerminalLinks(xterm: Terminal, options: PluginTerminalLinksOptions): IDisposable {
  const store = useTerminalLinksStore();
  const overruns = new Map<string, number>();
  const disabled = new Set<string>();
  let savedTitle: string | null = null;

  function activeRules() {
    return store.rules.filter((rule) => rule.terminals.includes(options.kind) && !disabled.has(rule.key));
  }

  function recordOverruns(keys: readonly string[]) {
    for (const key of keys) {
      const count = (overruns.get(key) ?? 0) + 1;
      overruns.set(key, count);
      if (count < TERMINAL_LINK_OVERRUNS_BEFORE_DISABLE || disabled.has(key)) continue;
      disabled.add(key);
      const rule = store.rules.find((item) => item.key === key);
      store.recordIssue(key, "disabled after exceeding the per-line time budget");
      const { t } = useVueI18n();
      ElMessage.warning({
        message: t("pluginContrib.terminalLinks.ruleDisabled", { rule: rule?.id ?? key, plugin: rule?.pluginName ?? "" }),
        grouping: true,
        showClose: true,
      });
    }
  }

  function targetLabel(match: TerminalLinkMatch): string | undefined {
    const target = match.rule.target;
    return target.type === "url" ? resolveTerminalLinkUrl(target.url, match.groups) : undefined;
  }

  function activate(match: TerminalLinkMatch) {
    const target = match.rule.target;
    if (!isDesktopRuntime()) {
      options.onWebLinkFailure("desktop-required");
      return;
    }
    if (target.type === "url") {
      const resolved = resolveTerminalLinkUrl(target.url, match.groups);
      const url = resolved ? normalizeWebUrl(resolved) : null;
      if (!url) {
        options.onWebLinkFailure("invalid-url");
        return;
      }
      void openSystemBrowser(url).catch(() => options.onWebLinkFailure("open-failed"));
      return;
    }
  }

  function clearHover() {
    if (savedTitle === null) return;
    if (savedTitle) options.host.title = savedTitle;
    else options.host.removeAttribute("title");
    savedTitle = null;
  }

  const provider = xterm.registerLinkProvider({
    provideLinks(bufferLineNumber, callback) {
      const rules = activeRules();
      if (rules.length === 0) {
        callback(undefined);
        return;
      }
      const row = bufferLineNumber - 1;
      const line = readLogicalLine(xterm, row);
      const result = scanTerminalLine(line.text, rules);
      if (result.overruns.length) recordOverruns(result.overruns);
      const links: ILink[] = [];
      for (const match of result.links) {
        const first = line.positions[match.start];
        const last = line.positions[match.end - 1];
        if (!first || !last || first.y > bufferLineNumber || last.y < bufferLineNumber) continue;
        const label = targetLabel(match);
        if (!label) continue;
        links.push({
          range: { start: { x: first.x, y: first.y }, end: { x: last.endX, y: last.y } },
          text: match.text,
          decorations: { underline: true, pointerCursor: true },
          activate: () => activate(match),
          hover: () => {
            if (savedTitle === null) savedTitle = options.host.getAttribute("title") ?? "";
            options.host.title = terminalLinkHoverText(label, match.rule.pluginName);
          },
          leave: clearHover,
        });
      }
      callback(links.length ? links : undefined);
    },
  });

  return {
    dispose() {
      clearHover();
      provider.dispose();
    },
  };
}
