import type {
  DatabaseSchemaChange,
  DatabaseSchemaSyncExecutionResult,
  DatabaseSchemaSyncPlan,
  DatabaseSchemaSyncSelection,
} from "@/domain/database/types";

export function defaultSchemaSyncSelection(changes: DatabaseSchemaChange[]): string[] {
  return changes.filter((change) => change.defaultSelected).map((change) => change.id);
}

export function normalizeSchemaSyncSelection(
  changes: DatabaseSchemaChange[],
  selectedChangeIds: string[]
): string[] {
  const available = new Set(changes.map((change) => change.id));
  return [...new Set(selectedChangeIds)].filter((id) => available.has(id));
}

export function schemaSyncPlanMatchesSelection(
  plan: DatabaseSchemaSyncPlan | undefined,
  selection: DatabaseSchemaSyncSelection,
  sourceFingerprint: string | undefined,
  targetFingerprint: string | undefined,
  selectedChangeIds: string[],
  dialect: string | undefined
): boolean {
  if (
    !plan ||
    !sourceFingerprint ||
    !targetFingerprint ||
    plan.dialect !== dialect ||
    plan.database !== selection.targetDatabase ||
    plan.sourceFingerprint !== sourceFingerprint ||
    plan.targetFingerprint !== targetFingerprint ||
    plan.statements.length !== selectedChangeIds.length
  ) return false;
  const planned = new Set(plan.statements.map((statement) => statement.changeId));
  return selectedChangeIds.every((id) => planned.has(id));
}

export function schemaSyncDialectForDbType(dbType: string): string | undefined {
  switch (dbType) {
    case "mysql":
    case "postgres":
    case "sqlite":
    case "sqlserver":
    case "oracle":
      return dbType;
    case "dameng":
    case "dm":
      return "dm";
    default:
      return undefined;
  }
}

export function schemaSyncRequiresDangerConfirmation(
  changes: DatabaseSchemaChange[],
  selectedChangeIds: string[]
): boolean {
  const selected = new Set(selectedChangeIds);
  return changes.some((change) => selected.has(change.id) && change.risk === "dangerous");
}

export function schemaSyncExecutionStatus(
  result: DatabaseSchemaSyncExecutionResult | undefined,
  changeId: string
) {
  return result?.statements.find((statement) => statement.changeId === changeId);
}
