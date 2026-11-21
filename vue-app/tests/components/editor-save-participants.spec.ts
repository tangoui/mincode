import { afterEach, expect, it, vi } from "vitest";
import { registerEditorSaveParticipant, runEditorSaveParticipants } from "@/services/editor-save-participants";

afterEach(() => vi.useRealTimers());
it("cancels timed-out formatting and cannot apply its late result", async () => {
  vi.useFakeTimers();
  let finish!: () => void;
  let applied = false;
  const task = runEditorSaveParticipants({ path: "/a", external: false, formatterHandled: true,
    format: async (signal) => { await new Promise<void>((r) => { finish = r; }); if (!signal.aborted) applied = true; } });
  await vi.advanceTimersByTimeAsync(5000);
  expect(await task).toHaveLength(1);
  finish(); await Promise.resolve(); expect(applied).toBe(false);
});
it("passes parent cancellation to participants and skips later participants", async () => {
  const parent = new AbortController();
  let signal!: AbortSignal;
  const unregister = registerEditorSaveParticipant(async (ctx) => { signal = ctx.signal!; parent.abort(); });
  const later = vi.fn().mockResolvedValue(undefined);
  const removeLater = registerEditorSaveParticipant(later);
  try {
    await runEditorSaveParticipants({ path: "/a", external: false, signal: parent.signal });
    expect(signal.aborted).toBe(true); expect(later).not.toHaveBeenCalled();
  } finally { unregister(); removeLater(); }
});
