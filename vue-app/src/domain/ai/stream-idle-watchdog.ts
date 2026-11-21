export const AI_STREAM_IDLE_TIMEOUT_MS = 300_000;
export const AI_STREAM_IDLE_TIMEOUT_MESSAGE = "The AI response stream stopped producing data.";

export class AiStreamIdleTimeoutError extends Error {
  constructor() {
    super(AI_STREAM_IDLE_TIMEOUT_MESSAGE);
    this.name = "AiStreamIdleTimeoutError";
  }
}

export function isAiStreamIdleTimeoutError(error: unknown): error is AiStreamIdleTimeoutError {
  return error instanceof Error
    && (error.name === "AiStreamIdleTimeoutError" || error.message === AI_STREAM_IDLE_TIMEOUT_MESSAGE);
}

export function createAiStreamIdleWatchdog(
  onTimeout: () => void,
  timeoutMs = AI_STREAM_IDLE_TIMEOUT_MS,
) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;
  let disposed = false;
  let settled = false;
  let rejectTimeout: (error: Error) => void = () => {};
  const expired = new Promise<never>((_resolve, reject) => {
    rejectTimeout = reject;
  });

  const schedule = (currentGeneration: number) => {
    timer = setTimeout(() => {
      if (disposed || settled || currentGeneration !== generation) return;
      timer = undefined;
      settled = true;
      rejectTimeout(new AiStreamIdleTimeoutError());
      onTimeout();
    }, timeoutMs);
  };
  const touch = () => {
    if (disposed || settled) return;
    generation += 1;
    if (timer) clearTimeout(timer);
    schedule(generation);
  };
  const dispose = () => {
    disposed = true;
    generation += 1;
    if (timer) clearTimeout(timer);
    timer = undefined;
  };
  touch();
  return { expired, touch, dispose };
}
