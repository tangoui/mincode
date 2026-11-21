import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  BROWSER_COMPUTER_USE_SYSTEM_GUIDANCE,
  DESKTOP_COMPUTER_USE_SYSTEM_GUIDANCE,
  computerUseGuidanceFor,
} from "../src/domain/ai/computer-use.ts";

test("computer-use guides are sent only with their tools", () => {
  assert.equal(computerUseGuidanceFor([]), "");
  assert.equal(computerUseGuidanceFor(["workspace.read_file", "git.status"]), "");
  assert.equal(computerUseGuidanceFor(["browser.click"]), BROWSER_COMPUTER_USE_SYSTEM_GUIDANCE);
  assert.equal(computerUseGuidanceFor(["computer.capture_screen"]), DESKTOP_COMPUTER_USE_SYSTEM_GUIDANCE);
  assert.equal(
    computerUseGuidanceFor(["computer.execute", "browser.capture_view"]),
    `${BROWSER_COMPUTER_USE_SYSTEM_GUIDANCE}\n\n${DESKTOP_COMPUTER_USE_SYSTEM_GUIDANCE}`,
  );
});

test("the assistant builds its system prompt after choosing the turn's tools", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.doesNotMatch(drawer, /COMPUTER_USE_SYSTEM_GUIDANCE/);
  const selected = drawer.indexOf("const selectedTools = currentTools();");
  const guidance = drawer.indexOf("computerUseGuidanceFor(selectedTools.map((tool) => tool.name))");
  const history = drawer.indexOf("const uncompactedHistory: AiLoopMessage[]");
  assert.ok(selected > 0 && selected < guidance && guidance < history);
});
