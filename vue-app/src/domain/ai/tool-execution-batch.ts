/**
 * Runs contiguous independent calls concurrently while preserving their input order
 * in the returned results. Calls that are not explicitly marked parallel-safe stay
 * serialized, which keeps mutations and externally managed tools deterministic.
 */
export async function executeToolBatches<T, R>(
  calls: readonly T[],
  canRunInParallel: (call: T) => boolean,
  execute: (call: T) => Promise<R>,
): Promise<R[]> {
  const results: R[] = [];
  let index = 0;

  while (index < calls.length) {
    const call = calls[index];
    if (!canRunInParallel(call)) {
      results.push(await execute(call));
      index += 1;
      continue;
    }

    let end = index + 1;
    while (end < calls.length && canRunInParallel(calls[end])) end += 1;
    results.push(...await Promise.all(calls.slice(index, end).map((parallelCall) => execute(parallelCall))));
    index = end;
  }

  return results;
}

/**
 * Results for a batch that stopped part-way, in call order: calls that finished keep
 * their result and every other call gets `interrupted`.
 */
export function interruptedToolBatchResults<T extends { id: string }, R>(
  calls: readonly T[],
  finished: ReadonlyMap<string, R>,
  interrupted: R,
): R[] {
  return calls.map((call) => finished.get(call.id) ?? interrupted);
}
