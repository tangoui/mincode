// Frame-local save guards run before a workspace tab is removed or moved to
// another native window. A rejection leaves the original view mounted.
const guards = new Map<string, () => Promise<void>>();
export function registerPluginViewSave(id: string, save: () => Promise<void>): () => void {
  guards.set(id, save);
  return () => { if (guards.get(id) === save) guards.delete(id); };
}
export async function flushPluginView(id: string): Promise<void> {
  await guards.get(id)?.();
}
