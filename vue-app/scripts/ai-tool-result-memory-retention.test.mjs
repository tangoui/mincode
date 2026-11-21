import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const loopSourceUrl = new URL("../src/services/ai-agent-loop.ts", import.meta.url);
const drawerSourceUrl = new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url);
const storeSourceUrl = new URL("../src/stores/ai-assistant.ts", import.meta.url);

test("AI agent history retains bounded tool context instead of raw execution data", async () => {
  const [loopSource, drawerSource, storeSource] = await Promise.all([
    readFile(loopSourceUrl, "utf8"),
    readFile(drawerSourceUrl, "utf8"),
    readFile(storeSourceUrl, "utf8"),
  ]);

  assert.match(
    loopSource,
    /toolResult\?: Pick<AiLoopToolExecution, "status" \| "source">/,
  );
  assert.match(
    loopSource,
    /toolResult:\s*\{\s*status: result\.status,[\s\S]*?source: result\.source \? \{ \.\.\.result\.source \} : undefined,[\s\S]*?\}/,
  );
  assert.doesNotMatch(loopSource, /data\?: unknown/);
  assert.doesNotMatch(loopSource, /data:\s*result\.data/);
  assert.doesNotMatch(drawerSource, /data:\s*result,/);
  assert.doesNotMatch(drawerSource, /data:\s*sanitizeAiResult\(safeResult\)/);
  assert.doesNotMatch(storeSource, /JSON\.parse\(JSON\.stringify\(message\.toolResult\)\)/);
});
