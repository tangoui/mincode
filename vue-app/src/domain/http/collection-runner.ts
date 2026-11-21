import type { SavedHttpRequest } from "./request-collection";

export interface HttpCollectionRunResponse {
  status: number;
  status_text: string;
  headers: [string, string][];
  body: string;
  elapsed_ms: number;
  size_bytes: number;
}

export interface HttpCollectionRunResult {
  requestId: string;
  requestName: string;
  method: string;
  url: string;
  passed: boolean;
  status?: number;
  elapsedMs: number;
  error?: string;
}

export interface HttpCollectionRunSummary {
  total: number;
  passed: number;
  failed: number;
  durationMs: number;
  results: HttpCollectionRunResult[];
}

export function evaluateHttpCollectionResponse(
  request: SavedHttpRequest,
  response: HttpCollectionRunResponse
): HttpCollectionRunResult {
  return {
    requestId: request.id,
    requestName: request.name,
    method: request.method,
    url: request.url,
    passed: response.status >= 200 && response.status < 400,
    status: response.status,
    elapsedMs: response.elapsed_ms,
  };
}

export function summarizeHttpCollectionRun(
  results: HttpCollectionRunResult[],
  durationMs: number
): HttpCollectionRunSummary {
  const passed = results.filter((result) => result.passed).length;
  return {
    total: results.length,
    passed,
    failed: results.length - passed,
    durationMs,
    results,
  };
}
