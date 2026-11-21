export type AiRuleScope = "global" | "workspace";

export interface AiRuleDefinition {
  scope?: AiRuleScope;
  workspaceId?: string;
}

function normalizedRuleScope(rule: AiRuleDefinition): AiRuleScope {
  return rule.scope === "workspace" ? "workspace" : "global";
}

export function aiRuleAppliesToWorkspace(rule: AiRuleDefinition, workspaceId: string) {
  return normalizedRuleScope(rule) === "global" || rule.workspaceId === workspaceId;
}

export function applicableAiRules<T extends AiRuleDefinition>(
  rules: T[],
  workspaceId: string,
  scope?: AiRuleScope,
) {
  if (scope === "global") {
    return rules.filter((rule) => normalizedRuleScope(rule) === "global");
  }
  if (scope === "workspace") {
    return rules.filter(
      (rule) => normalizedRuleScope(rule) === "workspace" && rule.workspaceId === workspaceId,
    );
  }
  return rules.filter((rule) => aiRuleAppliesToWorkspace(rule, workspaceId));
}
