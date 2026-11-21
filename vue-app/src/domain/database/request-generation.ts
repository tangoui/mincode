export interface DatabaseRequestGeneration {
  scopeId: string;
  sessionId: string;
  target: string;
  generation: number;
}

export const createRequestGenerationGate = () => {
  const generations = new Map<string, number>();
  const keyOf = (request: Omit<DatabaseRequestGeneration, "generation">) => {
    const separator = request.target.indexOf(":");
    const channel = separator < 0 ? request.target : request.target.slice(0, separator);
    return `${request.scopeId}\u0000${request.sessionId}\u0000${channel}`;
  };

  const begin = (scopeId: string, sessionId: string, target: string): DatabaseRequestGeneration => {
    const key = keyOf({ scopeId, sessionId, target });
    const generation = (generations.get(key) ?? 0) + 1;
    generations.set(key, generation);
    return { scopeId, sessionId, target, generation };
  };

  const isCurrent = (request: DatabaseRequestGeneration) =>
    generations.get(keyOf(request)) === request.generation;

  const finish = (request: DatabaseRequestGeneration) => isCurrent(request);

  const invalidateSession = (sessionId: string) => {
    for (const key of generations.keys()) {
      if (key.includes(`\u0000${sessionId}\u0000`)) generations.delete(key);
    }
  };

  return { begin, isCurrent, finish, invalidateSession };
};