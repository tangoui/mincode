import type { DbType } from "./types";

const mysqlFamilyTypes = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
]);

const postgresFamilyTypes = new Set<DbType>([
  "postgres",
  "kingbase",
  "gaussdb",
  "opengauss",
  "highgo",
  "vastbase",
  "opentenbase",
  "gbase8c",
]);

const mysqlColumnDataTypes = [
  "TINYINT",
  "SMALLINT",
  "MEDIUMINT",
  "INT",
  "INT UNSIGNED",
  "BIGINT",
  "BIGINT UNSIGNED",
  "DECIMAL(10,2)",
  "FLOAT",
  "DOUBLE",
  "BIT(1)",
  "BOOLEAN",
  "CHAR(255)",
  "VARCHAR(255)",
  "TINYTEXT",
  "TEXT",
  "MEDIUMTEXT",
  "LONGTEXT",
  "BINARY(255)",
  "VARBINARY(255)",
  "TINYBLOB",
  "BLOB",
  "MEDIUMBLOB",
  "LONGBLOB",
  "DATE",
  "TIME",
  "DATETIME",
  "TIMESTAMP",
  "YEAR",
  "JSON",
  "ENUM('value1','value2')",
  "SET('value1','value2')",
] as const;

const postgresColumnDataTypes = [
  "SMALLINT",
  "INTEGER",
  "BIGINT",
  "DECIMAL(10,2)",
  "NUMERIC(10,2)",
  "REAL",
  "DOUBLE PRECISION",
  "SMALLSERIAL",
  "SERIAL",
  "BIGSERIAL",
  "BOOLEAN",
  "CHAR(255)",
  "VARCHAR(255)",
  "TEXT",
  "BYTEA",
  "DATE",
  "TIME",
  "TIME WITH TIME ZONE",
  "TIMESTAMP",
  "TIMESTAMP WITH TIME ZONE",
  "INTERVAL",
  "UUID",
  "JSON",
  "JSONB",
  "XML",
  "INET",
  "CIDR",
  "MACADDR",
  "INTEGER[]",
  "TEXT[]",
] as const;


const firebirdColumnDataTypes = [
  "SMALLINT",
  "INTEGER",
  "BIGINT",
  "NUMERIC(18,2)",
  "DECIMAL(18,2)",
  "FLOAT",
  "DOUBLE PRECISION",
  "BOOLEAN",
  "CHAR(255)",
  "VARCHAR(255)",
  "BLOB SUB_TYPE TEXT",
  "BLOB",
  "DATE",
  "TIME",
  "TIMESTAMP",
] as const;

export function columnDataTypeOptions(dbType?: DbType): readonly string[] {
  if (dbType && mysqlFamilyTypes.has(dbType)) return mysqlColumnDataTypes;
  if (dbType && postgresFamilyTypes.has(dbType)) return postgresColumnDataTypes;
  if (dbType === "firebird") return firebirdColumnDataTypes;
  return [];
}

export function defaultColumnDataType(dbType?: DbType): string {
  if (dbType && postgresFamilyTypes.has(dbType)) return "VARCHAR(255)";
  return "VARCHAR(255)";
}
