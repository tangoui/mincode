/**
 * A narrow bridge for AI capabilities that must drive an already-rendered workspace.
 * The action is handled by the same view methods as a user interaction, so the visible
 * tabs, selections and persisted state stay in sync with the desktop UI.
 */
export type AiWorkspaceActionHandler = (input: Record<string, unknown>, signal?: AbortSignal) => Promise<unknown> | unknown;

const handlers = new Map<string, AiWorkspaceActionHandler>();
const waiters = new Map<string, Array<() => void>>();

function notify(scope: string) {
  const pending = waiters.get(scope) ?? [];
  waiters.delete(scope);
  for (const resolve of pending) resolve();
}

export function registerAiWorkspaceActionHandler(scope: string, handler: AiWorkspaceActionHandler) {
  handlers.set(scope, handler);
  notify(scope);
  return () => {
    if (handlers.get(scope) === handler) handlers.delete(scope);
  };
}

async function waitForHandler(scope: string, timeoutMs = 4_000) {
  if (handlers.has(scope)) return;
  await new Promise<void>((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      const pending = waiters.get(scope) ?? [];
      waiters.set(scope, pending.filter((item) => item !== onReady));
      reject(new Error(`AI workspace action handler is unavailable: ${scope}`));
    }, timeoutMs);
    const onReady = () => {
      window.clearTimeout(timeout);
      resolve();
    };
    waiters.set(scope, [...(waiters.get(scope) ?? []), onReady]);
  });
}

export async function requestAiWorkspaceAction(
  scope: string,
  input: Record<string, unknown>,
  signal?: AbortSignal,
): Promise<unknown> {
  signal?.throwIfAborted();
  await waitForHandler(scope);
  signal?.throwIfAborted();
  const handler = handlers.get(scope);
  if (!handler) throw new Error(`AI workspace action handler is unavailable: ${scope}`);
  return handler(input, signal);
}
