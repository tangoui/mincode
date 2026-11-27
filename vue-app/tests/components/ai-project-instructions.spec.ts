import { beforeEach, expect, it, vi } from "vitest";

const host = vi.hoisted(() => ({
  invoke: vi.fn<(command: string, args: { projectRoot: string; filePath: string }) => Promise<unknown>>(),
}));
vi.mock("@/services/desktop-bridge", () => ({
  canInvoke: () => true,
  isDesktopRuntime: () => true,
  invokeBackend: host.invoke,
}));

beforeEach(() => {
  host.invoke.mockReset();
});

it("loads only the supported paths in order and rereads changed or newly created files", async () => {
  const { loadProjectInstructions } = await import("@/services/ai-project-instructions");
  const files = new Map([
    ["AGENTS.md", { modifiedAt: "2026-11-26T00:00:00Z", content: "Agents v1" }],
    [".mincode/rules.md", { modifiedAt: "2026-11-26T00:00:00Z", content: "MinCode rules" }],
    ["README.md", { modifiedAt: "2026-11-26T00:00:00Z", content: "Do not load" }],
  ]);
  host.invoke.mockImplementation(async (command, { filePath }) => {
    const file = files.get(filePath);
    if (!file) throw new Error("missing");
    return command === "local_fs_file_info"
      ? { isFile: true, size: file.content.length, modifiedAt: file.modifiedAt }
      : file.content;
  });

  const root = "/project-instructions/cache";
  const first = await loadProjectInstructions(root);
  expect(first.sources).toEqual(["AGENTS.md", ".mincode/rules.md"]);
  expect(first.prompt).toContain("### AGENTS.md\nAgents v1");
  expect(first.prompt).toContain("### .mincode/rules.md\nMinCode rules");

  files.set("AGENTS.md", { modifiedAt: "2026-11-26T00:00:01Z", content: "Agents v2" });
  files.set("CLAUDE.md", { modifiedAt: "2026-11-26T00:00:01Z", content: "Claude rules" });
  files.delete(".mincode/rules.md");
  const second = await loadProjectInstructions(root);
  expect(second.sources).toEqual(["AGENTS.md", "CLAUDE.md"]);
  expect(second.prompt).toContain("Agents v2");
  expect(second.prompt).toContain("Claude rules");
  expect(second.prompt).not.toContain("MinCode rules");

  host.invoke.mockClear();
  expect(await loadProjectInstructions(root)).toEqual(second);
  expect(host.invoke.mock.calls.map(([command]) => command)).toEqual(Array(3).fill("local_fs_file_info"));
});

it("caps the complete system section at 32 KiB without cutting a UTF-8 character", async () => {
  const { loadProjectInstructions } = await import("@/services/ai-project-instructions");
  const entry = "指示".repeat(6_000);
  host.invoke.mockImplementation(async (command) => command === "local_fs_file_info"
    ? { isFile: true, size: 36_000, modifiedAt: "2026-11-26T00:00:00Z" }
    : entry);
  const { prompt, sources } = await loadProjectInstructions("/project-instructions/limit");
  expect(sources).toEqual(["AGENTS.md"]);
  expect(new TextEncoder().encode(prompt).length).toBeLessThanOrEqual(32 * 1024);
  expect(prompt).toContain("[MinCode: project instructions truncated at 32 KiB]");
  expect(prompt).not.toContain("\uFFFD");
});
