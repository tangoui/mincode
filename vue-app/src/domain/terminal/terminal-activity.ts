export type TerminalActivityPhase =
  | "connecting"
  | "active"
  | "quiet"
  | "waiting_approval"
  | "waiting_input"
  | "completed"
  | "failed";

export type TerminalActivitySource = "generic" | "claude-hook";

export interface TerminalPaneActivity {
  phase: TerminalActivityPhase;
  source: TerminalActivitySource;
  summary?: string;
  startedAt: number;
  updatedAt: number;
  lastOutputAt?: number;
  endedAt?: number;
}

export interface TerminalTabActivity extends TerminalPaneActivity {
  paneCount: number;
}

const activePhases = new Set<TerminalActivityPhase>(["connecting", "active"]);
const escapeCharacter = String.fromCharCode(27);
const controlStringIntroducer = String.fromCharCode(155);
const bellCharacter = String.fromCharCode(7);
const ansiPattern = new RegExp(
  `[${escapeCharacter}${controlStringIntroducer}](?:\\[[0-?]*[ -/]*[@-~]|\\][^${bellCharacter}]*(?:${bellCharacter}|${escapeCharacter}\\\\)?)`,
  "g",
);

function removeControlCharacters(value: string) {
  return Array.from(value, (character) => {
    const code = character.codePointAt(0) ?? 0;
    return code <= 31 || (code >= 127 && code <= 159) ? " " : character;
  }).join("");
}

export function safeTerminalTaskLabel(value: string, maxLength = 80) {
  const normalized = removeControlCharacters(value.replace(ansiPattern, ""))
    .replace(/\s+/g, " ")
    .trim()
    .replace(/(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(bearer\s+)([a-z0-9._~+/=-]{12,})/gi, "$1[REDACTED]")
    .replace(/((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]")
    .replace(/(password\s*[=:]\s*)([^\s,;"'}]+)/gi, "$1[REDACTED]");
  if (!normalized) return "";
  const sentence = normalized.match(/^.*?(?:[.!?。！？](?:\s|$)|$)/)?.[0]?.trim() || normalized;
  const characters = Array.from(sentence);
  return characters.length <= maxLength
    ? sentence
    : `${characters.slice(0, Math.max(0, maxLength - 1)).join("")}…`;
}

function latestActivity(activities: TerminalPaneActivity[]) {
  return activities.reduce((latest, activity) =>
    activity.updatedAt >= latest.updatedAt ? activity : latest
  );
}

function activityWithPhase(
  activities: TerminalPaneActivity[],
  phase: TerminalActivityPhase,
) {
  return activities
    .filter((activity) => activity.phase === phase)
    .sort((left, right) => right.updatedAt - left.updatedAt)[0];
}

/**
 * Claude 的 hook 只在事件发生的那一刻推送一次。一次 turn 结束时若 Stop 没送达
 * （会话是 resume 起来的、钩子没装上、进程被强杀），"active" 会永远停在那里转圈。
 * 超过这个窗口再没有任何新信号（hook 事件或终端输出），就不再声称正在执行。
 */
export const staleActivityMs = 45_000;

function decayStaleActivity(activity: TerminalPaneActivity, now: number): TerminalPaneActivity {
  if (!activePhases.has(activity.phase)) return activity;
  const lastSignalAt = Math.max(activity.updatedAt, activity.lastOutputAt || 0);
  if (now - lastSignalAt <= staleActivityMs) return activity;
  // 状态已经过期，来源也不再算“精确”，否则提示里会同时出现静默和 Claude 精确状态。
  return { ...activity, phase: "quiet", source: "generic" };
}

export function aggregateTerminalActivity(
  activities: Record<string, TerminalPaneActivity>,
  createdAt: number,
  now = Date.now(),
): TerminalTabActivity {
  const values = Object.values(activities).map((activity) => decayStaleActivity(activity, now));
  if (!values.length) {
    return {
      // 没有任何面板汇报过状态时同样不能一直转圈。
      phase: now - createdAt > staleActivityMs ? "quiet" : "connecting",
      source: "generic",
      startedAt: createdAt,
      updatedAt: createdAt,
      paneCount: 0,
    };
  }

  const latest = latestActivity(values);
  const failed = activityWithPhase(values, "failed");
  const waitingApproval = activityWithPhase(values, "waiting_approval");
  const active = values
    .filter((activity) => activePhases.has(activity.phase))
    .sort((left, right) => right.updatedAt - left.updatedAt)[0];
  const waitingInput = activityWithPhase(values, "waiting_input");
  const quiet = activityWithPhase(values, "quiet");
  const allCompleted = values.every((activity) => activity.phase === "completed");
  const representative = failed || waitingApproval || active || waitingInput || quiet || latest;
  const phase = failed
    ? "failed"
    : waitingApproval
      ? "waiting_approval"
      : active
        ? active.phase
        : waitingInput
          ? "waiting_input"
          : allCompleted
            ? "completed"
            : "quiet";

  return {
    phase,
    source: representative.source,
    summary: representative.summary,
    startedAt: Math.min(createdAt, ...values.map((activity) => activity.startedAt)),
    updatedAt: Math.max(...values.map((activity) => activity.updatedAt)),
    lastOutputAt: Math.max(0, ...values.map((activity) => activity.lastOutputAt || 0)) || undefined,
    endedAt: phase === "completed" || phase === "failed"
      ? Math.max(...values.map((activity) => activity.endedAt || activity.updatedAt))
      : undefined,
    paneCount: values.length,
  };
}

export function terminalActivityDurationMs(activity: TerminalPaneActivity, now = Date.now()) {
  return Math.max(0, (activity.endedAt || now) - activity.startedAt);
}

export function isTerminalActivityActive(phase: TerminalActivityPhase) {
  return activePhases.has(phase);
}

export function shouldApplyTerminalActivity(
  current: TerminalPaneActivity | undefined,
  incoming: TerminalPaneActivity,
) {
  if (!current) return true;
  if (current.source === "generic" && incoming.source === "claude-hook") return true;
  if (incoming.updatedAt < current.updatedAt) return false;
  if (
    current.source === "claude-hook" &&
    incoming.source === "generic" &&
    incoming.phase !== "completed" &&
    incoming.phase !== "failed"
  ) {
    return false;
  }
  return true;
}
