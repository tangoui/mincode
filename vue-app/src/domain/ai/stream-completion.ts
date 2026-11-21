export const AI_STREAM_CONNECT_TIMEOUT_MS = 5_000;

export type AiStreamConnectionFailure = "connect_timeout" | "connect_closed" | "interrupted";

export class AiStreamConnectionError extends Error {
  readonly reason: AiStreamConnectionFailure;

  constructor(reason: AiStreamConnectionFailure) {
    super(
      reason === "connect_timeout"
        ? "The AI response stream could not connect in time."
        : reason === "connect_closed"
          ? "The AI response stream closed before the connection opened."
          : "The AI response stream ended before completion.",
    );
    this.name = "AiStreamConnectionError";
    this.reason = reason;
  }
}

/**
 * Tracks one AI Stream from connection through its required terminal frame.
 * A transport close is successful only after the backend sent `done`; business
 * errors retain their original message, while premature disconnects fail closed.
 */
export function createAiStreamCompletion(
  onConnectTimeout: () => void,
  connectTimeoutMs = AI_STREAM_CONNECT_TIMEOUT_MS,
) {
  let opened = false;
  let settled = false;
  let resolveCompletion: () => void = () => {};
  let rejectCompletion: (error: Error) => void = () => {};
  const completed = new Promise<void>((resolve, reject) => {
    resolveCompletion = resolve;
    rejectCompletion = reject;
  });

  const clearConnectTimer = () => clearTimeout(connectTimer);
  const succeed = () => {
    if (settled) return;
    settled = true;
    clearConnectTimer();
    resolveCompletion();
  };
  const fail = (error: Error) => {
    if (settled) return;
    settled = true;
    clearConnectTimer();
    rejectCompletion(error);
  };
  const connectTimer = setTimeout(() => {
    fail(new AiStreamConnectionError("connect_timeout"));
    onConnectTimeout();
  }, connectTimeoutMs);

  return {
    completed,
    opened() {
      if (settled || opened) return false;
      opened = true;
      clearConnectTimer();
      return true;
    },
    terminal(error?: Error) {
      if (error) fail(error);
      else succeed();
    },
    disconnected() {
      fail(new AiStreamConnectionError(opened ? "interrupted" : "connect_closed"));
    },
    cancel: succeed,
    dispose: clearConnectTimer,
  };
}

/** Retry only a failed handshake, never a request whose send may have started. */
export async function retryAiStreamConnection(
  connect: () => Promise<void>,
  signal?: AbortSignal,
  onFailure?: (error: AiStreamConnectionError, attempt: number) => void,
): Promise<void> {
  for (let attempt = 1; attempt <= 2; attempt += 1) {
    if (signal?.aborted) throw new DOMException("The AI request was cancelled.", "AbortError");
    try {
      await connect();
      return;
    } catch (error) {
      if (!(error instanceof AiStreamConnectionError) || error.reason === "interrupted") throw error;
      onFailure?.(error, attempt);
      if (attempt === 2) throw error;
    }
  }
}
