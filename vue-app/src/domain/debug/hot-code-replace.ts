/**
 * Hot code replace, generic over debuggers: a debugger plugin declares in its
 * manifest (`debuggers[].hotCodeReplace {event, request}`) the custom DAP
 * event its adapter sends about rebuilt code and the custom request that
 * puts the rebuilt code into the running program. The host knows no names;
 * it reads the declared ones and expects this protocol (java-debug's,
 * measured in backend/debug TestMachineSmokeJavaIdea2HotCodeReplace):
 *
 *  - The event body is `{changeType, message?}`. `BUILD_COMPLETE`: changed
 *    code was compiled and can be applied (java-debug: "Build completed.",
 *    once per save that changed class files). `WARNING` / `ERROR`: `message`
 *    is shown as it is (java-debug: "Add method not implemented" when a
 *    replace is refused). Any other type (java-debug's `STARTING`, `END`)
 *    is progress and ignored.
 *  - The request takes no arguments and answers `{changedClasses?: string[],
 *    errorMessage?: string}`: the names of the replaced types, or (a
 *    successful response all the same) why nothing was replaced, e.g.
 *    `{"changedClasses":[],"errorMessage":"Failed to redefine classes: Add method not implemented"}`.
 *  - The declaration may name the plugin's own setting holding the mode
 *    (`modeSetting`; the host checks at install that it is a global string
 *    setting with enum exactly `auto` | `manual` | `never`): auto sends the
 *    request on every `BUILD_COMPLETE`, manual waits for 「应用代码更改」,
 *    never ignores the events (the plugin also tells its adapter not to send
 *    them). Without `modeSetting` the mode is auto.
 */
import type { DapStackFrame } from "@/domain/debug/types";

export type HotCodeReplaceMode = "auto" | "manual" | "never";

/** Characters of an adapter's message the debug window shows; the rest is in its tooltip. */
export const HOT_CODE_REPLACE_MESSAGE_MAX = 2000;

/** text cut to HOT_CODE_REPLACE_MESSAGE_MAX characters (code points), with an ellipsis when cut. */
export function shortHotCodeReplaceMessage(text: string): string {
  const chars = Array.from(text);
  return chars.length <= HOT_CODE_REPLACE_MESSAGE_MAX ? text : `${chars.slice(0, HOT_CODE_REPLACE_MESSAGE_MAX).join("")}…`;
}

const MODES: readonly HotCodeReplaceMode[] = ["auto", "manual", "never"];

/** The mode a setting value selects: auto unless it is one of the modes. */
export function hotCodeReplaceMode(value: unknown): HotCodeReplaceMode {
  return MODES.find((mode) => mode === value) ?? "auto";
}

export type HotCodeReplaceEventKind = "buildComplete" | "warning" | "error" | "other";

export interface HotCodeReplaceEvent {
  kind: HotCodeReplaceEventKind;
  /** The adapter's text, shown as it is for warnings and errors. */
  message?: string;
}

const EVENT_KINDS: Record<string, HotCodeReplaceEventKind> = {
  BUILD_COMPLETE: "buildComplete",
  WARNING: "warning",
  ERROR: "error",
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The declared event's body; undefined when it is not `{changeType: string, …}`. */
export function parseHotCodeReplaceEvent(body: unknown): HotCodeReplaceEvent | undefined {
  if (!isRecord(body) || typeof body.changeType !== "string") return undefined;
  const kind = EVENT_KINDS[body.changeType] ?? "other";
  const message = typeof body.message === "string" && body.message.trim() !== "" ? body.message : undefined;
  return message === undefined ? { kind } : { kind, message };
}

export interface HotCodeReplaceResult {
  /** The replaced types, as the adapter names them. */
  changed: string[];
  /** Why nothing (or not everything) was replaced, as the adapter says it. */
  error?: string;
}

/** The declared request's answer; anything else counts as no type replaced. */
export function parseHotCodeReplaceResult(body: unknown): HotCodeReplaceResult {
  if (!isRecord(body)) return { changed: [] };
  const changed = Array.isArray(body.changedClasses)
    ? body.changedClasses.filter((name): name is string => typeof name === "string" && name !== "")
    : [];
  const error = typeof body.errorMessage === "string" && body.errorMessage.trim() !== "" ? body.errorMessage : undefined;
  return error === undefined ? { changed } : { changed, error };
}

/**
 * Whether frame runs in one of the replaced types. Type names are dotted
 * (`pkg.Type`, nested types joined as the adapter writes them); a frame is
 * named `<Type>.<member>…` with the type either fully qualified or as its
 * last segment (java-debug: `HotApp.value()`, `Outer$Inner.run()`).
 */
export function frameInChangedTypes(frame: Pick<DapStackFrame, "name">, changed: readonly string[]): boolean {
  return changed.some((name) => {
    const simple = name.slice(name.lastIndexOf(".") + 1);
    return [name, simple].some((type) => type !== "" && frame.name.startsWith(`${type}.`));
  });
}
