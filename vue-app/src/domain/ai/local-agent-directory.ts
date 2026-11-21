type LocalAgentDirectory = {
  kind: "project" | "default" | "temporary" | "unavailable";
  path: string;
};

export async function resolveLocalAgentDirectory(
  boundRoot: string,
  useDefaultOnce: boolean,
  deps: { check(path: string): Promise<boolean>; getDefault(): Promise<string> },
): Promise<LocalAgentDirectory> {
  if (!boundRoot) return { kind: "default", path: await deps.getDefault() };
  if (useDefaultOnce) return { kind: "temporary", path: await deps.getDefault() };
  return { kind: await deps.check(boundRoot) ? "project" : "unavailable", path: boundRoot };
}
