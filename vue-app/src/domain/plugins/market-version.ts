const versionPattern = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

function compareIdentifiers(a: string, b: string): number {
  const numericA = /^\d+$/.test(a);
  const numericB = /^\d+$/.test(b);
  if (numericA && numericB) return Math.sign(Number(a) - Number(b));
  if (numericA !== numericB) return numericA ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Semantic-version order matching the host's golang.org/x/mod/semver comparison; build metadata is ignored. */
export function comparePluginVersions(a: string, b: string): number | undefined {
  const left = versionPattern.exec(a);
  const right = versionPattern.exec(b);
  if (!left || !right) return undefined;
  for (let index = 1; index <= 3; index++) {
    const difference = Math.sign(Number(left[index]) - Number(right[index]));
    if (difference) return difference;
  }
  if (!left[4] || !right[4]) return left[4] ? -1 : right[4] ? 1 : 0;
  const leftParts = left[4].split(".");
  const rightParts = right[4].split(".");
  for (let index = 0; index < Math.min(leftParts.length, rightParts.length); index++) {
    const difference = compareIdentifiers(leftParts[index], rightParts[index]);
    if (difference) return difference;
  }
  return Math.sign(leftParts.length - rightParts.length);
}

export function isNewerPluginVersion(candidate: string | null | undefined, installed: string): boolean {
  return !!candidate && comparePluginVersions(candidate, installed) === 1;
}
