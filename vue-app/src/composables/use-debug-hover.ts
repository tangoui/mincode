/**
 * Debug hover: while the selected debug session is paused, hovering a word
 * (with its member chain, `a.b`) in a file of that session's project shows
 * its value, evaluated in the selected frame with the `hover` context. A
 * structured value lists its first children and links to the Evaluate
 * dialog, which shows it as an expandable variable tree (Monaco hovers
 * render Markdown only).
 *
 * One hover provider per Monaco language, registered once for every
 * workspace using this composable and disposed with the last one. Monaco
 * merges it with the language servers' hovers; it returns null whenever it
 * has nothing to add, so LSP hovers are unchanged. Monaco-free until Monaco
 * has loaded (the registration goes through the contribution hook).
 */
import { onBeforeUnmount } from "vue";
import { hoverExpressionAt } from "@/domain/debug/inline-values";
import { toRelativeKey } from "@/domain/debug/paths";
import type { DebugSessionView } from "@/domain/debug/session-model";
import type { DapEvaluateResult, DapVariable } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { pathsAreCaseInsensitive } from "@/stores/debug-configs";
import { useDebugSessionsStore, type DebugEvaluateContext } from "@/stores/debug-sessions";

export const DEBUG_INSPECT_COMMAND = "mincode.debug.inspectExpression";
const HOVER_VALUE_CHARS = 500;
const HOVER_CHILD_VALUE_CHARS = 120;
const HOVER_CHILDREN = 10;

export interface DebugHoverDeps {
  session: () => DebugSessionView | null;
  evaluate: (id: string, expression: string, context: DebugEvaluateContext) => Promise<DapEvaluateResult>;
  loadVariables: (id: string, variablesReference: number) => Promise<DapVariable[]>;
  caseInsensitive: () => boolean;
  /** Label of the "inspect in Evaluate" link; no link without it. */
  inspectLabel?: () => string;
  moreLabel: (count: number) => string;
}

export interface DebugHoverModel {
  uri: { scheme: string; path: string };
  getLineContent(line: number): string;
}

export interface DebugHoverPosition {
  lineNumber: number;
  column: number;
}

export interface DebugHoverResult {
  range: { startLineNumber: number; startColumn: number; endLineNumber: number; endColumn: number };
  contents: Array<{ value: string; isTrusted?: boolean | { enabledCommands: string[] } }>;
}

/** The file path of a `file:` model URI (`/C:/x` → `C:/x`), else null. */
function modelFilePath(uri: { scheme: string; path: string }): string | null {
  if (uri.scheme !== "file") return null;
  return /^\/[A-Za-z]:\//.test(uri.path) ? uri.path.slice(1) : uri.path;
}

function oneLine(value: string, max: number): string {
  const single = value.replace(/\r?\n/g, " ");
  return single.length > max ? `${single.slice(0, max - 1)}…` : single;
}

