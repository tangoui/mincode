/**
 * The steps of a debug launch shown in the debug panel while it starts
 * (services/debug-launch.ts reports them): checking the debugger and its
 * tools, starting the language service the debugger runs in (only debuggers
 * hosted by a language server), resolving the launch, launching. While the
 * language service starts, its own status text (e.g. import or download
 * progress) is shown verbatim, bounded and without control characters.
 */
import { unsafeCodePoint } from "@/domain/debug/address-breakpoints";

export type DebugLaunchStep = "checking" | "languageService" | "resolving" | "launching";

/**
 * One progress report: a new step, the language service's latest status
 * text, or a confirmation dialog opening (true) or closing (false); while
 * one is open the launch waits for the user, not for the language service.
 */
export interface DebugLaunchProgress {
  step?: DebugLaunchStep;
  /** Whether the launch has the language service step, known once the guard found the debugger. */
  languageService?: boolean;
  status?: string;
  confirming?: boolean;
}

/** Without any progress this long while the language service starts, the panel says it may be downloading. */
export const DEBUG_LAUNCH_STALL_MS = 30_000;

/** The language service's status texts are shown at most this often (the last one always). */
export const LAUNCH_STATUS_INTERVAL_MS = 100;

/** Characters of a shown status text. */
export const LAUNCH_STATUS_MAX_CHARS = 200;

/** The steps a launch shows, in order; the language service step only for debuggers it hosts. */
export function debugLaunchSteps(languageService: boolean): DebugLaunchStep[] {
  return languageService ? ["checking", "languageService", "resolving", "launching"] : ["checking", "resolving", "launching"];
}

/**
 * Status text a language server sent, safe to show: control, bidi and
 * zero-width characters become spaces, whitespace collapses, at most
 * LAUNCH_STATUS_MAX_CHARS characters (with an ellipsis). "" when nothing is left.
 */
export function launchStatusText(raw: unknown): string {
  if (typeof raw !== "string") return "";
  let text = "";
  for (const char of raw) {
    const code = char.codePointAt(0)!;
    text += unsafeCodePoint(code) || (code >= 0xd800 && code <= 0xdfff) ? " " : char;
  }
  const chars = [...text.replace(/\s+/g, " ").trim()];
  return chars.length > LAUNCH_STATUS_MAX_CHARS ? `${chars.slice(0, LAUNCH_STATUS_MAX_CHARS - 1).join("")}…` : chars.join("");
}

/** The `type` and shown `message` of a `language/status` notification's params; null when malformed. */
export function languageStatusParams(params: unknown): { type: string; message: string } | null {
  if (typeof params !== "object" || params === null) return null;
  const { type, message } = params as Record<string, unknown>;
  if (typeof type !== "string") return null;
  return { type, message: launchStatusText(message) };
}

/**
 * Passes values on to emit at most every `ms`: the first at once, later ones
 * after the interval, always the latest (so the last one is never lost).
 * `flush` passes a pending value on now; `cancel` drops it.
 */
export function throttleLatest<T>(emit: (value: T) => void, ms: number) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending: { value: T } | undefined;
  let last = Number.NEGATIVE_INFINITY;
  function fire() {
    timer = undefined;
    if (!pending) return;
    const { value } = pending;
    pending = undefined;
    last = Date.now();
    emit(value);
  }
  return {
    push(value: T) {
      pending = { value };
      if (timer !== undefined) return;
      const wait = last + ms - Date.now();
      if (wait <= 0) fire();
      else timer = setTimeout(fire, wait);
    },
    flush() {
      clearTimeout(timer);
      fire();
    },
    cancel() {
      clearTimeout(timer);
      timer = undefined;
      pending = undefined;
    },
  };
}
