import { databaseTypeLabels, type DatabaseConnection, type DbType } from "./types";

export interface DatabaseDriverDescriptor {
  dialectType: DbType;
  title: string;
  available: boolean;
}

export function resolveConnectionDriver(
  connection: Pick<DatabaseConnection, "dbType" | "driver">,
): DatabaseDriverDescriptor {
  return {
    dialectType: connection.dbType,
    title: databaseTypeLabels[connection.dbType],
    available: !connection.driver && connection.dbType !== "plugin",
  };
}
