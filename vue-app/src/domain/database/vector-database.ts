import type { DbType } from "@/domain/database/types";

export const vectorDatabaseTypes = [
  "pgvector",
  "qdrant",
  "milvus",
  "weaviate",
  "pinecone",
  "chroma",
] as const satisfies readonly DbType[];

const vectorDatabaseTypeSet = new Set<DbType>(vectorDatabaseTypes);

export function isVectorDatabaseType(dbType: DbType | undefined): boolean {
  return dbType !== undefined && vectorDatabaseTypeSet.has(dbType);
}

export interface VectorCollectionCreateRequest {
  name: string;
  dimension: number;
  metric: "cosine" | "euclidean" | "dotproduct";
  cloud: string;
  region: string;
  vectorName: string;
}
