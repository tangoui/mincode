export interface LatestRequestToken<T> {
  generation: number;
  target: T;
}

export function createLatestRequestGate<T>(currentTarget: () => T) {
  let generation = 0;

  return {
    begin(target = currentTarget()): LatestRequestToken<T> {
      generation += 1;
      return { generation, target };
    },
    invalidate(): void {
      generation += 1;
    },
    isCurrent(token: LatestRequestToken<T>): boolean {
      return token.generation === generation && Object.is(token.target, currentTarget());
    },
  };
}
