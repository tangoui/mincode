export function createAbortError(message = "Operation was cancelled.") {
  return new DOMException(message, "AbortError");
}

export function throwIfAborted(signal?: AbortSignal) {
  if (signal?.aborted) throw createAbortError();
}

export function runWithAbort<T>(
  signal: AbortSignal | undefined,
  operation: () => Promise<T> | T,
): Promise<T> {
  try {
    throwIfAborted(signal);
  } catch (error) {
    return Promise.reject(error);
  }
  if (!signal) return Promise.resolve().then(operation);

  let handleAbort: (() => void) | undefined;
  const aborted = new Promise<never>((_, reject) => {
    handleAbort = () => reject(createAbortError());
    signal.addEventListener("abort", handleAbort, { once: true });
  });

  return Promise.race([Promise.resolve().then(operation), aborted]).finally(
    () => {
      if (handleAbort) signal.removeEventListener("abort", handleAbort);
    },
  );
}
