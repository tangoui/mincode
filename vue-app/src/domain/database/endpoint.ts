import type { DatabaseConnection } from "./types";

export type ConnectionEndpointFields = Partial<
  Pick<DatabaseConnection, "host" | "port" | "filePath" | "database">
>;

/**
 * Short address shown next to a connection: the file path for file-based
 * engines, otherwise `host:port`, then the database name, and finally
 * `managedLabel` for services that expose none of these.
 */
export function connectionEndpoint(connection: ConnectionEndpointFields, managedLabel: string): string {
  if (connection.filePath) return connection.filePath;
  if (connection.host) return connection.port ? `${connection.host}:${connection.port}` : connection.host;
  return connection.database || managedLabel;
}
