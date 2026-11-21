import type { DatabaseDiagramTable } from "./types";

export interface DatabaseDiagramNode {
  table: DatabaseDiagramTable;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface DatabaseDiagramEdge {
  id: string;
  sourceTable: string;
  targetTable: string;
  sourceX: number;
  sourceY: number;
  targetX: number;
  targetY: number;
  label: string;
}

export interface DatabaseDiagramLayout {
  nodes: DatabaseDiagramNode[];
  edges: DatabaseDiagramEdge[];
  width: number;
  height: number;
}

const CARD_WIDTH = 260;
const HEADER_HEIGHT = 38;
const COLUMN_HEIGHT = 25;
const CARD_PADDING = 14;
const COLUMN_LIMIT = 12;
const HORIZONTAL_GAP = 72;
const VERTICAL_GAP = 56;

function cardHeight(table: DatabaseDiagramTable) {
  return (
    HEADER_HEIGHT + Math.min(COLUMN_LIMIT, table.columns.length) * COLUMN_HEIGHT + CARD_PADDING
  );
}

export function layoutDatabaseDiagram(
  tables: DatabaseDiagramTable[],
  columnsPerRow = 3
): DatabaseDiagramLayout {
  const safeColumns = Math.max(1, columnsPerRow);
  const rowHeights: number[] = [];
  const nodes = tables.map((table, index) => {
    const row = Math.floor(index / safeColumns);
    const height = cardHeight(table);
    rowHeights[row] = Math.max(rowHeights[row] ?? 0, height);
    return { table, row, column: index % safeColumns, height };
  });

  const rowOffsets: number[] = [];
  rowHeights.reduce((offset, height, index) => {
    rowOffsets[index] = offset;
    return offset + height + VERTICAL_GAP;
  }, 24);

  const positioned: DatabaseDiagramNode[] = nodes.map(({ table, row, column, height }) => ({
    table,
    x: 24 + column * (CARD_WIDTH + HORIZONTAL_GAP),
    y: rowOffsets[row] ?? 24,
    width: CARD_WIDTH,
    height,
  }));
  const nodeMap = new Map(positioned.map((node) => [node.table.name, node]));
  const edges: DatabaseDiagramEdge[] = [];

  for (const source of positioned) {
    source.table.foreignKeys.forEach((foreignKey, index) => {
      const target = nodeMap.get(foreignKey.referencedTable);
      if (!target) return;
      edges.push({
        id: `${source.table.name}:${foreignKey.name || index}:${target.table.name}`,
        sourceTable: source.table.name,
        targetTable: target.table.name,
        sourceX: source.x + source.width,
        sourceY: source.y + Math.min(source.height - 16, HEADER_HEIGHT + 14 + index * 14),
        targetX: target.x,
        targetY: target.y + Math.min(target.height - 16, HEADER_HEIGHT + 14),
        label: foreignKey.name || foreignKey.columns.join(", "),
      });
    });
  }

  const width = Math.max(720, ...positioned.map((node) => node.x + node.width + 24));
  const height = Math.max(460, ...positioned.map((node) => node.y + node.height + 24));
  return { nodes: positioned, edges, width, height };
}
