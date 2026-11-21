import {
  cleanDatabaseAiQueryOutput,
  databaseAiQueryGuide,
} from "@/domain/database/ai-query";
import type { DbType } from "@/domain/database/types";
import type {
  AiLoopConfig,
  AiLoopMessage,
  AiLoopStreamEvent,
  streamAiLoopTurn,
} from "@/services/ai-agent-loop";
import { streamAiTurnWithFailover } from "@/services/ai-model-failover";

const DATABASE_AI_QUERY_MAX_OUTPUT_TOKENS = 2048;
const DATABASE_AI_QUERY_MAX_COLUMNS = 60;
const DATABASE_AI_QUERY_MAX_TABLES = 50;

export interface DatabaseAiQueryColumn {
  name: string;
  dataType?: string;
}

export interface DatabaseAiQueryContext {
  dbType: DbType;
  database?: string;
  table?: string;
  columns?: DatabaseAiQueryColumn[];
  tables?: string[];
}

export interface GenerateAiDatabaseQueryOptions {
  config: AiLoopConfig;
  description: string;
  context: DatabaseAiQueryContext;
  signal?: AbortSignal;
  onDelta?: (delta: string) => void;
  streamTurn?: typeof streamAiLoopTurn;
}

export function databaseAiQueryPrompt(
  description: string,
  context: DatabaseAiQueryContext,
): AiLoopMessage[] {
  const guide = databaseAiQueryGuide(context.dbType);
  const lines = [`数据库类型：${context.dbType}`];
  if (context.database?.trim()) lines.push(`当前库：${context.database.trim()}`);
  if (context.table?.trim()) lines.push(`当前表/集合：${context.table.trim()}`);
  const columns = (context.columns ?? []).slice(0, DATABASE_AI_QUERY_MAX_COLUMNS);
  if (columns.length) {
    lines.push(
      `字段：${columns
        .map((column) =>
          column.dataType ? `${column.name} (${column.dataType})` : column.name,
        )
        .join(", ")}`,
    );
  }
  const tables = (context.tables ?? [])
    .filter((name) => name.trim() && name !== context.table)
    .slice(0, DATABASE_AI_QUERY_MAX_TABLES);
  if (tables.length) lines.push(`同库其他表/集合：${tables.join(", ")}`);

  return [
    {
      role: "system",
      content: `你是数据库查询生成助手。根据自然语言描述生成可直接执行的查询。${guide.instruction}`,
    },
    {
      role: "user",
      content: `${lines.join("\n")}\n\n查询目标：${description}`,
    },
  ];
}

/** 使用无工具、无历史的流式短请求生成查询文本，首个文本片段可立即展示。 */
export async function generateAiDatabaseQuery(
  options: GenerateAiDatabaseQueryOptions,
) {
  const description = options.description.trim();
  if (!description) throw new Error("Query description is empty.");

  let generated = "";
  await streamAiTurnWithFailover({
    config: {
      ...options.config,
      reasoningEffort: undefined,
      maxOutputTokens: Math.min(
        options.config.maxOutputTokens || DATABASE_AI_QUERY_MAX_OUTPUT_TOKENS,
        DATABASE_AI_QUERY_MAX_OUTPUT_TOKENS,
      ),
    },
    models: [options.config.model],
    messages: databaseAiQueryPrompt(description, options.context),
    tools: [],
    signal: options.signal,
    streamTurn: options.streamTurn,
    onEvent: (event: AiLoopStreamEvent) => {
      if (event.type !== "chunk" || !event.text) return;
      generated += event.text;
      options.onDelta?.(event.text);
    },
  });

  const result = cleanDatabaseAiQueryOutput(generated);
  if (!result) throw new Error("AI did not return a query.");
  return result;
}
