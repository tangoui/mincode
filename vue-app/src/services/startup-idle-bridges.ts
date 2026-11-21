import { scheduleIdleTask } from "@/services/idle-task";

export interface StartupIdleBridge {
  name: string;
  load: () => Promise<(() => void) | void> | (() => void) | void;
  timeout?: number;
}

export type IdleScheduler = (task: () => void, timeout?: number) => () => void;
export type StartupIdleBridgeError = (name: string, error: unknown) => void;

/**
 * Defers optional root-level bridges until after the first paint. A bridge may
 * resolve asynchronously; disposing before it resolves still disposes the
 * eventual result so an unmounted root cannot leak a listener.
 */
export function createStartupIdleBridgeCoordinator(
  bridges: readonly StartupIdleBridge[],
  schedule: IdleScheduler = scheduleIdleTask,
  onError: StartupIdleBridgeError = () => undefined,
) {
  let disposed = false;
  const installed = new Set<() => void>();
  const cancel = bridges.map((bridge) => schedule(() => {
    if (disposed) return;
    Promise.resolve()
      .then(() => bridge.load())
      .then((stop) => {
        if (typeof stop !== "function") return;
        if (disposed) stop();
        else installed.add(stop);
      })
      .catch((error: unknown) => onError(bridge.name, error));
  }, bridge.timeout));

  return () => {
    if (disposed) return;
    disposed = true;
    for (const stop of cancel) stop();
    for (const stop of installed) stop();
    installed.clear();
  };
}
