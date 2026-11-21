import { describe, expect, it, vi } from "vitest";
import { resolveLocalAgentDirectory } from "@/domain/ai/local-agent-directory";

function directories(available = false) {
  return {
    check: vi.fn(async (_path: string) => available),
    getDefault: vi.fn(async () => "/data/MinCode/agent-workspace"),
  };
}

describe("resolveLocalAgentDirectory", () => {
  it("only gets the default when no path is bound", async () => {
    const deps = directories();
    await expect(resolveLocalAgentDirectory("", false, deps)).resolves.toEqual({
      kind: "default", path: "/data/MinCode/agent-workspace",
    });
    expect(deps.check).not.toHaveBeenCalled();
    expect(deps.getDefault).toHaveBeenCalledOnce();
  });

  it("only accepts a missing path after explicit consent", async () => {
    const deps = directories();
    await expect(resolveLocalAgentDirectory("/original", false, deps)).resolves.toEqual({
      kind: "unavailable", path: "/original",
    });
    expect(deps.check).toHaveBeenCalledWith("/original");
    expect(deps.getDefault).not.toHaveBeenCalled();

    deps.check.mockResolvedValue(true);
    await expect(resolveLocalAgentDirectory("/original", true, deps)).resolves.toEqual({
      kind: "temporary", path: "/data/MinCode/agent-workspace",
    });
    expect(deps.check).toHaveBeenCalledOnce();
    await expect(resolveLocalAgentDirectory("/original", false, deps)).resolves.toEqual({
      kind: "project", path: "/original",
    });
    expect(deps.getDefault).toHaveBeenCalledOnce();
  });

  it("propagates the original errors from preflight and creation", async () => {
    const deps = directories();
    const denied = new Error("permission denied");
    deps.check.mockRejectedValueOnce(denied);
    await expect(resolveLocalAgentDirectory("/original", false, deps)).rejects.toBe(denied);
    expect(deps.getDefault).not.toHaveBeenCalled();

    const blocked = new Error("path is a file");
    deps.getDefault.mockRejectedValueOnce(blocked);
    await expect(resolveLocalAgentDirectory("", false, deps)).rejects.toBe(blocked);
  });
});
