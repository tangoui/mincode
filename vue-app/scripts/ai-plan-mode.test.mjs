import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import {
  automaticProtocolPermissionOption,
  normalizeAiApprovalMode,
  planModeDenies,
  requiresAiToolApproval,
} from "../src/domain/ai/approval-policy.ts";
import { approvalModeForConversation } from "../src/domain/ai/conversation-approval.ts";

test("plan mode is a valid stored mode", () => {
  assert.equal(normalizeAiApprovalMode("plan"), "plan");
});

test("plan mode denies every non-read risk and allows read", () => {
  for (const risk of ["network", "write", "dangerous"]) assert.equal(planModeDenies("plan", risk), true);
  assert.equal(planModeDenies("plan", "read"), false);
  assert.equal(planModeDenies("ask", "write"), false);
});

test("plan mode still requires approval for side effects as a fallback", () => {
  assert.equal(requiresAiToolApproval("plan", "read"), false);
  for (const risk of ["network", "write", "dangerous"]) assert.equal(requiresAiToolApproval("plan", risk), true);
});

test("ACP permission requests in plan mode pick reject for writes and wait for the user on reads", () => {
  const options = [{ optionId: "ok", kind: "allow_once" }, { optionId: "no", kind: "reject_once" }];
  assert.equal(automaticProtocolPermissionOption("plan", "edit", options), "no");
  assert.equal(automaticProtocolPermissionOption("plan", "read", options), undefined);
});

test("ACP plan-mode rejection falls back to reject_always and never allows", () => {
  const always = [{ optionId: "ok", kind: "allow_always" }, { optionId: "never", kind: "reject_always" }];
  assert.equal(automaticProtocolPermissionOption("plan", "execute", always), "never");
  assert.equal(automaticProtocolPermissionOption("plan", "fetch", [{ optionId: "ok", kind: "allow_once" }]), undefined);
});

test("relayed conversations stay in plan mode but otherwise fall back to ask", () => {
  const external = { kind: "plugin", pluginId: "p", pluginName: "P", key: "chat:1" };
  assert.equal(approvalModeForConversation("plan", external), "plan");
  assert.equal(approvalModeForConversation("full-access", external), "ask");
  assert.equal(approvalModeForConversation("plan", undefined), "plan");
});

const drawer = readFileSync(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.notEqual(start, -1, `missing ${signature}`);
  const end = source.indexOf("\n}\n", start);
  assert.notEqual(end, -1, `unterminated ${signature}`);
  return source.slice(start, end);
}

const executeAgentTool = functionBody(drawer, "async function executeAgentTool(");
const mcpBranch = executeAgentTool.slice(
  executeAgentTool.indexOf("if (mcpTool) {"),
  executeAgentTool.indexOf("const capability = aiCapabilityRegistry.get(call.name);"),
);
const nativeBranch = executeAgentTool.slice(executeAgentTool.indexOf("const capability = aiCapabilityRegistry.get(call.name);"));

test("the drawer blocks non-read MCP calls in plan mode before approval", () => {
  assert.match(
    mcpBranch,
    /const mcpMode = approvalModeForConversation\(aiApprovalMode\.value, conversation\?\.external\);\s*if \(planModeDenies\(mcpMode, authorizationRequest\.risk\)\) \{\s*return \{ content: t\("ai\.agentic\.planModeBlocked"\), status: "cancelled" as const \};\s*\}[\s\S]*?requiresAiToolApproval\(mcpMode, authorizationRequest\.risk\)[\s\S]*?authorizeTool\([\s\S]*?callMcpToolCancellable/,
  );
});

test("the drawer blocks non-read native calls in plan mode before approval", () => {
  assert.match(
    nativeBranch,
    /const callMode = approvalModeForConversation\(aiApprovalMode\.value, conversation\?\.external\);\s*if \(planModeDenies\(callMode, callRisk\)\) \{\s*return \{ content: t\("ai\.agentic\.planModeBlocked"\), status: "cancelled" as const \};\s*\}[\s\S]*?requiresAiToolApproval\(callMode, callRisk\)[\s\S]*?authorizeTool\([\s\S]*?aiCapabilityRegistry\.execute\(/,
  );
});

// Plan constraint: a card already waiting when the user switches to plan mode is
// settled by the user's answer as usual; only the next call is judged by the new mode.
test("each call reads the approval mode afresh, after hydration and risk resolution", () => {
  assert.match(mcpBranch, /await approvalModeHydration;\s*const mcpMode = approvalModeForConversation\(aiApprovalMode\.value,/);
  assert.match(
    nativeBranch,
    /await approvalModeHydration;[\s\S]*?await capability\.resolveRisk\([\s\S]*?const callMode = approvalModeForConversation\(aiApprovalMode\.value,/,
  );
  // The mode is not captured once per turn and reused: every read in the tool path is live.
  for (const name of ["mcpMode", "callMode"]) {
    assert.equal((executeAgentTool.match(new RegExp(`\\b(?:const|let) ${name}\\b`, "g")) ?? []).length, 1);
  }
});

test("an approval granted after switching to plan mode is not re-checked against the mode", () => {
  for (const [branch, execution] of [[mcpBranch, "callMcpToolCancellable"], [nativeBranch, "aiCapabilityRegistry.execute("]]) {
    const afterApproval = branch.slice(branch.indexOf("authorizeTool("), branch.indexOf(execution));
    assert.ok(afterApproval.length > 0);
    assert.doesNotMatch(afterApproval, /aiApprovalMode|planModeDenies|approvalModeForConversation|requiresAiToolApproval|planModeBlocked/);
  }
  assert.doesNotMatch(
    functionBody(drawer, "async function authorizeTool("),
    /aiApprovalMode|planModeDenies|approvalModeForConversation|requiresAiToolApproval|planModeBlocked/,
  );
});

test("switching modes leaves pending approval cards alone", () => {
  assert.doesNotMatch(
    functionBody(drawer, "async function selectApprovalMode("),
    /toolApprovalQueue|resolveToolApproval|toolApprovals/,
  );
  for (const match of drawer.matchAll(/\bwatch(?:Effect)?\(/g)) {
    assert.doesNotMatch(drawer.slice(match.index, match.index + 160), /aiApprovalMode/);
  }
});
