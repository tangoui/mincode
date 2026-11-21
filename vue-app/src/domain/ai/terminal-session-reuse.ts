export interface AiTerminalSessionReference {
  tabId: string;
  paneId: string;
}

export interface AiTerminalSessionReuseRegistry {
  resolve(
    conversationId: string,
    isAvailable: (reference: AiTerminalSessionReference) => boolean
  ): AiTerminalSessionReference | undefined;
  remember(conversationId: string, reference: AiTerminalSessionReference): void;
  forget(conversationId: string): void;
  clear(): void;
}

export function createAiTerminalSessionReuseRegistry(): AiTerminalSessionReuseRegistry {
  const sessions = new Map<string, AiTerminalSessionReference>();

  return {
    resolve(conversationId, isAvailable) {
      const key = conversationId.trim();
      if (!key) return undefined;
      const reference = sessions.get(key);
      if (!reference) return undefined;
      if (isAvailable(reference)) return { ...reference };
      sessions.delete(key);
      return undefined;
    },
    remember(conversationId, reference) {
      const key = conversationId.trim();
      if (!key) return;
      sessions.set(key, { ...reference });
    },
    forget(conversationId) {
      sessions.delete(conversationId.trim());
    },
    clear() {
      sessions.clear();
    },
  };
}
