import { expect, it, vi } from "vitest";
import { DEFAULT_ACP_ADAPTER_ID, acpAdapterLabel, startAcpAgent } from "@/domain/ai/acp-agent-start";

it("starts the selected built-in or custom adapter", async () => {
  const launch = vi.fn(async (_id: string) => {});
  for (const id of ["codex", "custom:mine"]) {
    await expect(startAcpAgent({ selectedAdapterId: id, recordedAdapterId: undefined, hasPreviousSession: false }, launch))
      .resolves.toEqual({ kind: "started", adapterId: id });
    expect(launch).toHaveBeenLastCalledWith(id);
  }
  expect(acpAdapterLabel("custom:mine", [{ id: "mine", name: "My agent" }])).toBe("My agent");
});

it("falls back for an unavailable selection while protecting earlier ACP sessions", async () => {
  const legacy = "plugin:demo.agents/helper";
  const launch = vi.fn(async (_id: string) => {});
  await expect(startAcpAgent({ selectedAdapterId: legacy, recordedAdapterId: legacy, hasPreviousSession: true }, launch))
    .resolves.toEqual({ kind: "conversation-unavailable", recordedAdapterId: legacy, adapterId: DEFAULT_ACP_ADAPTER_ID });
  expect(launch).not.toHaveBeenCalled();
  await expect(startAcpAgent({ selectedAdapterId: legacy, recordedAdapterId: undefined, hasPreviousSession: false }, launch))
    .resolves.toEqual({ kind: "started", adapterId: DEFAULT_ACP_ADAPTER_ID });
  expect(launch).toHaveBeenCalledWith(DEFAULT_ACP_ADAPTER_ID);
});
