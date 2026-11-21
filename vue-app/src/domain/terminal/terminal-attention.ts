import { safeTerminalTaskLabel, type TerminalActivityPhase } from "@/domain/terminal/terminal-activity";

/**
 * Why a terminal wants the user back. The backend reads bell/notify/quiet/exited
 * straight from PTY output (so background tabs work too); the others come from
 * tool integrations (Claude hooks) or a classified notification text.
 */
export type TerminalAttentionReason =
  | "failed"
  | "waiting_approval"
  | "waiting_input"
  | "bell"
  | "notify"
  | "completed"
  | "exited"
  | "quiet";

export interface TerminalAttention {
  paneId: string;
  reason: TerminalAttentionReason;
  message?: string;
  at: number;
}

export const TERMINAL_ATTENTION_EVENT = "terminal-attention";

const backendKinds = new Set(["bell", "notify", "quiet", "exited"]);
const paneIdPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;

// Higher wins when a pane already has an unseen attention.
const priority: Record<TerminalAttentionReason, number> = {
  failed: 7,
  waiting_approval: 6,
  waiting_input: 5,
  bell: 4,
  notify: 4,
  completed: 3,
  exited: 2,
  quiet: 1,
};

/**
 * Tools word their desktop notifications differently (codex: "Approval requested: …",
 * "Agent turn complete"); recognise the common ones so the tab says what happened.
 */
export function classifyNotification(message: string): TerminalAttentionReason {
  if (/approv|permission|授权|批准|允许/i.test(message)) return "waiting_approval";
  if (/question|waiting for (?:your )?input|needs? (?:your )?input|plan|回答|等待输入/i.test(message)) return "waiting_input";
  if (/complete|finished|\bdone\b|完成|结束/i.test(message)) return "completed";
  if (/fail|error|失败|出错|错误/i.test(message)) return "failed";
  return "notify";
}

/** Validates a backend `terminal-attention` event. */
export function parseTerminalAttentionEvent(payload: unknown): TerminalAttention | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const { paneId, kind, message, at } = payload as Record<string, unknown>;
  if (typeof paneId !== "string" || !paneIdPattern.test(paneId)) return undefined;
  if (typeof kind !== "string" || !backendKinds.has(kind)) return undefined;
  const text = typeof message === "string" ? safeTerminalTaskLabel(message, 120) : "";
  const reason = kind === "notify" && text ? classifyNotification(text) : (kind as TerminalAttentionReason);
  return {
    paneId,
    reason,
    ...(text ? { message: text } : {}),
    at: typeof at === "number" && Number.isFinite(at) ? at : Date.now(),
  };
}

const activityReasons: Partial<Record<TerminalActivityPhase, TerminalAttentionReason>> = {
  completed: "completed",
  failed: "failed",
  waiting_approval: "waiting_approval",
  waiting_input: "waiting_input",
};

/**
 * Maps a precise tool status (Claude hooks) to an attention; busy phases map to
 * nothing, and so does a session that has only just started (it waits for its
 * first prompt, nothing finished).
 */
export function attentionFromActivity(snapshot: unknown): TerminalAttention | undefined {
  if (!snapshot || typeof snapshot !== "object") return undefined;
  const { paneId, phase, summary, updatedAt, event } = snapshot as Record<string, unknown>;
  if (typeof paneId !== "string" || !paneIdPattern.test(paneId) || typeof phase !== "string") return undefined;
  if (event === "SessionStart") return undefined;
  const reason = activityReasons[phase as TerminalActivityPhase];
  if (!reason) return undefined;
  const text = typeof summary === "string" ? safeTerminalTaskLabel(summary, 120) : "";
  return {
    paneId,
    reason,
    ...(text ? { message: text } : {}),
    at: typeof updatedAt === "number" && Number.isFinite(updatedAt) ? updatedAt : Date.now(),
  };
}

// What xterm.js sends to the program on the user's behalf: focus in/out, mouse
// reports, cursor-position / device-attribute / mode replies, OSC and DCS replies.
const terminalReportPattern = new RegExp(
  "^(?:\\x1b\\[[IO]|\\x1b\\[M[\\s\\S]{3}|\\x1b\\[<\\d+;\\d+;\\d+[Mm]|\\x1b\\[\\d+;\\d+R|\\x1b\\[[?>]?[\\d;]*c"
    + "|\\x1b\\[\\?[\\d;]+\\$y|\\x1b\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|\\x1bP[^\\x1b]*\\x1b\\\\)+$",
);

/**
 * True for terminal reports rather than keystrokes. Apps such as Claude Code turn
 * on focus reporting, so returning to the window sends "ESC [ I" through onData;
 * that must not count as the user acknowledging the pane.
 */
export function isTerminalReport(input: string) {
  return input !== "" && terminalReportPattern.test(input);
}

export type AttentionItems = ReadonlyMap<string, TerminalAttention>;

/**
 * Records an attention unless the user is looking at that pane. Returns the same
 * map when nothing changes so callers can skip reactive updates, and whether the
 * pane newly needs the user (or for a more important reason) — the only case that
 * deserves a system notification.
 */
export function raiseAttention(items: AttentionItems, entry: TerminalAttention, seen: boolean) {
  if (seen) return { items, raised: false };
  const current = items.get(entry.paneId);
  if (current && priority[entry.reason] < priority[current.reason]) return { items, raised: false };
  const next = new Map(items);
  next.set(entry.paneId, entry);
  return { items: next as AttentionItems, raised: !current || priority[entry.reason] > priority[current.reason] };
}

/** Removes the given panes; returns the same map when none of them had an attention. */
export function clearAttention(items: AttentionItems, paneIds: Iterable<string>): AttentionItems {
  let next: Map<string, TerminalAttention> | undefined;
  for (const paneId of paneIds) {
    if (!(next ?? items).has(paneId)) continue;
    next ??= new Map(items);
    next.delete(paneId);
  }
  return next ?? items;
}

/** Keeps only panes that still belong to a tab. */
export function retainAttention(items: AttentionItems, alive: (paneId: string) => boolean): AttentionItems {
  const gone = [...items.keys()].filter((paneId) => !alive(paneId));
  return gone.length ? clearAttention(items, gone) : items;
}

/** The most important attention among some panes (a tab's panes). */
export function strongestAttention(items: AttentionItems, paneIds: Iterable<string>) {
  let best: TerminalAttention | undefined;
  for (const paneId of paneIds) {
    const item = items.get(paneId);
    if (item && (!best || priority[item.reason] > priority[best.reason] || (priority[item.reason] === priority[best.reason] && item.at > best.at))) {
      best = item;
    }
  }
  return best;
}

export function attentionReasonKey(reason: TerminalAttentionReason) {
  return `terminal.attention.reason.${reason}` as const;
}

/** Two- or three-word label for the pane toolbar chip. */
export function attentionShortKey(reason: TerminalAttentionReason) {
  return `terminal.attention.short.${reason}` as const;
}

const CODEX_NOTIFICATION_FLAGS = "-c tui.notifications=true -c tui.notification_method=osc9 -c tui.notification_condition=always";

/**
 * Codex only announces "turn complete" / "approval requested" when asked to; have it
 * send them as OSC 9 so the terminal attention scanner sees them. Applies to commands
 * that start with `codex` and do not already set the notification method.
 */
export function withCodexNotifications(command: string) {
  const match = /^(\s*codex)(?=\s|$)/.exec(command);
  if (!match || /tui\.notification_method/.test(command)) return command;
  return `${match[1]} ${CODEX_NOTIFICATION_FLAGS}${command.slice(match[1].length)}`;
}
