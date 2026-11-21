export type PluginApprovalRisk = "read" | "write" | "dangerous";

/**
 * 普通读取和写入操作由宿主默认放行；不可逆或高风险操作仍需用户确认。
 */
export function requiresPluginApproval(risk: PluginApprovalRisk): boolean {
  return risk === "dangerous";
}
