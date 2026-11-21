const permanentlyDeletedScopeIds = new Set<string>();

export function isWorkspaceScopePermanentlyDeleted(scopeId: string) {
  return permanentlyDeletedScopeIds.has(scopeId);
}

/** Prevents cached views from recreating scoped state after permanent deletion. */
export function markWorkspaceScopePermanentlyDeleted(scopeId: string) {
  const normalized = scopeId.trim();
  if (normalized) permanentlyDeletedScopeIds.add(normalized);
}

export function unmarkWorkspaceScopePermanentlyDeleted(scopeId: string) {
  permanentlyDeletedScopeIds.delete(scopeId.trim());
}