/** A fenced Markdown code block whose fence is longer than any backtick run in text. */
function codeBlock(text: string): string {
  const longest = Math.max(0, ...[...text.matchAll(/`+/g)].map((match) => match[0].length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return `${fence}\n${text}\n${fence}`;
}

function escapeMarkdown(text: string): string {
  return text.replace(/[\\`*_{}[\]()#+\-.!|<>]/g, "\\$&");
}

function samePausedFrame(before: DebugSessionView, after: DebugSessionView | null): boolean {
  return Boolean(after && after.id === before.id && after.state === "paused" && after.selectedFrameId === before.selectedFrameId);
}

/**
 * The debug hover at position, or null: no paused session, a file outside its
 * project, no expression there, an evaluation error (hovers never show errors),
 * or the program moved on meanwhile.
 */
export async function provideDebugHover(
  deps: DebugHoverDeps,
  model: DebugHoverModel,
  position: DebugHoverPosition,
  cancelled: () => boolean = () => false,
): Promise<DebugHoverResult | null> {
  const session = deps.session();
  if (!session || session.state !== "paused" || session.noDebug || session.selectedFrameId === undefined) return null;
  const path = modelFilePath(model.uri);
  if (!path || toRelativeKey(session.projectRoot, path, deps.caseInsensitive()) === null) return null;
  const target = hoverExpressionAt(model.getLineContent(position.lineNumber), position.column);
  if (!target) return null;
  let result: DapEvaluateResult;
  try {
    result = await deps.evaluate(session.id, target.expression, "hover");
  } catch {
    return null;
  }
  const structured = result.variablesReference > 0;
  const children = structured ? await deps.loadVariables(session.id, result.variablesReference) : [];
  if (cancelled() || !samePausedFrame(session, deps.session())) return null;
  const type = result.type ? `{${result.type}} ` : "";
  const lines = [`${target.expression} = ${type}${oneLine(result.result, HOVER_VALUE_CHARS)}`];
  for (const child of children.slice(0, HOVER_CHILDREN)) lines.push(`  ${child.name} = ${oneLine(child.value, HOVER_CHILD_VALUE_CHARS)}`);
  if (children.length > HOVER_CHILDREN) lines.push(`  ${deps.moreLabel(children.length - HOVER_CHILDREN)}`);
  const contents: DebugHoverResult["contents"] = [{ value: codeBlock(lines.join("\n")) }];
  const inspect = deps.inspectLabel?.();
  if (structured && inspect) {
    const args = encodeURIComponent(JSON.stringify([target.expression]));
    contents.push({
      value: `[${escapeMarkdown(inspect)}](command:${DEBUG_INSPECT_COMMAND}?${args})`,
      isTrusted: { enabledCommands: [DEBUG_INSPECT_COMMAND] },
    });
  }
  return {
    range: { startLineNumber: position.lineNumber, startColumn: target.startColumn, endLineNumber: position.lineNumber, endColumn: target.endColumn },
    contents,
  };
}

// ---- registration (shared by every workspace) ----

interface HoverUser {
  active: () => boolean;
  onInspect: (expression: string) => void;
}

const users = new Set<HoverUser>();
let deps: DebugHoverDeps | null = null;
let stopHook: (() => void) | null = null;
/** The command is registered once Monaco's command module has loaded (the link shows from then on). */
let commandReady = false;

function inspect(expression: string) {
  const list = [...users];
  (list.find((user) => user.active()) ?? list[0])?.onInspect(expression);
}

function install(monaco: MonacoApi): () => void {
  const providers = new Map<string, { dispose(): void }>();
  const ensure = (languageId: string) => {
    if (providers.has(languageId)) return;
    providers.set(
      languageId,
      monaco.languages.registerHoverProvider(languageId, {
        provideHover: (model, position, token) =>
          deps ? provideDebugHover(deps, model, position, () => token.isCancellationRequested) : null,
      })
    );
  };
  for (const model of monaco.editor.getModels()) ensure(model.getLanguageId());
  const listeners = [
    monaco.editor.onDidCreateModel((model) => ensure(model.getLanguageId())),
    monaco.editor.onDidChangeModelLanguage((event) => ensure(event.model.getLanguageId())),
  ];
  let disposed = false;
  let stopCommand: (() => void) | undefined;
  void import("@/services/monaco-commands")
    .then(({ registerMonacoCommand }) => {
      if (disposed) return;
      stopCommand = registerMonacoCommand(DEBUG_INSPECT_COMMAND, (expression) => {
        if (typeof expression === "string") inspect(expression);
      });
      commandReady = true;
    })
    .catch(() => undefined);
  return () => {
    disposed = true;
    commandReady = false;
    stopCommand?.();
    for (const listener of listeners) listener.dispose();
    for (const provider of providers.values()) provider.dispose();
    providers.clear();
  };
}

/**
 * Enables the debug hover while the calling component is mounted.
 * `onInspect` opens the Evaluate dialog with an expression (the hover's
 * link); with several workspaces, the active one gets it.
 */
export function useDebugHover(options: HoverUser) {
  const sessions = useDebugSessionsStore();
  const { t } = useVueI18n();
  deps ??= {
    session: () => sessions.selectedSession,
    evaluate: (id, expression, context) => sessions.evaluate(id, expression, context),
    loadVariables: (id, reference) => sessions.loadVariables(id, reference),
    caseInsensitive: pathsAreCaseInsensitive,
    inspectLabel: () => (commandReady ? t("debug.hover.inspect") : ""),
    moreLabel: (count) => t("debug.hover.more", { count }),
  };
  const user: HoverUser = { active: options.active, onInspect: options.onInspect };
  users.add(user);
  stopHook ??= registerMonacoContributionHook(install);
  onBeforeUnmount(() => {
    users.delete(user);
    if (users.size) return;
    stopHook?.();
    stopHook = null;
    deps = null;
  });
}
