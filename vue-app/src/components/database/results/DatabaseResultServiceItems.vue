<script setup lang="ts">
/**
 * "Process with plugin" (surface dbResult) in the result pane's cell menu.
 * Loaded with the first cell menu, so the target assembly, the result
 * serialization and the service menu stay out of the database workspace
 * chunk. The grid comes from the pane's props; the connection is the one the
 * rows came from (`source`: a table tab's, or the one the query ran on), never
 * the sidebar's current selection, and its names are read from the store.
 */
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import { dbResultServiceTarget, type DbResultGrid, type DbResultMenuSource } from "@/domain/database/result-service-target";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const props = defineProps<{
  /** The result pane's props: the grid as shown. */
  grid: DbResultGrid & { activeQueryRunning: boolean };
  /** Where the rows came from; null while no cell menu is open. */
  source: () => DbResultMenuSource | null;
}>();

const emit = defineEmits<{ run: [] }>();
const databaseStore = useDatabaseWorkspaceStore();

function target() {
  const source = props.source();
  if (!source) return null;
  const { grid } = props;
  const connection = source.connectionId ? databaseStore.connections.find((item) => item.id === source.connectionId) : undefined;
  const table = grid.object === "table" ? grid.selectedTable : undefined;
  return dbResultServiceTarget(grid, {
    running: grid.object === "table" ? databaseStore.loading || databaseStore.queryRunning : grid.activeQueryRunning,
    connection,
    kind: connection ? databaseStore.connectionDialectType(connection) : undefined,
    database: source.database,
    tableKind: connection && table
      ? databaseStore.schemas[connection.id]?.databases.find((item) => item.name === source.database)?.tables.find((item) => item.name === table)?.kind
      : undefined,
  });
}
</script>

<template>
  <PluginServiceMenuItems :target="target" @run="emit('run')" />
</template>
