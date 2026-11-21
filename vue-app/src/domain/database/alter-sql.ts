import {
  qualifiedTable,
  quoteIdentifier,
  supportsCopyAsInsertSql,
} from "@/domain/database/insert-sql";
import type { DatabaseColumnInfo, DbType } from "@/domain/database/types";

export interface BuildColumnAlterSqlInput {
  dbType: DbType;
  database?: string;
  table: string;
  column: DatabaseColumnInfo;
}

/** 与 INSERT 复制一致，ALTER 生成只覆盖 SQL 方言连接。 */
export function supportsCopyAsAlterSql(dbType: DbType): boolean {
  return supportsCopyAsInsertSql(dbType);
}

function columnDefinition(dbType: DbType, column: DatabaseColumnInfo): string {
  const parts = [quoteIdentifier(dbType, column.name), column.dataType || "TEXT"];
  if (!column.nullable) parts.push("NOT NULL");
  if (column.defaultValue != null && column.defaultValue !== "") {
    parts.push(`DEFAULT ${column.defaultValue}`);
  }
  if (column.comment && (dbType === "mysql" || dbType === "mariadb" || dbType === "tidb")) {
    parts.push(`COMMENT '${column.comment.replace(/'/g, "''")}'`);
  }
  return parts.join(" ");
}

/**
 * 生成该列的 ALTER TABLE ADD 语句，便于把结果集里的列定义带到迁移/建表场景。
 */
export function buildColumnAlterSql(input: BuildColumnAlterSqlInput): string {
  const table = input.table.trim();
  if (!supportsCopyAsAlterSql(input.dbType)) {
    throw new Error(`Copy as SQL ALTER is not supported for ${input.dbType}`);
  }
  if (!table) throw new Error("SQL ALTER requires a table name");
  const target = qualifiedTable(input.dbType, input.database, table);
  return `ALTER TABLE ${target}\nADD ${columnDefinition(input.dbType, input.column)};`;
}
